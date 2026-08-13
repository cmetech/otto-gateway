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
	ctx    context.Context
	chunks []canonical.Chunk
	final  *canonical.FinalResult
	err    error

	start sync.Once
	out   chan canonical.Chunk
	done  chan struct{}
}

func newReplayStream(ctx context.Context, chunks []canonical.Chunk, final *canonical.FinalResult, err error) *replayStream {
	if ctx == nil {
		ctx = context.Background()
	}
	owned := cloneChunks(chunks)
	capacity := len(owned)
	if capacity > replayStreamBufferSize {
		capacity = replayStreamBufferSize
	}
	return &replayStream{
		ctx:    ctx,
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
			if err := s.ctx.Err(); err != nil {
				s.err = err
				return
			}
			for _, chunk := range s.chunks {
				select {
				case <-s.ctx.Done():
					s.err = s.ctx.Err()
					return
				case s.out <- chunk:
				}
			}
			if err := s.ctx.Err(); err != nil {
				s.err = err
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
	live := s.source.Chunks()
	for _, chunk := range s.prefix {
		if !s.send(chunk) {
			s.cancelAndFinish(live)
			return
		}
	}
	if s.boundary != nil && !s.send(*s.boundary) {
		s.cancelAndFinish(live)
		return
	}

	for {
		select {
		case <-s.ctx.Done():
			s.cancelAndFinish(live)
			return
		case chunk, ok := <-live:
			if !ok {
				s.closeAndFinish()
				return
			}
			if !s.send(chunk) {
				s.cancelAndFinish(live)
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

func (s *prefixLiveStream) cancelAndFinish(live <-chan canonical.Chunk) {
	// Stop the downstream stream immediately, but keep consuming upstream.
	// Some Stream implementations cannot close or make Result available until
	// a backpressured producer has delivered every pending chunk.
	close(s.out)
	for range live {
	}
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
		remaining := maxToolProtocolPreflightBytes - retainedSize
		chunkSize, unsafeToRetain := retainedChunkBytesWithinBudget(chunk, remaining)
		var overflow bool
		retainedSize, overflow = saturatingRetainedBytes(retainedSize, chunkSize, maxToolProtocolPreflightBytes)

		if chunkCount > maxToolProtocolPreflightChunks || unsafeToRetain || overflow {
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
		stream:      newReplayStream(ctx, prefix, final, nil),
	}, nil
}

func nativeCallSatisfiesPolicy(name string, policy toolProtocolPolicy, aliases map[string]string) bool {
	if len(policy.tools) == 0 {
		return false
	}
	resolved, surface := ResolveNativeToolName(name, policy.tools, aliases)
	if !surface || !toolOffered(resolved, policy.tools) {
		return false
	}
	return policy.requirement != toolProtocolNamed || resolved == policy.namedTool
}

type stableChunkStream struct {
	chunks <-chan canonical.Chunk
	source Stream
}

func (s *stableChunkStream) Chunks() <-chan canonical.Chunk { return s.chunks }

func (s *stableChunkStream) Result() (*canonical.FinalResult, error) {
	result, err := s.source.Result()
	if err != nil {
		return result, fmt.Errorf("engine: stable stream result: %w", err)
	}
	return result, nil
}

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

const (
	maxRetainedValueDepth    = 64
	retainedMapBaseBytes     = 64
	retainedMapEntryBytes    = 64
	retainedSliceHeaderBytes = 24
	retainedInterfaceBytes   = 16
)

type retainedVisit struct {
	kind reflect.Kind
	ptr  uintptr
}

type retainedByteWalker struct {
	limit    int
	used     int
	exceeded bool
	visiting map[retainedVisit]struct{}
}

// retainedChunkBytesWithinBudget conservatively accounts for payload and
// JSON-container storage that cloneChunk would retain. It saturates as soon as
// budget is exceeded and rejects cyclic/excessively deep graphs before clone.
func retainedChunkBytesWithinBudget(chunk canonical.Chunk, budget int) (int, bool) {
	walker := retainedByteWalker{
		limit:    budget,
		visiting: make(map[retainedVisit]struct{}),
	}
	if budget < 0 {
		walker.markExceeded()
		return walker.used, true
	}
	switch chunk.Kind {
	case canonical.ChunkKindText:
		if chunk.Text != nil {
			walker.add(len(chunk.Text.Content))
		}
	case canonical.ChunkKindThought:
		if chunk.Thought != nil {
			walker.add(len(chunk.Thought.Content))
		}
	case canonical.ChunkKindToolCall:
		if chunk.ToolCall != nil {
			walker.add(len(chunk.ToolCall.ID))
			walker.add(len(chunk.ToolCall.Name))
			walker.walkValue(chunk.ToolCall.Args, 0)
		}
	case canonical.ChunkKindPlan:
		if chunk.Plan != nil {
			walker.add(len(chunk.Plan.Content))
		}
	}
	return walker.used, walker.exceeded
}

func (w *retainedByteWalker) walkValue(value any, depth int) {
	if w.exceeded {
		return
	}
	if depth > maxRetainedValueDepth {
		w.markExceeded()
		return
	}

	switch value := value.(type) {
	case nil:
		return
	case string:
		w.add(len(value))
	case []byte:
		w.add(retainedSliceHeaderBytes)
		w.add(len(value))
	case map[string]any:
		visit := retainedVisit{kind: reflect.Map, ptr: reflect.ValueOf(value).Pointer()}
		if !w.enter(visit) {
			return
		}
		defer w.leave(visit)
		w.add(retainedMapBaseBytes)
		w.addProduct(len(value), retainedMapEntryBytes)
		if w.exceeded {
			return
		}
		for key, item := range value {
			w.add(len(key))
			w.walkValue(item, depth+1)
			if w.exceeded {
				return
			}
		}
	case []any:
		visit := retainedVisit{kind: reflect.Slice, ptr: reflect.ValueOf(value).Pointer()}
		if !w.enter(visit) {
			return
		}
		defer w.leave(visit)
		w.add(retainedSliceHeaderBytes)
		w.addProduct(len(value), retainedInterfaceBytes)
		if w.exceeded {
			return
		}
		for _, item := range value {
			w.walkValue(item, depth+1)
			if w.exceeded {
				return
			}
		}
	default:
		typeOf := reflect.TypeOf(value)
		if typeOf != nil {
			size := typeOf.Size()
			if size > uintptr(^uint(0)>>1) {
				w.markExceeded()
				return
			}
			w.add(int(size))
		}
	}
}

func (w *retainedByteWalker) enter(visit retainedVisit) bool {
	if visit.ptr == 0 {
		return true
	}
	if _, exists := w.visiting[visit]; exists {
		w.markExceeded()
		return false
	}
	w.visiting[visit] = struct{}{}
	return true
}

func (w *retainedByteWalker) leave(visit retainedVisit) {
	if visit.ptr != 0 {
		delete(w.visiting, visit)
	}
}

func (w *retainedByteWalker) add(value int) {
	if w.exceeded {
		return
	}
	if value < 0 || value > w.limit || w.used > w.limit-value {
		w.markExceeded()
		return
	}
	w.used += value
}

func (w *retainedByteWalker) addProduct(count, size int) {
	if w.exceeded {
		return
	}
	if count < 0 || size < 0 || (size != 0 && count > (w.limit-w.used)/size) {
		w.markExceeded()
		return
	}
	w.add(count * size)
}

func (w *retainedByteWalker) markExceeded() {
	w.exceeded = true
	if w.limit < int(^uint(0)>>1) {
		w.used = w.limit + 1
	} else {
		w.used = w.limit
	}
}

func saturatingRetainedBytes(total, addition, limit int) (int, bool) {
	if limit < 0 || total < 0 || addition < 0 || total > limit || addition > limit || total > limit-addition {
		if limit >= 0 && limit < int(^uint(0)>>1) {
			return limit + 1, true
		}
		return limit, true
	}
	return total + addition, false
}

var (
	_ Stream = (*replayStream)(nil)
	_ Stream = (*prefixLiveStream)(nil)
)
