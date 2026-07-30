// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package imagenorm

// SVG is the one source format that arrives as a DOCUMENT rather than pixels,
// and a modern site increasingly ships its mark as nothing else. It is
// rasterized here on the way in, never stored or served as markup: an SVG can
// carry script, and a scripted document served back from this deployment's own
// origin is a hole a favicon has no business opening. Rasterizing closes it by
// construction — what comes out the other side is pixels this package drew.

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"image"
	"io"
	"strings"

	"github.com/srwiley/oksvg"
	"github.com/srwiley/rasterx"
)

// svgRasterEdge is the square a vector mark is drawn into. It sits above the
// stored ceiling so SquarePNG's own downscale does the final resample — a
// vector has no native resolution to preserve, so drawing it small and
// enlarging later would throw away detail nothing can recover.
const svgRasterEdge = 512

// looksLikeSVG reports whether the bytes are an SVG document. Content types
// are not consulted: a server mislabels an asset often enough that the bytes
// are the only honest answer. The scan walks XML tokens rather than searching
// for "<svg" as text, so an XML comment or a stray mention in a JSON blob
// cannot pass for a document.
//
// It reads only as far as the first element, so its cost is set by where that
// element sits and not by the document's size — a byte cap here would only
// misreport a large vector as an undecodable raster.
func looksLikeSVG(src []byte) bool {
	if len(src) == 0 {
		return false
	}
	decoder := xml.NewDecoder(bytes.NewReader(src))
	// A favicon SVG routinely carries entities the strict decoder rejects, and
	// this pass only has to find the root element's name.
	decoder.Strict = false
	for {
		token, err := decoder.Token()
		if err != nil {
			return false // EOF or malformed before any element: not a document
		}
		if start, ok := token.(xml.StartElement); ok {
			return strings.EqualFold(start.Name.Local, "svg")
		}
	}
}

// svgMaxElements bounds how much document the rasterizer is asked to draw. A
// company's mark is tens of shapes; a file with tens of thousands is not a
// logo, and refusing it keeps one hostile favicon from occupying a deep-read
// worker for its whole timeout.
const svgMaxElements = 20_000

// rasterizeSVG draws a vector mark into pixels, fitting it inside an
// svgRasterEdge square with its aspect preserved. A document oksvg cannot
// parse — an exotic filter, a feature it owns no renderer for — is
// ErrUnsupported like any other undecodable candidate, and the caller moves on
// to the next one.
func rasterizeSVG(src []byte) (image.Image, error) {
	if err := refuseUnsafeSVG(src); err != nil {
		return nil, err
	}
	icon, err := oksvg.ReadIconStream(bytes.NewReader(src), oksvg.WarnErrorMode)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnsupported, err)
	}
	width, height := icon.ViewBox.W, icon.ViewBox.H
	if width <= 0 || height <= 0 {
		return nil, fmt.Errorf("%w: the SVG declares a %gx%g viewBox", ErrUnsupported, width, height)
	}

	// Fit inside the square: the longest side becomes the edge, the other
	// scales with it, so the drawing keeps the shape its author gave it.
	scale := float64(svgRasterEdge) / max(width, height)
	target := image.Rect(0, 0, max(1, int(width*scale+0.5)), max(1, int(height*scale+0.5)))
	// The transform is set here rather than through icon.SetTarget, which
	// translates by the viewBox origin WITHOUT scaling that translation — so a
	// document whose viewBox does not start at 0,0 (a mark centred on its own
	// origin, say) draws shifted off the canvas by the unscaled remainder.
	// Shifting first and scaling the whole thing puts every viewBox origin in
	// the same place: the top-left corner of the raster.
	icon.Transform = rasterx.Identity.Scale(scale, scale).Translate(-icon.ViewBox.X, -icon.ViewBox.Y)

	// The canvas starts fully transparent, so a mark drawn without its own
	// background keeps one — the render layer supplies the backdrop.
	canvas := image.NewNRGBA(target)
	scanner := rasterx.NewScannerGV(target.Dx(), target.Dy(), canvas, target)
	icon.Draw(rasterx.NewDasher(target.Dx(), target.Dy(), scanner), 1)
	return canvas, nil
}

// refuseUnsafeSVG rejects the documents the renderer cannot be trusted with,
// BEFORE it sees them.
//
// The renderer expands <use> by re-dispatching the referenced element through
// the same handler table, with no depth limit and no self-reference check: the
// 130 bytes
//
//	<defs><g id="a"><use href="#a"/></g></defs><use href="#a"/>
//
// exhaust the goroutine stack, and a stack overflow in Go is a FATAL error —
// no recover, no job-timeout, the process dies. Nesting <use> instead of
// recursing gets the same worker stuck exponentially. So <use> is refused
// outright rather than analysed: a company's mark is flat shapes, references
// buy a favicon nothing, and a rule with no graph to reason about has no
// subtle case to get wrong. A refused document is simply an unusable
// candidate — the chain moves on and the record keeps its monogram.
func refuseUnsafeSVG(src []byte) error {
	decoder := xml.NewDecoder(bytes.NewReader(src))
	decoder.Strict = false
	for elements := 0; ; elements++ {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("%w: the SVG is not readable as XML: %w", ErrUnsupported, err)
		}
		if elements > svgMaxElements {
			return fmt.Errorf("%w: the SVG carries more than %d elements", ErrUnsupported, svgMaxElements)
		}
		if start, ok := token.(xml.StartElement); ok && strings.EqualFold(start.Name.Local, "use") {
			return fmt.Errorf("%w: the SVG references other elements with <use>", ErrUnsupported)
		}
	}
}
