package imagemeta

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// The fixtures in testdata are 33x17 images produced by real encoders
// (ffmpeg with libwebp, libaom and its built-in encoders), so the hand-written
// WebP/BMP/AVIF parsers are checked against what encoders actually emit.
var fixtures = []struct{ file, format string }{
	{"page.jpg", "jpeg"},
	{"page.png", "png"},
	{"page.gif", "gif"},
	{"page.bmp", "bmp"},
	{"lossy.webp", "webp"},    // VP8 chunk
	{"lossless.webp", "webp"}, // VP8L chunk
	{"extended.webp", "webp"}, // VP8X chunk (alpha)
	{"page.avif", "avif"},     // single image
	{"alpha.avif", "avif"},    // primary image plus an alpha plane
}

func read(t testing.TB, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestDecodeFixtures(t *testing.T) {
	for _, f := range fixtures {
		got, err := Decode(bytes.NewReader(read(t, f.file)))
		if err != nil {
			t.Errorf("%s: %v", f.file, err)
			continue
		}
		if want := (Info{Format: f.format, Width: 33, Height: 17}); got != want {
			t.Errorf("%s: got %+v, want %+v", f.file, got, want)
		}
	}
}

// TestTruncated feeds every prefix of every fixture: each must either fail
// cleanly or (once the header is complete) report the right size — never
// panic or report a wrong size.
func TestTruncated(t *testing.T) {
	for _, f := range fixtures {
		data := read(t, f.file)
		for n := range len(data) {
			got, err := Decode(bytes.NewReader(data[:n]))
			if err == nil && (got.Width != 33 || got.Height != 17) {
				t.Errorf("%s[:%d]: got %+v with no error", f.file, n, got)
			}
		}
	}
}

func TestUnknown(t *testing.T) {
	for _, in := range [][]byte{nil, []byte("hello, world"), []byte("PK\x03\x04 not an image")} {
		if _, err := Decode(bytes.NewReader(in)); !errors.Is(err, ErrUnknown) {
			t.Errorf("Decode(%q) err = %v, want ErrUnknown", in, err)
		}
	}
}

// TestBoxHostileSizes checks the ISOBMFF scanner against lengths that point
// past the buffer, below the header size, or use the 64-bit form.
func TestBoxHostileSizes(t *testing.T) {
	cases := [][]byte{
		{0xff, 0xff, 0xff, 0xff, 'f', 't', 'y', 'p'},                         // past the end
		{0, 0, 0, 4, 'f', 't', 'y', 'p'},                                     // smaller than its header
		{0, 0, 0, 1, 'f', 't', 'y', 'p', 0xff, 0xff, 0xff, 0xff, 0, 0, 0, 0}, // 64-bit, past the end
		{0, 0, 0, 1, 'f', 't', 'y', 'p'},                                     // 64-bit, truncated
	}
	for _, c := range cases {
		if _, _, ok := box(c, "ftyp"); ok {
			t.Errorf("box(%x) accepted a hostile size", c)
		}
	}
	// A size of 0 means "to the end".
	if p, _, ok := box([]byte{0, 0, 0, 0, 'f', 't', 'y', 'p', 'a', 'v', 'i', 'f'}, "ftyp"); !ok || string(p) != "avif" {
		t.Errorf("size-0 box = %q, %v", p, ok)
	}
}

// FuzzDecode runs the fixtures (and, under `go test -fuzz`, mutations of
// them) through Decode: it must never panic, and a success must be sane.
func FuzzDecode(f *testing.F) {
	for _, fx := range fixtures {
		f.Add(read(f, fx.file))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		info, err := Decode(bytes.NewReader(data))
		if err == nil && (info.Width <= 0 || info.Height <= 0 || info.Width > maxSide || info.Height > maxSide) {
			t.Errorf("accepted %+v", info)
		}
	})
}
