// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package imagenorm turns an untrusted source image into ONE stored shape: a
// square PNG, the source scaled to fit and centred, transparent where the
// source did not paint. It is technical plumbing — it holds no opinion about
// what the picture is FOR, so the dimension and aspect policy of any caller
// (what counts as a usable company logo, say) stays with that caller, which
// reads the decoded bounds and decides.
//
// Two properties make it safe to point at bytes a third-party website served:
//   - Nothing passes through. Every output is this package's own PNG encoder's
//     work, so markup smuggled inside an image-typed response cannot reach a
//     caller that serves the result back. An SVG is not an exception: it is
//     rasterized on the way in (svg.go), so what leaves is pixels, never the
//     document — and a scripted one has nothing left to script.
//   - Declared dimensions are checked before pixels are allocated, so a
//     kilobyte-sized header claiming 30000x30000 is refused rather than
//     turned into gigabytes of image buffer.
package imagenorm

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	// Registered for image.Decode's format sniffing: GIF and JPEG are the
	// other two shapes a site's mark commonly arrives in.
	_ "image/gif"
	_ "image/jpeg"
	"image/png"

	xdraw "golang.org/x/image/draw"
	// WebP is the one modern image format the stdlib carries no decoder for,
	// and a site that ships its mark as one is not unusual.
	_ "golang.org/x/image/webp"
)

// ContentType is the media type of everything SquarePNG produces. Callers
// store it beside the bytes rather than carrying the source's own type: the
// source format is not what gets served.
const ContentType = "image/png"

// maxSourcePixels bounds a decode. An image header is cheap to forge and
// costs the decoder width*height*4 bytes to believe, so the declared size is
// refused before any of it is allocated. 40 Mpx covers every real logo and
// every plausible og:image with room to spare.
const maxSourcePixels = 40 << 20

// ErrUnsupported reports bytes this package cannot turn into a picture: a
// format it carries no decoder for (SVG, a bitmap-only ICO), a truncated
// body, or something that was never an image. It is a property of the input,
// not a fault — callers treat it as "this candidate is unusable" and move on.
var ErrUnsupported = errors.New("imagenorm: unsupported or undecodable image")

// Decode decodes an untrusted image. Beyond the stdlib's PNG/JPEG/GIF it
// handles WebP, and it unwraps the ICO container by extracting the largest
// PNG frame inside it — a legacy ICO carrying only bitmap frames is
// ErrUnsupported, because those frames' stride and transparency-mask rules
// are a decoder this package deliberately does not own.
func Decode(src []byte) (image.Image, error) {
	if looksLikeSVG(src) {
		// A vector mark has no pixels of its own, so it is drawn here rather
		// than decoded — and never kept as the document it arrived as.
		return rasterizeSVG(src)
	}
	if frame, isICO := icoPNGFrame(src); isICO {
		if frame == nil {
			return nil, fmt.Errorf("%w: the ICO carries no PNG frame", ErrUnsupported)
		}
		src = frame
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(src))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnsupported, err)
	}
	if config.Width <= 0 || config.Height <= 0 {
		return nil, fmt.Errorf("%w: it declares a %dx%d canvas", ErrUnsupported, config.Width, config.Height)
	}
	if int64(config.Width)*int64(config.Height) > maxSourcePixels {
		return nil, fmt.Errorf("%w: %dx%d exceeds the %d-pixel decode cap",
			ErrUnsupported, config.Width, config.Height, maxSourcePixels)
	}
	img, _, err := image.Decode(bytes.NewReader(src))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnsupported, err)
	}
	return img, nil
}

