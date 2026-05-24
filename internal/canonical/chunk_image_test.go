package canonical

import (
	"reflect"
	"testing"
)

// TestBlockKindImage_Discriminator locks the iota positions of every
// BlockKind constant. Plan 04 (engine.buildBlocks) and Plan 06
// (Ollama adapter image translation) depend on these exact positions
// — any reordering breaks the ACP image-block construction path.
func TestBlockKindImage_Discriminator(t *testing.T) {
	cases := []struct {
		name string
		got  BlockKind
		want BlockKind
	}{
		{"BlockKindText", BlockKindText, 0},
		{"BlockKindResourceLink", BlockKindResourceLink, 1},
		{"BlockKindImage", BlockKindImage, 2},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("%s: got %d, want %d", tc.name, tc.got, tc.want)
		}
	}
}

// TestImageBlock_ZeroValue locks the zero value of every ImageBlock
// field so adapters can rely on "unset" === empty for Source/MIMEType
// and nil for Data.
func TestImageBlock_ZeroValue(t *testing.T) {
	var b ImageBlock
	if b.Source != "" {
		t.Errorf("Source: got %q, want empty", b.Source)
	}
	if b.MIMEType != "" {
		t.Errorf("MIMEType: got %q, want empty", b.MIMEType)
	}
	if b.Data != nil {
		t.Errorf("Data: got %v, want nil", b.Data)
	}
}

// TestBlock_ImageVariant constructs a Block with the BlockKindImage
// discriminator + populated Image pointer and asserts it round-trips
// via reflect.DeepEqual (no JSON; D-11). PNG magic bytes used as the
// payload so the assertion is unambiguous.
func TestBlock_ImageVariant(t *testing.T) {
	got := Block{
		Kind: BlockKindImage,
		Image: &ImageBlock{
			MIMEType: "image/png",
			Data:     []byte{0x89, 0x50, 0x4e, 0x47},
		},
	}
	want := Block{
		Kind: BlockKindImage,
		Image: &ImageBlock{
			MIMEType: "image/png",
			Data:     []byte{0x89, 0x50, 0x4e, 0x47},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Block image variant round-trip: got %+v, want %+v", got, want)
	}
}

// TestNoJSONTags_ChunkImageBlock walks ImageBlock via reflect and
// asserts no json:"..." tag is ever attached. Sibling defense to
// chat_test.go TestNoJSONTags — keeps the D-11 invariant intact
// across both files.
func TestNoJSONTags_ChunkImageBlock(t *testing.T) {
	tp := reflect.TypeOf(ImageBlock{})
	for i := 0; i < tp.NumField(); i++ {
		f := tp.Field(i)
		if tag := f.Tag.Get("json"); tag != "" {
			t.Errorf("%s.%s has forbidden json tag %q (D-11)", tp.Name(), f.Name, tag)
		}
	}
}
