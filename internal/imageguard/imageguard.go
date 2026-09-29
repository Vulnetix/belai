// Package imageguard admits an image the harness captured. The security
// classifier reads text, so an image never goes through it. Instead an image
// is admitted only by deterministic checks: the header must decode as PNG or
// JPEG within a pixel budget, the pixels are decoded, bounded to a long-edge
// ceiling, and re-encoded as a fresh PNG. Anything the decoder does not
// understand (trailing bytes, metadata chunks, a polyglot file, an animated
// or exotic format) does not survive the re-encode or is refused outright.
// The result carries pixels and nothing else.
//
// Only harness-owned capture tools (Screenshot) may hand an image to this
// package. It is not a general way to admit an image a model or a tool
// fetched from elsewhere.
package imageguard

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
)

// Limits bounds an admitted image.
type Limits struct {
	// MaxBytes caps the input and the re-encoded output.
	MaxBytes int
	// MaxPixels caps width times height before any pixel is decoded, so a
	// small file that declares a huge canvas is refused without allocating it.
	MaxPixels int
	// MaxEdge is the long-edge ceiling. A larger image is scaled down to it.
	MaxEdge int
}

// Default is the budget for a screenshot: what vision models downscale to
// anyway, so nothing is sent that would be thrown away.
var Default = Limits{MaxBytes: 32 << 20, MaxPixels: 50_000_000, MaxEdge: 1568}

// MediaType is the media type of every admitted image.
const MediaType = "image/png"

// Image is an admitted image.
type Image struct {
	// PNG is the re-encoded image.
	PNG    []byte
	Width  int
	Height int
	// Scaled reports that the image was reduced to fit MaxEdge.
	Scaled bool
}

// Admit decodes data and returns a fresh, bounded PNG. Any failure refuses
// the image; there is no partial admission.
func Admit(data []byte, lim Limits) (Image, error) {
	if lim.MaxBytes <= 0 || lim.MaxPixels <= 0 || lim.MaxEdge <= 0 {
		return Image{}, errors.New("imageguard: limits are not set")
	}
	if len(data) == 0 {
		return Image{}, errors.New("empty image")
	}
	if len(data) > lim.MaxBytes {
		return Image{}, fmt.Errorf("image is %d bytes, over the %d byte limit", len(data), lim.MaxBytes)
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return Image{}, fmt.Errorf("not a decodable image: %w", err)
	}
	if format != "png" && format != "jpeg" {
		return Image{}, fmt.Errorf("image format %q is not accepted", format)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return Image{}, errors.New("image has no pixels")
	}
	// Compare with division so the product cannot overflow.
	if cfg.Width > lim.MaxPixels/cfg.Height {
		return Image{}, fmt.Errorf("image is %dx%d, over the %d pixel limit", cfg.Width, cfg.Height, lim.MaxPixels)
	}

	var src image.Image
	switch format {
	case "png":
		src, err = png.Decode(bytes.NewReader(data))
	default:
		src, err = jpeg.Decode(bytes.NewReader(data))
	}
	if err != nil {
		return Image{}, fmt.Errorf("image does not decode: %w", err)
	}

	out := Image{Width: cfg.Width, Height: cfg.Height}
	if long := max(cfg.Width, cfg.Height); long > lim.MaxEdge {
		w, h := scaledSize(cfg.Width, cfg.Height, lim.MaxEdge)
		src = boxScale(src, w, h)
		out.Width, out.Height, out.Scaled = w, h, true
	}

	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestSpeed}
	if err := enc.Encode(&buf, flatten(src)); err != nil {
		return Image{}, fmt.Errorf("image does not encode: %w", err)
	}
	if buf.Len() > lim.MaxBytes {
		return Image{}, fmt.Errorf("re-encoded image is %d bytes, over the %d byte limit", buf.Len(), lim.MaxBytes)
	}
	out.PNG = buf.Bytes()
	return out, nil
}

// scaledSize fits (w, h) to a long edge of edge, keeping the aspect ratio and
// never returning a zero side.
func scaledSize(w, h, edge int) (int, int) {
	if w >= h {
		return edge, max(1, h*edge/w)
	}
	return max(1, w*edge/h), edge
}

// flatten draws src onto an opaque white canvas, so transparency cannot hide
// content and every admitted image has the same pixel format.
func flatten(src image.Image) *image.RGBA {
	b := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(dst, dst.Bounds(), &image.Uniform{C: color.White}, image.Point{}, draw.Src)
	draw.Draw(dst, dst.Bounds(), src, b.Min, draw.Over)
	return dst
}

// boxScale reduces src to w by h by averaging the source pixels each
// destination pixel covers. Averaging keeps small text legible where
// dropping pixels would not.
func boxScale(src image.Image, w, h int) *image.RGBA {
	sb := src.Bounds()
	sw, sh := sb.Dx(), sb.Dy()
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		y0, y1 := y*sh/h, max((y+1)*sh/h, y*sh/h+1)
		for x := 0; x < w; x++ {
			x0, x1 := x*sw/w, max((x+1)*sw/w, x*sw/w+1)
			var r, g, b, a, n uint64
			for sy := y0; sy < y1; sy++ {
				for sx := x0; sx < x1; sx++ {
					pr, pg, pb, pa := src.At(sb.Min.X+sx, sb.Min.Y+sy).RGBA()
					r, g, b, a = r+uint64(pr), g+uint64(pg), b+uint64(pb), a+uint64(pa)
					n++
				}
			}
			dst.Set(x, y, color.RGBA64{R: uint16(r / n), G: uint16(g / n), B: uint16(b / n), A: uint16(a / n)})
		}
	}
	return dst
}