// SquarePNG re-encodes img as a transparent-background PNG square. The square's
// edge is the source's longest side, capped at maxEdge: the source is scaled
// down to fit and centred, keeping its aspect (a wide mark letterboxes, it
// never stretches), and it is never scaled UP — inventing resolution a source
// does not have only costs bytes and sharpness, so a 64px favicon is stored as
// a 64px square.
func SquarePNG(img image.Image, maxEdge int) ([]byte, error) {
	if maxEdge <= 0 {
		return nil, fmt.Errorf("imagenorm: a square needs a positive edge, got %d", maxEdge)
	}
	bounds := img.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	if width <= 0 || height <= 0 {
		return nil, fmt.Errorf("%w: it decoded to a %dx%d canvas", ErrUnsupported, width, height)
	}

	edge := max(width, height)
	if edge > maxEdge {
		edge = maxEdge
	}
	scale := float64(edge) / float64(max(width, height))
	// Rounding up keeps a 1px source side from scaling to a 0px destination.
	fitWidth := max(1, int(float64(width)*scale+0.5))
	fitHeight := max(1, int(float64(height)*scale+0.5))

	square := image.NewNRGBA(image.Rect(0, 0, edge, edge))
	target := image.Rect(
		(edge-fitWidth)/2, (edge-fitHeight)/2,
		(edge-fitWidth)/2+fitWidth, (edge-fitHeight)/2+fitHeight,
	)
	// CatmullRom over Src, not Over: the destination starts fully transparent
	// and the scaled source REPLACES it, so a source's own alpha survives
	// instead of being composited onto nothing.
	xdraw.CatmullRom.Scale(square, target, img, bounds, xdraw.Src, nil)

	var out bytes.Buffer
	if err := (&png.Encoder{CompressionLevel: png.BestCompression}).Encode(&out, square); err != nil {
		return nil, fmt.Errorf("imagenorm: encoding the normalized PNG: %w", err)
	}
	return out.Bytes(), nil
}

// ICO container layout (the parts that matter): a 6-byte header — 2 reserved
// zero bytes, a type of 1 for icons, then the frame count — followed by one
// 16-byte directory entry per frame. Each entry declares the frame's width and
// height in a single byte (0 meaning 256), and carries its payload's length
// and offset.
const (
	icoHeaderSize = 6
	icoEntrySize  = 16
	icoTypeIcon   = 1
)

// pngMagic is the 8-byte PNG signature. An ICO frame is either a PNG file
// verbatim or a headerless bitmap, and this is how they are told apart.
var pngMagic = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}

// icoPNGFrame reports whether src is an ICO container and, when it is,
// returns its largest PNG frame (nil when it holds none). Sizing goes by the
// declared pixel area with the payload length as the tie-break, so a file
// whose entries all declare 0x0 still yields its beefiest frame.
func icoPNGFrame(src []byte) (frame []byte, isICO bool) {
	if len(src) < icoHeaderSize {
		return nil, false
	}
	if binary.LittleEndian.Uint16(src[0:2]) != 0 || binary.LittleEndian.Uint16(src[2:4]) != icoTypeIcon {
		return nil, false
	}
	count := int(binary.LittleEndian.Uint16(src[4:6]))
	if count == 0 || len(src) < icoHeaderSize+count*icoEntrySize {
		return nil, true // an ICO header with an unreadable directory is still an ICO
	}

	var best []byte
	bestArea, bestSize := -1, -1
	for i := range count {
		entry := src[icoHeaderSize+i*icoEntrySize:]
		width, height := icoDimension(entry[0]), icoDimension(entry[1])
		size := int(binary.LittleEndian.Uint32(entry[8:12]))
		offset := int(binary.LittleEndian.Uint32(entry[12:16]))
		if size <= 0 || offset < 0 || offset+size > len(src) {
			continue // a frame pointing outside the file is not a frame
		}
		payload := src[offset : offset+size]
		if !bytes.HasPrefix(payload, pngMagic) {
			continue
		}
		if area := width * height; area > bestArea || (area == bestArea && size > bestSize) {
			best, bestArea, bestSize = payload, area, size
		}
	}
	return best, true
}

// icoDimension reads one directory-entry side, where a zero byte is the
// container's way of spelling 256 (the value does not fit in a byte).
func icoDimension(b byte) int {
	if b == 0 {
		return 256
	}
	return int(b)
}
