package engine

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	"otto-gateway/internal/canonical"
)

const (
	maxToolProtocolPreflightBytes  = 1 << 20
	maxToolProtocolPreflightChunks = 4096
)

const replayStreamBufferSize = 64

// replayStream owns an immutable snapshot of a fully drained source stream.
type replayStream struct {
	chunks []canonical.Chunk
	final  *canonical.FinalResult
	err    error

	start sync.Once
	out   chan canonical.Chunk
	done  chan struct{}
}

func newReplayStream(chunks []canonical.Chunk, final *canonical.FinalResult, err error) *replayStream {
	owned := cloneChunks(chunks)
	capacity := len(owned)
	if capacity > replayStreamBufferSize {
		capacity = replayStreamBufferSize
	}
	return &replayStream{
		chunks: owned,
		final:  cloneFinalResult(final),
		err:    err,
		out:    make(chan canonical.Chunk, capacity),
		done:   make(chan struct{}),
	}
}

func (s *replayStream) Chunks() <-chan canonical.Chunk {
	s.start.Do(func() {
		go func() {
			defer close(s.done)
			defer close(s.out)
			for _, chunk := range s.chunks {
				s.out <- chunk
			}
		}()
	})
	return s.out
}

func (s *replayStream) Result() (*canonical.FinalResult, error) {
	s.Chunks()
	<-s.done
	return cloneFinalResult(s.final), s.err
}

// prefixLiveStream replays an owned consumed prefix before forwarding the
// original stream's still-unread channel. Its single-slot output preserves
// downstream backpressure while every send remains cancellable.
type prefixLiveStream struct {
	ctx      context.Context
	prefix   []canonical.Chunk
	boundary *canonical.Chunk
	source   Stream
	finish   func()

	start        sync.Once
	terminalOnce sync.Once
	out          chan canonical.Chunk
	done         chan struct{}
	final        *canonical.FinalResult
	err          error
}

func newPrefixLiveStream(ctx context.Context, prefix []canonical.Chunk, source Stream, finish func()) *prefixLiveStream {
	return newPrefixLiveStreamWithBoundary(ctx, prefix, nil, source, finish)
}

func newPrefixLiveStreamWithBoundary(
	ctx context.Context,
	prefix []canonical.Chunk,
	boundary *canonical.Chunk,
	source Stream,
	finish func(),
) *prefixLiveStream {
	if ctx == nil {
		ctx = context.Background()
	}
	return &prefixLiveStream{
		ctx:      ctx,
		prefix:   cloneChunks(prefix),
		boundary: boundary,
		source:   source,
		finish:   finish,
		out:      make(chan canonical.Chunk, 1),
		done:     make(chan struct{}),
	}
}

func (s *prefixLiveStream) Chunks() <-chan canonical.Chunk {
	s.start.Do(func() { go s.pump() })
	return s.out
}

func (s *prefixLiveStream) pump() {
	for _, chunk := range s.prefix {
		if !s.send(chunk) {
			s.closeAndFinish()
			return
		}
	}
	if s.boundary != nil && !s.send(*s.boundary) {
		s.closeAndFinish()
		return
	}

	live := s.source.Chunks()
	for {
		select {
		case <-s.ctx.Done():
			s.closeAndFinish()
			return
		case chunk, ok := <-live:
			if !ok {
				s.closeAndFinish()
				return
			}
			if !s.send(chunk) {
				s.closeAndFinish()
				return
			}
		}
	}
}

func (s *prefixLiveStream) send(chunk canonical.Chunk) bool {
	select {
	case <-s.ctx.Done():
		return false
	case s.out <- chunk:
		return true
	}
}

func (s *prefixLiveStream) closeAndFinish() {
	close(s.out)
	s.finishTerminal()
}

func (s *prefixLiveStream) finishTerminal() {
	s.terminalOnce.Do(func() {
		s.final, s.err = s.source.Result()
		s.final = cloneFinalResult(s.final)
		if s.finish != nil {
			s.finish()
		}
		close(s.done)
	})
}

func (s *prefixLiveStream) Result() (*canonical.FinalResult, error) {
	s.Chunks()
	<-s.done
	return cloneFinalResult(s.final), s.err
}

// toolProtocolAttemptCapture labels the two products of a successful bounded
// capture so Engine.Run integration cannot accidentally swap their roles.
type toolProtocolAttemptCapture struct {
	observation attemptObservation
	stream      Stream
}

var errToolProtocolCaptureHandoff = errors.New("tool protocol capture handoff")

