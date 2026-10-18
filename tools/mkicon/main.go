// mkicon draws the maxmix icon (three intertwined cables) procedurally and
// writes assets/maxmix.ico (16..256 px) plus assets/maxmix.png for preview.
// Standard library only; run with `go run ./tools/mkicon`.
package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
)

var sizes = []int{16, 20, 24, 32, 40, 48, 64, 128, 256}

type cable struct {
	fill    color.NRGBA
	outline color.NRGBA
	phase   float64
}

var cables = []cable{
	{fill: color.NRGBA{0x3A, 0x8D, 0xFF, 0xFF}, outline: color.NRGBA{0x12, 0x3C, 0x7A, 0xFF}, phase: 0},
	{fill: color.NRGBA{0xFF, 0x8A, 0x3D, 0xFF}, outline: color.NRGBA{0x7A, 0x36, 0x10, 0xFF}, phase: 2 * math.Pi / 3},
	{fill: color.NRGBA{0x3D, 0xDC, 0x84, 0xFF}, outline: color.NRGBA{0x12, 0x62, 0x38, 0xFF}, phase: 4 * math.Pi / 3},
}

// render draws the icon at size px, supersampled ss times.
func render(size, ss int) *image.NRGBA {
	W := size * ss
	img := image.NewNRGBA(image.Rect(0, 0, W, W))
	f := float64(W)

	// Cable geometry: y(t) = mid + amp*sin(2*pi*t*turns + phase), t in [0,1].
	thick := math.Max(0.105*f, 2.4*float64(ss)) // core diameter
	edge := math.Max(0.018*f, 1.0*float64(ss))  // outline width
	amp := 0.24 * f
	turns := 1.0
	x0, x1 := 0.06*f, 0.94*f
	mid := f / 2

	type seg struct {
		x, y float64
		c    int
	}
	// Sample all cables; sort by "depth" (cos of the phase) so that at each
	// crossing the cable coming to the front is drawn last -> braid look.
	var pts []seg
	steps := int(f * 2)
	for i := 0; i <= steps; i++ {
		t := float64(i) / float64(steps)
		x := x0 + (x1-x0)*t
		for c := range cables {
			y := mid + amp*math.Sin(2*math.Pi*t*turns+cables[c].phase)
			pts = append(pts, seg{x, y, c})
		}
	}
	depth := func(s seg) float64 {
		t := (s.x - x0) / (x1 - x0)
		return math.Cos(2*math.Pi*t*turns + cables[s.c].phase)
	}
	// Two passes: back half first (depth < 0), then front half; within a pass
	// draw outline then fill per point so overlaps of the same cable stay clean.
	for pass := 0; pass < 2; pass++ {
		for _, s := range pts {
			d := depth(s)
			if (pass == 0) != (d < 0) {
				continue
			}
			// Slight perspective: cables in the back are a bit thinner/darker.
			scale := 0.86 + 0.14*(d+1)/2
			disc(img, s.x, s.y, (thick/2+edge)*scale, cables[s.c].outline)
		}
		for _, s := range pts {
			d := depth(s)
			if (pass == 0) != (d < 0) {
				continue
			}
			scale := 0.86 + 0.14*(d+1)/2
			col := cables[s.c].fill
			if d < 0 {
				col = shade(col, 0.82)
			}
			disc(img, s.x, s.y, thick/2*scale, col)
			// Specular highlight strip on top of the cable.
			disc(img, s.x, s.y-thick*0.22*scale, thick*0.16*scale, lighten(col, 0.35))
		}
	}
	// Connector plugs at both ends (gray blocks), so it reads as "cables".
	plugW, plugH := 0.09*f, thick*1.9
	for c := range cables {
		for _, end := range []float64{x0, x1} {
			y := mid + amp*math.Sin(cables[c].phase+boolf(end == x1)*2*math.Pi*turns)
			left := end - plugW/2
			rectangle(img, left, y-plugH/2, plugW, plugH, color.NRGBA{0x2B, 0x2F, 0x38, 0xFF})
			rectangle(img, left+edge, y-plugH/2+edge, plugW-2*edge, plugH-2*edge, color.NRGBA{0x8B, 0x93, 0xA5, 0xFF})
		}
	}
	return downsample(img, ss)
}

