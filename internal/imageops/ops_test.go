package imageops

import (
	"image"
	"image/color"
	"testing"
)

// --- helpers ----------------------------------------------------------------

// makeTestImage returns a w×h *image.RGBA where each pixel encodes its (x,y)
// position into the R and G channels (B and A are constant). This makes
// post-transform pixel locations trivially verifiable.
func makeTestImage(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 0x42, A: 0xff})
		}
	}
	return img
}

// assertImageEqual fails the test unless want and got have identical
// dimensions and identical pixel values via At() across the entire image.
func assertImageEqual(t *testing.T, want, got image.Image) {
	t.Helper()
	wb := want.Bounds()
	gb := got.Bounds()
	if wb.Dx() != gb.Dx() || wb.Dy() != gb.Dy() {
		t.Fatalf("dimension mismatch: want %dx%d, got %dx%d",
			wb.Dx(), wb.Dy(), gb.Dx(), gb.Dy())
	}
	for y := 0; y < wb.Dy(); y++ {
		for x := 0; x < wb.Dx(); x++ {
			wr, wg, wbb, wa := want.At(wb.Min.X+x, wb.Min.Y+y).RGBA()
			gr, gg, gbb, ga := got.At(gb.Min.X+x, gb.Min.Y+y).RGBA()
			if wr != gr || wg != gg || wbb != gbb || wa != ga {
				t.Fatalf("pixel mismatch at (%d,%d): want (%d,%d,%d,%d), got (%d,%d,%d,%d)",
					x, y, wr, wg, wbb, wa, gr, gg, gbb, ga)
			}
		}
	}
}

func sameColor(a, b color.Color) bool {
	ar, ag, ab, aa := a.RGBA()
	br, bg, bb, ba := b.RGBA()
	return ar == br && ag == bg && ab == bb && aa == ba
}

// --- Name() tests -----------------------------------------------------------

func TestNames(t *testing.T) {
	cases := []struct {
		op   Operation
		want string
	}{
		{Crop{}, "crop"},
		{RotateCW90{}, "rotate-cw"},
		{RotateCCW90{}, "rotate-ccw"},
		{Rotate180{}, "rotate-180"},
		{FlipHorizontal{}, "flip-h"},
		{FlipVertical{}, "flip-v"},
	}
	for _, c := range cases {
		if got := c.op.Name(); got != c.want {
			t.Errorf("Name() = %q, want %q", got, c.want)
		}
	}
}

// --- Crop tests -------------------------------------------------------------

func TestCropHappyPath(t *testing.T) {
	src := makeTestImage(10, 10)
	out, err := Crop{X: 2, Y: 3, W: 4, H: 4}.Apply(src)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Bounds().Dx() != 4 || out.Bounds().Dy() != 4 {
		t.Fatalf("wanted 4x4, got %dx%d", out.Bounds().Dx(), out.Bounds().Dy())
	}
	// Result (0,0) should equal source (2,3).
	want := src.At(2, 3)
	got := out.At(0, 0)
	if !sameColor(want, got) {
		t.Fatalf("crop origin mismatch: want %v, got %v", want, got)
	}
	// And (3,3) of result should equal (5,6) of source.
	if !sameColor(src.At(5, 6), out.At(3, 3)) {
		t.Fatalf("crop far corner mismatch")
	}
}

func TestCropBoundsErrors(t *testing.T) {
	src := makeTestImage(10, 10)
	cases := []struct {
		name string
		c    Crop
	}{
		{"negativeX", Crop{X: -1, Y: 0, W: 4, H: 4}},
		{"negativeY", Crop{X: 0, Y: -1, W: 4, H: 4}},
		{"zeroW", Crop{X: 0, Y: 0, W: 0, H: 4}},
		{"zeroH", Crop{X: 0, Y: 0, W: 4, H: 0}},
		{"negativeW", Crop{X: 0, Y: 0, W: -1, H: 4}},
		{"negativeH", Crop{X: 0, Y: 0, W: 4, H: -1}},
		{"xPlusWBeyond", Crop{X: 8, Y: 0, W: 4, H: 4}},
		{"yPlusHBeyond", Crop{X: 0, Y: 8, W: 4, H: 4}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := c.c.Apply(src); err == nil {
				t.Fatalf("expected error for %+v, got nil", c.c)
			}
		})
	}
}

func TestCropResultIsIndependent(t *testing.T) {
	// A fresh allocation (not a SubImage view) must not share storage with
	// the source — mutating the result must not change the source.
	src := makeTestImage(10, 10)
	origAtSrc := src.At(3, 4)

	out, err := Crop{X: 2, Y: 3, W: 4, H: 4}.Apply(src)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	rgba, ok := out.(*image.RGBA)
	if !ok {
		t.Fatalf("expected *image.RGBA result, got %T", out)
	}
	rgba.Set(1, 1, color.RGBA{R: 0xee, G: 0xee, B: 0xee, A: 0xff})

	if !sameColor(origAtSrc, src.At(3, 4)) {
		t.Fatalf("mutating crop result mutated source — pixels are shared")
	}
}

// --- Rotate primitive (CW90) -----------------------------------------------

func TestRotateCW90PixelMapping(t *testing.T) {
	// A known 2×3 input. After CW90 the output should be 3×2, with
	// src(x,y) mapping to dst(h-1-y, x) where h=3.
	src := makeTestImage(2, 3)
	got, err := RotateCW90{}.Apply(src)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Bounds().Dx() != 3 || got.Bounds().Dy() != 2 {
		t.Fatalf("wanted 3x2, got %dx%d", got.Bounds().Dx(), got.Bounds().Dy())
	}
	w, h := 2, 3
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			want := src.At(x, y)
			have := got.At(h-1-y, x)
			if !sameColor(want, have) {
				t.Fatalf("CW90 mapping: src(%d,%d) -> dst(%d,%d): want %v, got %v",
					x, y, h-1-y, x, want, have)
			}
		}
	}
}

