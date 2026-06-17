package format

import (
	"bytes"
	"encoding/hex"
	"errors"
	"image"
	"image/color"
	"sort"
	"testing"
)

// minimalWebPHex is a 38-byte VP8L (lossless) WebP encoding a 4x4 white image.
// Generated via ffmpeg:
//
//	ffmpeg -f lavfi -i color=white:s=4x4 -frames:v 1 -c:v libwebp -lossless 1 out.webp
//
// We embed it because splat does not encode WebP itself, so the test cannot
// produce its own fixture.
const minimalWebPHex = "524946461e000000574542505650384c110000002f03c0000007d0fffef7bfff8188e87f0000"

// makeImage builds a 4x4 RGBA image with a deterministic pattern. Used for
// round-trip encode/decode tests.
func makeImage() *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			img.SetRGBA(x, y, color.RGBA{
				R: uint8(x * 60),
				G: uint8(y * 60),
				B: uint8((x + y) * 30),
				A: 255,
			})
		}
	}
	return img
}

func TestNewRegistry_HappyPath(t *testing.T) {
	r, err := NewRegistry(50)
	if err != nil {
		t.Fatalf("NewRegistry(50) returned error: %v", err)
	}
	if r == nil {
		t.Fatal("NewRegistry(50) returned nil registry")
	}
}

func TestNewRegistry_InvalidQuality(t *testing.T) {
	for _, q := range []int{0, -1, 101, 1000} {
		r, err := NewRegistry(q)
		if err == nil {
			t.Errorf("NewRegistry(%d) expected error, got nil", q)
		}
		if r != nil {
			t.Errorf("NewRegistry(%d) expected nil registry on error, got %v", q, r)
		}
	}
	// Boundary: 1 and 100 must succeed.
	for _, q := range []int{1, 100} {
		if _, err := NewRegistry(q); err != nil {
			t.Errorf("NewRegistry(%d) unexpected error: %v", q, err)
		}
	}
}

func TestByExt(t *testing.T) {
	r, err := NewRegistry(80)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	cases := []struct {
		ext      string
		wantOK   bool
		wantCT   string
		wantCExt string // canonical Ext
	}{
		{".jpg", true, "image/jpeg", ".jpg"},
		{".JPG", true, "image/jpeg", ".jpg"},
		{".jpeg", true, "image/jpeg", ".jpg"},
		{".JPEG", true, "image/jpeg", ".jpg"},
		{".png", true, "image/png", ".png"},
		{".PNG", true, "image/png", ".png"},
		{".webp", true, "image/webp", ".webp"},
		{".gif", false, "", ""},
		{"", false, "", ""},
		{"jpg", false, "", ""}, // missing leading dot
	}
	for _, tc := range cases {
		got, ok := r.ByExt(tc.ext)
		if ok != tc.wantOK {
			t.Errorf("ByExt(%q) ok = %v, want %v", tc.ext, ok, tc.wantOK)
			continue
		}
		if !ok {
			continue
		}
		if got.ContentType != tc.wantCT {
			t.Errorf("ByExt(%q) ContentType = %q, want %q", tc.ext, got.ContentType, tc.wantCT)
		}
		if got.Ext != tc.wantCExt {
			t.Errorf("ByExt(%q) Ext = %q, want %q", tc.ext, got.Ext, tc.wantCExt)
		}
	}
}

func TestIsSupported(t *testing.T) {
	r, err := NewRegistry(80)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	cases := map[string]bool{
		".jpg":  true,
		".JPG":  true,
		".jpeg": true,
		".png":  true,
		".webp": true,
		".gif":  false,
		"":      false,
		"jpg":   false,
	}
	for ext, want := range cases {
		if got := r.IsSupported(ext); got != want {
			t.Errorf("IsSupported(%q) = %v, want %v", ext, got, want)
		}
	}
}

