//go:build ignore

// Command generate draws godl's icon and writes every form the
// platforms want: a colored PNG for the Linux tray, the same art in an
// ICO container for Windows, a monochrome template PNG for macOS (which
// tints template images itself to match the menu bar), the hicolor
// app-icon sizes plus an SVG for the Linux application menu, and an
// ICNS for the macOS .app bundle.
//
// Run with: go run ./tray/icon/generate.go
package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
)

// appIconSizes are the hicolor theme's standard application icon
// sizes. 16 and 24 matter for menus and panels, 48 is the GNOME/KDE
// list default, 256 is what a large-icon view and GNOME Software use.
var appIconSizes = []int{16, 24, 32, 48, 64, 128, 256}

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

// svg is the same glyph as vector art, for the scalable/ slot: a
// desktop that renders the icon at a size not in appIconSizes scales
// this rather than a bitmap. Proportions match draw() exactly — the
// fractions there are the coordinates here, times 64.
func svg(c color.NRGBA) string {
	hex := fmt.Sprintf("#%02X%02X%02X", c.R, c.G, c.B)
	// viewBox only, no width/height: a scalable icon has to take the
	// size its container asks for, and fixed attributes pin it to 64px
	// in anything that honors them.
	return fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64">
  <rect x="26" y="8" width="12" height="24" fill="%[1]s"/>
  <path d="M14 32 L50 32 L32 46 Z" fill="%[1]s"/>
  <rect x="14" y="51" width="36" height="7" fill="%[1]s"/>
</svg>
`, hex)
}

// icnsTypes maps an ICNS entry type to the pixel size it holds. These
// are the PNG-capable types (10.7+), which is why no legacy mask
// entries are written: a modern macOS reads the PNG's own alpha.
//
// Several sizes appear twice on purpose — ic08 and ic13 are both 256px
// — because one is the 1x icon at that size and the other is the 2x
// icon for the size below it. Finder picks by slot, not by pixels, so
// omitting the retina slots gives a blurry icon on a retina display
// even though the pixels for it are already in the file.
var icnsTypes = []struct {
	code string
	size int
}{
	{"icp4", 16},   // 16x16
	{"icp5", 32},   // 32x32
	{"ic11", 32},   // 16x16@2x
	{"ic12", 64},   // 32x32@2x
	{"ic07", 128},  // 128x128
	{"ic13", 256},  // 128x128@2x
	{"ic08", 256},  // 256x256
	{"ic14", 512},  // 256x256@2x
	{"ic09", 512},  // 512x512
	{"ic10", 1024}, // 512x512@2x
}

// encodeICNS packs PNG entries into an ICNS container for the .app
// bundle's Resources. The format is a magic word, the total file
// length, then one entry per icon: a four-character type, the entry's
// own length *including* its eight-byte header, and the data.
// Everything is big-endian, unlike ICO.
func encodeICNS(c color.NRGBA) []byte {
	var body bytes.Buffer
	for _, t := range icnsTypes {
		data := encodePNG(draw(t.size, c))
		body.WriteString(t.code)
		binary.Write(&body, binary.BigEndian, uint32(len(data)+8))
		body.Write(data)
	}

	var out bytes.Buffer
	out.WriteString("icns")
	binary.Write(&out, binary.BigEndian, uint32(body.Len()+8))
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

	// The sizes the hicolor icon theme looks for, so the application
	// menu has a crisp icon at whatever size it renders at rather than
	// rescaling one. Written as app<size>.png and installed into
	// hicolor/<size>x<size>/apps by internal/desktop.
	for _, size := range appIconSizes {
		write(fmt.Sprintf("app%d.png", size), encodePNG(draw(size, accent)))
	}
	write("godl.svg", []byte(svg(accent)))
	write("godl.icns", encodeICNS(accent))
	write("godl.ico", encodeICO([]int{16, 32, 48, 64}, accent))
	// macOS template icons must be black with alpha; the system
	// recolors them for light/dark menu bars.
	write("godl-template.png", encodePNG(draw(64, color.NRGBA{A: 0xFF})))
}
