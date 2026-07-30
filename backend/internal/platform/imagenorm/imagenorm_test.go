// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package imagenorm

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"
)

// solidPNG encodes a width x height PNG filled with one colour.
func solidPNG(t *testing.T, width, height int, fill color.NRGBA) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := range height {
		for x := range width {
			img.SetNRGBA(x, y, fill)
		}
	}
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		t.Fatalf("encoding the fixture PNG: %v", err)
	}
	return out.Bytes()
}

func TestDecodeReadsThePixelFormatsASitePublishesAMarkIn(t *testing.T) {
	opaque := color.NRGBA{R: 10, G: 120, B: 200, A: 255}

	pngBytes := solidPNG(t, 64, 48, opaque)
	img, err := Decode(pngBytes)
	if err != nil {
		t.Fatalf("decoding a PNG: %v", err)
	}
	if got := img.Bounds(); got.Dx() != 64 || got.Dy() != 48 {
		t.Fatalf("PNG decoded to %v, want 64x48", got)
	}

	var jpegBytes bytes.Buffer
	if err := jpeg.Encode(&jpegBytes, img, nil); err != nil {
		t.Fatalf("encoding the fixture JPEG: %v", err)
	}
	if _, err := Decode(jpegBytes.Bytes()); err != nil {
		t.Fatalf("decoding a JPEG: %v", err)
	}
}

func TestDecodeRasterizesAVectorMarkIntoPixels(t *testing.T) {
	// A flat two-colour mark, the shape a favicon SVG actually takes.
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 100 50">` +
		`<rect x="0" y="0" width="100" height="50" fill="#ff6b00"/></svg>`)
	img, err := Decode(svg)
	if err != nil {
		t.Fatalf("rasterizing an SVG: %v", err)
	}
	bounds := img.Bounds()
	if bounds.Dx() != svgRasterEdge || bounds.Dy() != svgRasterEdge/2 {
		t.Fatalf("rasterized to %v, want the viewBox aspect fitted to %d", bounds, svgRasterEdge)
	}
	if _, _, _, alpha := img.At(bounds.Dx()/2, bounds.Dy()/2).RGBA(); alpha == 0 {
		t.Fatal("the rasterized mark is blank")
	}
}

func TestDecodeDrawsAViewBoxThatDoesNotStartAtTheOrigin(t *testing.T) {
	// A mark whose viewBox is offset — a real shape, and the case a naive
	// transform draws shifted off the canvas. The square fills its whole
	// viewBox, so every corner of the raster must be painted.
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="-5 -5 10 10">` +
		`<rect x="-5" y="-5" width="10" height="10" fill="#000"/></svg>`)
	img, err := Decode(svg)
	if err != nil {
		t.Fatalf("rasterizing an offset viewBox: %v", err)
	}
	bounds := img.Bounds()
	for _, p := range []image.Point{
		{X: 2, Y: 2},
		{X: bounds.Dx() - 3, Y: 2},
		{X: 2, Y: bounds.Dy() - 3},
		{X: bounds.Dx() - 3, Y: bounds.Dy() - 3},
	} {
		if _, _, _, alpha := img.At(p.X, p.Y).RGBA(); alpha == 0 {
			t.Fatalf("corner %v is unpainted — the viewBox offset shifted the drawing off the canvas", p)
		}
	}
}

func TestDecodeRasterizesAScriptedSVGRatherThanKeepingItsMarkup(t *testing.T) {
	// The security property this whole package rests on: a document that
	// carries script comes out as pixels, so nothing a caller serves back can
	// still execute. What must NOT happen is the markup surviving.
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10">` +
		`<script>alert(1)</script><circle cx="5" cy="5" r="5" fill="#000"/></svg>`)
	img, err := Decode(svg)
	if err != nil {
		t.Fatalf("a scripted SVG must still rasterize: %v", err)
	}
	out, err := SquarePNG(img, 64)
	if err != nil {
		t.Fatalf("SquarePNG: %v", err)
	}
	if bytes.Contains(out, []byte("script")) || bytes.Contains(out, []byte("alert")) {
		t.Fatal("the SVG's markup survived into the stored bytes")
	}
	if _, err := png.Decode(bytes.NewReader(out)); err != nil {
		t.Fatalf("the stored bytes are not a PNG: %v", err)
	}
}