// --- Identities -------------------------------------------------------------

func TestFourCW90sIsIdentity(t *testing.T) {
	src := makeTestImage(5, 7)
	cur := image.Image(src)
	for i := 0; i < 4; i++ {
		out, err := RotateCW90{}.Apply(cur)
		if err != nil {
			t.Fatalf("unexpected error on iter %d: %v", i, err)
		}
		cur = out
	}
	assertImageEqual(t, src, cur)
}

func TestFourCCW90sIsIdentity(t *testing.T) {
	src := makeTestImage(5, 7)
	cur := image.Image(src)
	for i := 0; i < 4; i++ {
		out, err := RotateCCW90{}.Apply(cur)
		if err != nil {
			t.Fatalf("unexpected error on iter %d: %v", i, err)
		}
		cur = out
	}
	assertImageEqual(t, src, cur)
}

func TestCCW90ThenCW90IsIdentity(t *testing.T) {
	src := makeTestImage(5, 7)
	mid, err := RotateCCW90{}.Apply(src)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out, err := RotateCW90{}.Apply(mid)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertImageEqual(t, src, out)
}

func TestFlipHorizontalIdentity(t *testing.T) {
	src := makeTestImage(5, 7)
	mid, err := FlipHorizontal{}.Apply(src)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out, err := FlipHorizontal{}.Apply(mid)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertImageEqual(t, src, out)
}

func TestFlipVerticalIdentity(t *testing.T) {
	src := makeTestImage(5, 7)
	mid, err := FlipVertical{}.Apply(src)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out, err := FlipVertical{}.Apply(mid)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertImageEqual(t, src, out)
}

// --- Composition equivalences ----------------------------------------------

func TestRotate180EqualsFlipHThenFlipV(t *testing.T) {
	src := makeTestImage(6, 4)
	want, err := Rotate180{}.Apply(src)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	mid, err := FlipVertical{}.Apply(src)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, err := FlipHorizontal{}.Apply(mid)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertImageEqual(t, want, got)

	// Spot-check several pixels independently of the helper.
	checkpoints := []image.Point{{0, 0}, {5, 3}, {3, 2}, {1, 0}, {0, 3}}
	for _, p := range checkpoints {
		if !sameColor(want.At(p.X, p.Y), got.At(p.X, p.Y)) {
			t.Fatalf("composition mismatch at (%d,%d)", p.X, p.Y)
		}
	}
}

func TestRotate180Dimensions(t *testing.T) {
	src := makeTestImage(6, 4)
	out, err := Rotate180{}.Apply(src)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Bounds().Dx() != 6 || out.Bounds().Dy() != 4 {
		t.Fatalf("wanted 6x4, got %dx%d", out.Bounds().Dx(), out.Bounds().Dy())
	}
	// Rotate180: src(x,y) maps to dst(w-1-x, h-1-y).
	w, h := 6, 4
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if !sameColor(src.At(x, y), out.At(w-1-x, h-1-y)) {
				t.Fatalf("Rotate180 mapping wrong at (%d,%d)", x, y)
			}
		}
	}
}

// --- Non-RGBA input --------------------------------------------------------

func TestOperationsAcceptGrayInput(t *testing.T) {
	// Operations must work on any image.Image — exercise with a *image.Gray.
	gray := image.NewGray(image.Rect(0, 0, 4, 5))
	for y := 0; y < 5; y++ {
		for x := 0; x < 4; x++ {
			gray.Set(x, y, color.Gray{Y: uint8(x*10 + y)})
		}
	}
	ops := []Operation{
		Crop{X: 0, Y: 0, W: 2, H: 2},
		RotateCW90{}, RotateCCW90{}, Rotate180{},
		FlipHorizontal{}, FlipVertical{},
	}
	for _, op := range ops {
		out, err := op.Apply(gray)
		if err != nil {
			t.Fatalf("%s on gray input: unexpected error: %v", op.Name(), err)
		}
		if _, ok := out.(*image.RGBA); !ok {
			t.Fatalf("%s: output should be *image.RGBA, got %T", op.Name(), out)
		}
	}
}

// --- Non-zero-origin bounds -------------------------------------------------

func TestOperationsHandleNonZeroBoundsOrigin(t *testing.T) {
	// An image whose bounds do not start at (0,0) must still be transformed
	// correctly — operations must read pixels relative to bounds.Min.
	src := image.NewRGBA(image.Rect(10, 20, 14, 23)) // 4x3
	for y := 20; y < 23; y++ {
		for x := 10; x < 14; x++ {
			src.Set(x, y, color.RGBA{R: uint8(x - 10), G: uint8(y - 20), B: 0x42, A: 0xff})
		}
	}
	out, err := RotateCW90{}.Apply(src)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Output dims should be 3x4 (h x w).
	if out.Bounds().Dx() != 3 || out.Bounds().Dy() != 4 {
		t.Fatalf("wanted 3x4, got %dx%d", out.Bounds().Dx(), out.Bounds().Dy())
	}
	// src-relative (x,y)=(0,0) → dst(h-1-y, x) = dst(2, 0).
	want := src.At(10, 20)
	if !sameColor(want, out.At(2, 0)) {
		t.Fatalf("non-zero-origin mapping incorrect")
	}
}
