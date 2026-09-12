//go:build ignore

// Command generate draws godl's tray icon and writes the three forms
// the platforms want: a colored PNG for Linux, the same art in an ICO
// container for Windows, and a monochrome template PNG for macOS, which
// tints template images itself to match the menu bar.
//
// Run with: go run ./tray/icon/generate.go
package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
)

// accent is godl's blue. Picked to stay legible on both a light and a
// dark tray, which a white or black glyph cannot manage alone.
var accent = color.NRGBA{R: 0x2F, G: 0x81, B: 0xF7, A: 0xFF}

// draw renders a download glyph — a downward arrow over a baseline — at
// size×size. Proportions are in fractions of size so every resolution
// is the same drawing rather than a scaled bitmap.
func draw(size int, c color.NRGBA) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	f := func(v float64) int { return int(v * float64(size)) }

	shaftX0, shaftX1 := f(0.406), f(0.594)
	shaftY0, shaftY1 := f(0.125), f(0.500)
	fill(img, shaftX0, shaftY0, shaftX1, shaftY1, c)

	// Arrowhead: an isosceles triangle, drawn as rows that narrow
	// toward the tip so it stays symmetric at every size.
	headTop, headBottom := shaftY1, f(0.719)
	headHalf := float64(f(0.281))
	for y := headTop; y < headBottom; y++ {
		t := float64(y-headTop) / float64(headBottom-headTop)
		half := int(headHalf * (1 - t))
		fill(img, size/2-half, y, size/2+half, y+1, c)
	}

	fill(img, f(0.219), f(0.797), f(0.781), f(0.906), c)
	return img
}

func fill(img *image.NRGBA, x0, y0, x1, y1 int, c color.NRGBA) {
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			if image.Pt(x, y).In(img.Bounds()) {
				img.SetNRGBA(x, y, c)
			}
		}
	}
}

func encodePNG(img image.Image) []byte {
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

// encodeICO packs PNG-compressed entries into an ICO container, which
// Windows has accepted since Vista — far simpler than emitting the
// legacy BMP-with-AND-mask form, and smaller.
func encodeICO(sizes []int, c color.NRGBA) []byte {
	var body bytes.Buffer
	var dir bytes.Buffer
	offset := 6 + 16*len(sizes)

	for _, s := range sizes {
		data := encodePNG(draw(s, c))
		// 0 means 256 in the ICO width/height bytes.
		dim := byte(s)
		if s >= 256 {
			dim = 0
		}
		dir.Write([]byte{dim, dim, 0, 0})
		binary.Write(&dir, binary.LittleEndian, uint16(1))  // color planes
		binary.Write(&dir, binary.LittleEndian, uint16(32)) // bits per pixel
		binary.Write(&dir, binary.LittleEndian, uint32(len(data)))
		binary.Write(&dir, binary.LittleEndian, uint32(offset))
		offset += len(data)
		body.Write(data)
	}

	var out bytes.Buffer
	binary.Write(&out, binary.LittleEndian, uint16(0)) // reserved
	binary.Write(&out, binary.LittleEndian, uint16(1)) // type: icon
	binary.Write(&out, binary.LittleEndian, uint16(len(sizes)))
	out.Write(dir.Bytes())
	out.Write(body.Bytes())
	return out.Bytes()
}

func main() {
	dir := filepath.Dir(os.Args[0])
	if wd, err := os.Getwd(); err == nil {
		dir = filepath.Join(wd, "tray", "icon")
	}
	write := func(name string, data []byte) {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			panic(err)
		}
	}
	write("godl.png", encodePNG(draw(64, accent)))
	write("godl.ico", encodeICO([]int{16, 32, 48, 64}, accent))
	// macOS template icons must be black with alpha; the system
	// recolors them for light/dark menu bars.
	write("godl-template.png", encodePNG(draw(64, color.NRGBA{A: 0xFF})))
}