func TestDecodeRefusesWhatIsNotAPictureItCanRender(t *testing.T) {
	if _, err := Decode([]byte("<!doctype html><title>404</title>")); !errors.Is(err, ErrUnsupported) {
		t.Fatal("an HTML error page served as an image must be unsupported")
	}
	// An HTML page whose body mentions <svg> is not an SVG document: the sniff
	// reads the ROOT element, so a page like this must not reach the rasterizer.
	if _, err := Decode([]byte(`<!doctype html><body><p>use &lt;svg&gt;</p></body>`)); !errors.Is(err, ErrUnsupported) {
		t.Fatal("an HTML page mentioning svg must not pass as an SVG document")
	}
	if _, err := Decode(nil); !errors.Is(err, ErrUnsupported) {
		t.Fatal("empty bytes must be unsupported")
	}
	truncated := solidPNG(t, 32, 32, color.NRGBA{A: 255})
	if _, err := Decode(truncated[:len(truncated)/2]); !errors.Is(err, ErrUnsupported) {
		t.Fatal("a truncated PNG must be unsupported")
	}
}

func TestDecodeRefusesAHeaderClaimingMorePixelsThanItWillAllocate(t *testing.T) {
	// A real 1x1 PNG, with its IHDR width and height overwritten to claim a
	// 40000x40000 canvas: bytes a decoder would answer with 6.4 GB of image.
	// The CRC no longer matches, but the size gate must refuse it before any
	// decoder gets far enough to care.
	bomb := solidPNG(t, 1, 1, color.NRGBA{A: 255})
	ihdr := bytes.Index(bomb, []byte("IHDR"))
	if ihdr < 0 {
		t.Fatal("the fixture PNG has no IHDR chunk")
	}
	binary.BigEndian.PutUint32(bomb[ihdr+4:], 40000)
	binary.BigEndian.PutUint32(bomb[ihdr+8:], 40000)

	_, err := Decode(bomb)
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("an oversized declared canvas must be refused, got %v", err)
	}
}

func TestSquarePNGPadsToASquareWithoutStretchingTheMark(t *testing.T) {
	// A wide source: the mark must keep its 4:1 shape inside a square canvas,
	// which means transparent bands above and below it.
	img, err := Decode(solidPNG(t, 200, 50, color.NRGBA{R: 255, A: 255}))
	if err != nil {
		t.Fatalf("decoding the fixture: %v", err)
	}
	out, err := SquarePNG(img, 100)
	if err != nil {
		t.Fatalf("SquarePNG: %v", err)
	}
	square, err := png.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("the output is not a PNG: %v", err)
	}
	bounds := square.Bounds()
	if bounds.Dx() != 100 || bounds.Dy() != 100 {
		t.Fatalf("output is %v, want a 100x100 square", bounds)
	}
	// The centre row is inside the scaled mark; the top row is padding.
	if _, _, _, alpha := square.At(50, 50).RGBA(); alpha == 0 {
		t.Fatal("the centre of the square is transparent — the mark did not land")
	}
	if _, _, _, alpha := square.At(50, 1).RGBA(); alpha != 0 {
		t.Fatal("the top band is painted — the mark was stretched instead of padded")
	}
}

func TestSquarePNGNeverInventsResolutionASourceDoesNotHave(t *testing.T) {
	img, err := Decode(solidPNG(t, 48, 48, color.NRGBA{B: 255, A: 255}))
	if err != nil {
		t.Fatalf("decoding the fixture: %v", err)
	}
	out, err := SquarePNG(img, 300)
	if err != nil {
		t.Fatalf("SquarePNG: %v", err)
	}
	square, err := png.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("the output is not a PNG: %v", err)
	}
	if got := square.Bounds().Dx(); got != 48 {
		t.Fatalf("a 48px source was stored at %dpx; it must stay 48", got)
	}
}