func boolf(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

func shade(c color.NRGBA, k float64) color.NRGBA {
	return color.NRGBA{uint8(float64(c.R) * k), uint8(float64(c.G) * k), uint8(float64(c.B) * k), c.A}
}

func lighten(c color.NRGBA, k float64) color.NRGBA {
	l := func(v uint8) uint8 { return uint8(float64(v) + (255-float64(v))*k) }
	return color.NRGBA{l(c.R), l(c.G), l(c.B), c.A}
}

func disc(img *image.NRGBA, cx, cy, r float64, col color.NRGBA) {
	x0, x1 := int(cx-r)-1, int(cx+r)+1
	y0, y1 := int(cy-r)-1, int(cy+r)+1
	b := img.Bounds()
	for y := max(y0, b.Min.Y); y <= min(y1, b.Max.Y-1); y++ {
		for x := max(x0, b.Min.X); x <= min(x1, b.Max.X-1); x++ {
			dx, dy := float64(x)+0.5-cx, float64(y)+0.5-cy
			if dx*dx+dy*dy <= r*r {
				img.SetNRGBA(x, y, col)
			}
		}
	}
}

func rectangle(img *image.NRGBA, x, y, w, h float64, col color.NRGBA) {
	b := img.Bounds()
	for py := max(int(y), b.Min.Y); py < min(int(y+h), b.Max.Y); py++ {
		for px := max(int(x), b.Min.X); px < min(int(x+w), b.Max.X); px++ {
			img.SetNRGBA(px, py, col)
		}
	}
}

// downsample box-filters img by factor ss (premultiplied averaging).
func downsample(img *image.NRGBA, ss int) *image.NRGBA {
	W := img.Bounds().Dx() / ss
	out := image.NewNRGBA(image.Rect(0, 0, W, W))
	n := float64(ss * ss)
	for y := 0; y < W; y++ {
		for x := 0; x < W; x++ {
			var r, g, b, a float64
			for dy := 0; dy < ss; dy++ {
				for dx := 0; dx < ss; dx++ {
					c := img.NRGBAAt(x*ss+dx, y*ss+dy)
					af := float64(c.A) / 255
					r += float64(c.R) * af
					g += float64(c.G) * af
					b += float64(c.B) * af
					a += af
				}
			}
			if a == 0 {
				continue
			}
			out.SetNRGBA(x, y, color.NRGBA{uint8(r / a), uint8(g / a), uint8(b / a), uint8(a / n * 255)})
		}
	}
	return out
}

// dib encodes img as a 32-bit BITMAPINFOHEADER icon image (XOR + AND masks).
func dib(img *image.NRGBA) []byte {
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	var buf bytes.Buffer
	maskRow := ((w + 31) / 32) * 4
	xorSize := w * h * 4
	andSize := maskRow * h
	hdr := []uint32{40, uint32(w), uint32(h * 2)}
	for _, v := range hdr {
		binary.Write(&buf, binary.LittleEndian, v)
	}
	binary.Write(&buf, binary.LittleEndian, uint16(1))  // planes
	binary.Write(&buf, binary.LittleEndian, uint16(32)) // bpp
	for _, v := range []uint32{0, uint32(xorSize + andSize), 0, 0, 0, 0} {
		binary.Write(&buf, binary.LittleEndian, v)
	}
	for y := h - 1; y >= 0; y-- { // bottom-up
		for x := 0; x < w; x++ {
			c := img.NRGBAAt(x, y)
			buf.Write([]byte{c.B, c.G, c.R, c.A})
		}
	}
	buf.Write(make([]byte, andSize)) // AND mask: alpha does the work
	return buf.Bytes()
}

func main() {
	outDir := "assets"
	if len(os.Args) > 1 {
		outDir = os.Args[1]
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	type entry struct {
		size int
		data []byte
	}
	var entries []entry
	for _, s := range sizes {
		img := render(s, 6)
		var data []byte
		if s >= 128 {
			var b bytes.Buffer
			png.Encode(&b, img)
			data = b.Bytes()
		} else {
			data = dib(img)
		}
		entries = append(entries, entry{s, data})
		if s == 256 {
			f, _ := os.Create(filepath.Join(outDir, "maxmix.png"))
			png.Encode(f, img)
			f.Close()
		}
	}
	// ICO container.
	var ico bytes.Buffer
	binary.Write(&ico, binary.LittleEndian, uint16(0))
	binary.Write(&ico, binary.LittleEndian, uint16(1))
	binary.Write(&ico, binary.LittleEndian, uint16(len(entries)))
	offset := 6 + 16*len(entries)
	for _, e := range entries {
		dim := uint8(e.size)
		if e.size >= 256 {
			dim = 0
		}
		ico.Write([]byte{dim, dim, 0, 0})
		binary.Write(&ico, binary.LittleEndian, uint16(1))
		binary.Write(&ico, binary.LittleEndian, uint16(32))
		binary.Write(&ico, binary.LittleEndian, uint32(len(e.data)))
		binary.Write(&ico, binary.LittleEndian, uint32(offset))
		offset += len(e.data)
	}
	for _, e := range entries {
		ico.Write(e.data)
	}
	path := filepath.Join(outDir, "maxmix.ico")
	if err := os.WriteFile(path, ico.Bytes(), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("wrote %s (%d images) and maxmix.png\n", path, len(entries))
}