// captureToolProtocolAttempt drains a source only while its retained prefix
// remains bounded. A decisive matching native call or the first bound breach
// hands the consumed prefix and untouched live remainder to a composite stream.
func captureToolProtocolAttempt(
	ctx context.Context,
	source Stream,
	idle time.Duration,
	policy toolProtocolPolicy,
	aliases map[string]string,
	finish func(),
) (toolProtocolAttemptCapture, error) {
	if source == nil {
		return toolProtocolAttemptCapture{}, errors.New("engine: tool protocol capture: nil stream")
	}

	// Cache Chunks exactly once. Stream implementations conventionally return
	// one stable channel, but this wrapper makes the untouched-tail guarantee
	// independent of repeated Chunks method behavior.
	stable := &stableChunkStream{chunks: source.Chunks(), source: source}
	var (
		observation  attemptObservation
		prefix       []canonical.Chunk
		boundary     *canonical.Chunk
		retainedSize int
		chunkCount   int
		text         strings.Builder
	)

	rangeErr := RangeChunksWithIdleTimeout(ctx, stable, idle, func(chunk canonical.Chunk) error {
		chunkCount++
		chunkSize := retainedChunkBytes(chunk)
		if retainedSize > maxToolProtocolPreflightBytes-chunkSize {
			retainedSize = maxToolProtocolPreflightBytes + 1
		} else {
			retainedSize += chunkSize
		}

		if chunkCount > maxToolProtocolPreflightChunks || retainedSize > maxToolProtocolPreflightBytes {
			observation.BufferBypass = true
			// The already-consumed boundary cannot be put back on source. Keep
			// its immutable ACP value separately so the retained copied prefix
			// remains within both caps and the next source chunk stays unread.
			boundary = &chunk
			return errToolProtocolCaptureHandoff
		}
		prefix = append(prefix, cloneChunk(chunk))

		switch chunk.Kind {
		case canonical.ChunkKindText:
			if chunk.Text != nil {
				text.WriteString(chunk.Text.Content)
			}
		case canonical.ChunkKindToolCall:
			if chunk.ToolCall == nil {
				break
			}
			observation.NativeCall = true
			observation.ToolCalls = append(observation.ToolCalls, canonical.ToolCall{
				ID:        chunk.ToolCall.ID,
				Name:      chunk.ToolCall.Name,
				Arguments: cloneStringAnyMap(chunk.ToolCall.Args),
			})
			if nativeCallSatisfiesPolicy(chunk.ToolCall.Name, policy, aliases) {
				return errToolProtocolCaptureHandoff
			}
		}
		return nil
	})

	observation.Text = text.String()
	if errors.Is(rangeErr, errToolProtocolCaptureHandoff) {
		return toolProtocolAttemptCapture{
			observation: observation,
			stream:      newPrefixLiveStreamWithBoundary(ctx, prefix, boundary, stable, finish),
		}, nil
	}
	if rangeErr != nil {
		return toolProtocolAttemptCapture{observation: observation}, rangeErr
	}

	final, resultErr := source.Result()
	observation.Final = cloneFinalResult(final)
	if resultErr != nil {
		return toolProtocolAttemptCapture{observation: observation}, fmt.Errorf("engine: tool protocol capture result: %w", resultErr)
	}
	return toolProtocolAttemptCapture{
		observation: observation,
		stream:      newReplayStream(prefix, final, nil),
	}, nil
}

func nativeCallSatisfiesPolicy(name string, policy toolProtocolPolicy, aliases map[string]string) bool {
	resolved, surface := ResolveNativeToolName(name, policy.tools, aliases)
	if !surface {
		return false
	}
	return policy.requirement != toolProtocolNamed || resolved == policy.namedTool
}

type stableChunkStream struct {
	chunks <-chan canonical.Chunk
	source Stream
}

func (s *stableChunkStream) Chunks() <-chan canonical.Chunk { return s.chunks }

func (s *stableChunkStream) Result() (*canonical.FinalResult, error) { return s.source.Result() }

func cloneChunks(chunks []canonical.Chunk) []canonical.Chunk {
	if chunks == nil {
		return nil
	}
	out := make([]canonical.Chunk, len(chunks))
	for i, chunk := range chunks {
		out[i] = cloneChunk(chunk)
	}
	return out
}

func cloneChunk(chunk canonical.Chunk) canonical.Chunk {
	clone := canonical.Chunk{Kind: chunk.Kind}
	if chunk.Text != nil {
		text := *chunk.Text
		clone.Text = &text
	}
	if chunk.Thought != nil {
		thought := *chunk.Thought
		clone.Thought = &thought
	}
	if chunk.ToolCall != nil {
		toolCall := *chunk.ToolCall
		toolCall.Args = cloneStringAnyMap(chunk.ToolCall.Args)
		clone.ToolCall = &toolCall
	}
	if chunk.Plan != nil {
		plan := *chunk.Plan
		clone.Plan = &plan
	}
	return clone
}

func cloneFinalResult(final *canonical.FinalResult) *canonical.FinalResult {
	if final == nil {
		return nil
	}
	clone := *final
	return &clone
}

func cloneStringAnyMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = cloneRetainedValue(value)
	}
	return out
}

func cloneRetainedValue(value any) any {
	switch value := value.(type) {
	case map[string]any:
		return cloneStringAnyMap(value)
	case []any:
		clone := make([]any, len(value))
		for i, item := range value {
			clone[i] = cloneRetainedValue(item)
		}
		return clone
	case []byte:
		return append([]byte(nil), value...)
	default:
		return value
	}
}

func retainedChunkBytes(chunk canonical.Chunk) int {
	switch chunk.Kind {
	case canonical.ChunkKindText:
		if chunk.Text != nil {
			return len(chunk.Text.Content)
		}
	case canonical.ChunkKindThought:
		if chunk.Thought != nil {
			return len(chunk.Thought.Content)
		}
	case canonical.ChunkKindToolCall:
		if chunk.ToolCall != nil {
			return len(chunk.ToolCall.ID) + len(chunk.ToolCall.Name) + retainedValueBytes(chunk.ToolCall.Args)
		}
	case canonical.ChunkKindPlan:
		if chunk.Plan != nil {
			return len(chunk.Plan.Content)
		}
	}
	return 0
}

func retainedValueBytes(value any) int {
	switch value := value.(type) {
	case nil:
		return 0
	case string:
		return len(value)
	case []byte:
		return len(value)
	case map[string]any:
		total := 0
		for key, item := range value {
			total += len(key) + retainedValueBytes(item)
		}
		return total
	case []any:
		total := 0
		for _, item := range value {
			total += retainedValueBytes(item)
		}
		return total
	default:
		typeOf := reflect.TypeOf(value)
		if typeOf == nil {
			return 0
		}
		return int(typeOf.Size())
	}
}

var (
	_ Stream = (*replayStream)(nil)
	_ Stream = (*prefixLiveStream)(nil)
)