func TestSquarePNGPreservesTheSourcesTransparency(t *testing.T) {
	// A logo on a transparent background must not come out flattened: the
	// render layer supplies the backdrop, and a baked-in white one would
	// look wrong on the dark theme.
	img := image.NewNRGBA(image.Rect(0, 0, 64, 64))
	for y := range 64 {
		for x := 32; x < 64; x++ {
			img.SetNRGBA(x, y, color.NRGBA{R: 255, G: 255, B: 255, A: 255})
		}
	}
	out, err := SquarePNG(img, 64)
	if err != nil {
		t.Fatalf("SquarePNG: %v", err)
	}
	square, err := png.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("the output is not a PNG: %v", err)
	}
	if _, _, _, alpha := square.At(4, 32).RGBA(); alpha != 0 {
		t.Fatal("the source's transparent half came out opaque")
	}
}

// icoWith builds a one-frame ICO container around the given payload.
func icoWith(payload []byte, width, height byte) []byte {
	var out bytes.Buffer
	header := make([]byte, icoHeaderSize)
	binary.LittleEndian.PutUint16(header[2:4], icoTypeIcon)
	binary.LittleEndian.PutUint16(header[4:6], 1)
	out.Write(header)
	entry := make([]byte, icoEntrySize)
	entry[0], entry[1] = width, height
	binary.LittleEndian.PutUint32(entry[8:12], uint32(len(payload)))
	binary.LittleEndian.PutUint32(entry[12:16], uint32(icoHeaderSize+icoEntrySize))
	out.Write(entry)
	out.Write(payload)
	return out.Bytes()
}

func TestDecodeUnwrapsAFaviconIcoAroundAPNGFrame(t *testing.T) {
	inner := solidPNG(t, 64, 64, color.NRGBA{G: 200, A: 255})
	img, err := Decode(icoWith(inner, 64, 64))
	if err != nil {
		t.Fatalf("decoding an ICO with a PNG frame: %v", err)
	}
	if got := img.Bounds().Dx(); got != 64 {
		t.Fatalf("the unwrapped frame is %dpx wide, want 64", got)
	}
}

func TestDecodeRefusesABitmapOnlyIcoRatherThanGuessingAtItsFrames(t *testing.T) {
	// A frame that is not a PNG: the legacy bitmap shape, which this package
	// deliberately owns no decoder for.
	bitmapFrame := bytes.Repeat([]byte{0x28, 0x00, 0x00, 0x00}, 16)
	_, err := Decode(icoWith(bitmapFrame, 32, 32))
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("a bitmap-only ICO must be unsupported, got %v", err)
	}
}

func TestICOFrameSelectionTakesTheLargestAndRefusesOneOutsideTheFile(t *testing.T) {
	small := solidPNG(t, 32, 32, color.NRGBA{A: 255})
	large := solidPNG(t, 128, 128, color.NRGBA{A: 255})

	var out bytes.Buffer
	header := make([]byte, icoHeaderSize)
	binary.LittleEndian.PutUint16(header[2:4], icoTypeIcon)
	binary.LittleEndian.PutUint16(header[4:6], 2)
	out.Write(header)
	base := icoHeaderSize + 2*icoEntrySize
	for i, frame := range [][]byte{small, large} {
		entry := make([]byte, icoEntrySize)
		if i == 0 {
			entry[0], entry[1] = 32, 32
			binary.LittleEndian.PutUint32(entry[12:16], uint32(base))
		} else {
			entry[0], entry[1] = 128, 128
			binary.LittleEndian.PutUint32(entry[12:16], uint32(base+len(small)))
		}
		binary.LittleEndian.PutUint32(entry[8:12], uint32(len(frame)))
		out.Write(entry)
	}
	out.Write(small)
	out.Write(large)

	img, err := Decode(out.Bytes())
	if err != nil {
		t.Fatalf("decoding a two-frame ICO: %v", err)
	}
	if got := img.Bounds().Dx(); got != 128 {
		t.Fatalf("frame selection took a %dpx frame, want the 128px one", got)
	}

	// A directory entry whose payload runs past the end of the file must be
	// ignored rather than read out of bounds.
	truncated := out.Bytes()[:base+len(small)]
	if _, err := Decode(truncated); !errors.Is(err, ErrUnsupported) {
		// The surviving 32px frame is intact here, so the honest outcome is
		// that it decodes; what must NOT happen is a panic or the out-of-range
		// frame being read.
		if err != nil {
			t.Fatalf("a truncated ICO must either decode its intact frame or be unsupported, got %v", err)
		}
	}
}