func TestSupportedExtensions_Sorted(t *testing.T) {
	r, err := NewRegistry(80)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	got := r.SupportedExtensions()
	// Verify it contains the four required entries.
	want := []string{".jpg", ".jpeg", ".png", ".webp"}
	for _, w := range want {
		found := false
		for _, g := range got {
			if g == w {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("SupportedExtensions() missing %q; got %v", w, got)
		}
	}
	// Verify sorted order.
	if !sort.StringsAreSorted(got) {
		t.Errorf("SupportedExtensions() not sorted: %v", got)
	}
}

func TestOutputFor(t *testing.T) {
	r, err := NewRegistry(80)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}

	// .webp → png handler, output ext .png.
	out, ext, ok := r.OutputFor(".webp")
	if !ok {
		t.Fatal("OutputFor(.webp) ok=false")
	}
	if ext != ".png" {
		t.Errorf("OutputFor(.webp) ext = %q, want .png", ext)
	}
	if out.Ext != ".png" || out.ContentType != "image/png" {
		t.Errorf("OutputFor(.webp) returned wrong format: %+v", out)
	}

	// .jpg → jpg handler, output ext .jpg.
	out, ext, ok = r.OutputFor(".jpg")
	if !ok {
		t.Fatal("OutputFor(.jpg) ok=false")
	}
	if ext != ".jpg" {
		t.Errorf("OutputFor(.jpg) ext = %q, want .jpg", ext)
	}
	if out.Ext != ".jpg" {
		t.Errorf("OutputFor(.jpg) returned wrong format: %+v", out)
	}

	// .jpeg alias → jpg canonical, output ext .jpg.
	out, ext, ok = r.OutputFor(".jpeg")
	if !ok {
		t.Fatal("OutputFor(.jpeg) ok=false")
	}
	if ext != ".jpg" {
		t.Errorf("OutputFor(.jpeg) ext = %q, want .jpg", ext)
	}
	if out.Ext != ".jpg" {
		t.Errorf("OutputFor(.jpeg) returned wrong format: %+v", out)
	}

	// .png → png handler, output ext .png.
	out, ext, ok = r.OutputFor(".PNG")
	if !ok {
		t.Fatal("OutputFor(.PNG) ok=false")
	}
	if ext != ".png" {
		t.Errorf("OutputFor(.PNG) ext = %q, want .png", ext)
	}
	if out.Ext != ".png" {
		t.Errorf("OutputFor(.PNG) returned wrong format: %+v", out)
	}

	// Unknown extension.
	if _, _, ok := r.OutputFor(".gif"); ok {
		t.Error("OutputFor(.gif) ok=true, want false")
	}
}

func TestJPEG_RoundTrip(t *testing.T) {
	r, err := NewRegistry(90)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	f, ok := r.ByExt(".jpg")
	if !ok {
		t.Fatal("ByExt(.jpg) ok=false")
	}
	src := makeImage()

	var buf bytes.Buffer
	if err := f.Encode(&buf, src); err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if buf.Len() == 0 {
		t.Fatal("Encode produced 0 bytes")
	}

	got, err := f.Decode(&buf)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if got.Bounds() != src.Bounds() {
		t.Errorf("Decoded bounds = %v, want %v", got.Bounds(), src.Bounds())
	}
	// JPEG is lossy, so we don't check pixel-exact equality. We do confirm
	// the result is non-nil and has matching dimensions.
}

func TestPNG_RoundTrip_PixelPerfect(t *testing.T) {
	r, err := NewRegistry(90)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	f, ok := r.ByExt(".png")
	if !ok {
		t.Fatal("ByExt(.png) ok=false")
	}
	src := makeImage()

	var buf bytes.Buffer
	if err := f.Encode(&buf, src); err != nil {
		t.Fatalf("Encode: %v", err)
	}

	got, err := f.Decode(&buf)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if got.Bounds() != src.Bounds() {
		t.Fatalf("Decoded bounds = %v, want %v", got.Bounds(), src.Bounds())
	}
	for y := src.Bounds().Min.Y; y < src.Bounds().Max.Y; y++ {
		for x := src.Bounds().Min.X; x < src.Bounds().Max.X; x++ {
			gr, gg, gb, ga := got.At(x, y).RGBA()
			sr, sg, sb, sa := src.At(x, y).RGBA()
			if gr != sr || gg != sg || gb != sb || ga != sa {
				t.Fatalf("pixel (%d,%d) mismatch: got (%d,%d,%d,%d), want (%d,%d,%d,%d)",
					x, y, gr, gg, gb, ga, sr, sg, sb, sa)
			}
		}
	}
}

func TestWebP_DecodeAndEncodeError(t *testing.T) {
	r, err := NewRegistry(90)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	f, ok := r.ByExt(".webp")
	if !ok {
		t.Fatal("ByExt(.webp) ok=false")
	}
	if f.Decode == nil {
		t.Fatal("WebP Decode is nil")
	}
	if f.Encode == nil {
		t.Fatal("WebP Encode is nil; design says it must be non-nil and return a sentinel error")
	}
	if f.OutputExt != ".png" {
		t.Errorf("WebP OutputExt = %q, want .png", f.OutputExt)
	}

	// Decode round-trip from an embedded minimal-valid VP8L WebP.
	raw, err := hex.DecodeString(minimalWebPHex)
	if err != nil {
		t.Fatalf("hex.DecodeString: %v", err)
	}
	img, err := f.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("WebP Decode: %v", err)
	}
	if img == nil {
		t.Fatal("WebP Decode returned nil image")
	}
	b := img.Bounds()
	if b.Dx() != 4 || b.Dy() != 4 {
		t.Errorf("WebP Decode bounds = %v, want 4x4", b)
	}

	// Encode must return the documented sentinel error.
	var buf bytes.Buffer
	encErr := f.Encode(&buf, makeImage())
	if encErr == nil {
		t.Fatal("WebP Encode returned nil; want sentinel error")
	}
	if !errors.Is(encErr, ErrWebPEncodeUnsupported) {
		t.Errorf("WebP Encode err = %v, want ErrWebPEncodeUnsupported", encErr)
	}
	if buf.Len() != 0 {
		t.Errorf("WebP Encode wrote %d bytes; expected 0 on error", buf.Len())
	}
}
