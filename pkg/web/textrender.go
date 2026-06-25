package web

import (
	"image"
	"image/color"
	"image/draw"
	"strings"

	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
)

// renderDocImage rasterises document text to a monospace image so text/markdown
// documents are viewable as images in gpix while the original stays
// downloadable. Uses the built-in 7x13 bitmap font (golang.org/x/image), so no
// font files are embedded.
func renderDocImage(content []byte, width, maxLines int) image.Image {
	const (
		glyphW = 7
		lineH  = 14
		padX   = 12
		padY   = 12
	)
	if width < 200 {
		width = 200
	}
	cols := (width - 2*padX) / glyphW
	if cols < 16 {
		cols = 16
	}

	lines := wrapLines(string(content), cols)
	truncated := false
	if len(lines) > maxLines {
		lines = lines[:maxLines]
		truncated = true
	}
	rows := len(lines)
	if truncated {
		rows++
	}
	if rows < 1 {
		rows = 1
	}
	height := 2*padY + rows*lineH

	img := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.Draw(img, img.Bounds(), image.NewUniform(color.RGBA{0xfb, 0xfb, 0xfa, 0xff}), image.Point{}, draw.Src)

	d := &font.Drawer{
		Dst:  img,
		Src:  image.NewUniform(color.RGBA{0x1a, 0x1a, 0x1a, 0xff}),
		Face: basicfont.Face7x13,
	}
	y := padY + 11 // baseline of first line
	for _, ln := range lines {
		d.Dot = fixed.P(padX, y)
		d.DrawString(ln)
		y += lineH
	}
	if truncated {
		d.Src = image.NewUniform(color.RGBA{0x99, 0x99, 0x99, 0xff})
		d.Dot = fixed.P(padX, y)
		d.DrawString("… (truncated — download for the full document)")
	}
	return img
}

// wrapLines normalises and hard-wraps text to at most cols columns.
func wrapLines(s string, cols int) []string {
	s = strings.ReplaceAll(s, "\t", "    ")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	var out []string
	for _, raw := range strings.Split(s, "\n") {
		r := []rune(raw)
		if len(r) == 0 {
			out = append(out, "")
			continue
		}
		for len(r) > cols {
			out = append(out, string(r[:cols]))
			r = r[cols:]
		}
		out = append(out, string(r))
	}
	return out
}
