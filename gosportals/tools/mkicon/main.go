// mkicon генерирует app.ico (16/32/48 px, 32bpp BMP в ICO) для exe.
// Иконка — стилизованная: красно-зелёное поле с белобрусчатым орнаментом слева.
// Запуск (не нужен при сборке, app.ico уже закоммичен):
//
//	go run ./tools/mkicon ../../app.ico
package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"os"
)

var (
	red     = color.RGBA{0xC8, 0x31, 0x3E, 0xFF}
	green   = color.RGBA{0x2E, 0x7D, 0x46, 0xFF}
	white   = color.RGBA{0xFF, 0xFF, 0xFF, 0xFF}
	nothing = color.RGBA{0, 0, 0, 0}
)

func draw(n int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, n, n))
	radius := n / 6
	barW := n / 5
	band := n * 2 / 3 // красно-зелёная граница
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			if outsideRound(x, y, n, radius) {
				img.SetRGBA(x, y, nothing)
				continue
			}
			switch {
			case x < barW:
				img.SetRGBA(x, y, white)
			case y < band:
				img.SetRGBA(x, y, red)
			default:
				img.SetRGBA(x, y, green)
			}
		}
	}
	// Ромбовидный орнамент на белой полосе.
	sz := max(3, n/14)
	cy := n / 2
	step := sz * 2
	for yy := cy - (cy/step)*step - step; yy < n+step; yy += step {
		if yy < 0 {
			continue
		}
		diamond(img, barW/2, yy, sz, red)
	}
	return img
}

func diamond(img *image.RGBA, cx, cy, r int, c color.RGBA) {
	for dy := -r; dy <= r; dy++ {
		w := r - abs(dy)
		for dx := -w; dx <= w; dx++ {
			x, y := cx+dx, cy+dy
			if image.Pt(x, y).In(img.Bounds()) {
				img.SetRGBA(x, y, c)
			}
		}
	}
}

func outsideRound(x, y, n, r int) bool {
	corner := func(cx, cy int) bool {
		dx, dy := x-cx, y-cy
		return dx*dx+dy*dy > r*r
	}
	switch {
	case x < r && y < r:
		return corner(r, r)
	case x >= n-r && y < r:
		return corner(n-r-1, r)
	case x < r && y >= n-r:
		return corner(r, n-r-1)
	case x >= n-r && y >= n-r:
		return corner(n-r-1, n-r-1)
	}
	return false
}

// dib32 кодирует изображение в 32-битный DIB (XOR + AND-маска) для ICO.
func dib32(img *image.RGBA) []byte {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	var buf bytes.Buffer
	binary.Write(&buf, binary.LittleEndian, uint32(40))    // biSize
	binary.Write(&buf, binary.LittleEndian, int32(w))      // biWidth
	binary.Write(&buf, binary.LittleEndian, int32(h*2))    // biHeight (XOR+AND)
	binary.Write(&buf, binary.LittleEndian, uint16(1))     // biPlanes
	binary.Write(&buf, binary.LittleEndian, uint16(32))    // biBitCount
	binary.Write(&buf, binary.LittleEndian, uint32(0))     // biCompression BI_RGB
	binary.Write(&buf, binary.LittleEndian, uint32(w*h*4)) // biSizeImage
	binary.Write(&buf, binary.LittleEndian, [4]int32{})    // остальное — нули
	for y := h - 1; y >= 0; y-- {                          // снизу вверх
		for x := 0; x < w; x++ {
			r, g, bl, a := img.RGBAAt(x, y).RGBA()
			buf.WriteByte(byte(bl >> 8))
			buf.WriteByte(byte(g >> 8))
			buf.WriteByte(byte(r >> 8))
			buf.WriteByte(byte(a >> 8))
		}
	}
	maskRow := (w + 31) / 32 * 4
	buf.Write(make([]byte, maskRow*h)) // AND-маска: всё нули (непрозрачно по маске)
	return buf.Bytes()
}

type icoEntry struct{ w, h int }

func writeICO(path string, images []*image.RGBA) error {
	var buf bytes.Buffer
	binary.Write(&buf, binary.LittleEndian, uint16(0))           // reserved
	binary.Write(&buf, binary.LittleEndian, uint16(1))           // type: icon
	binary.Write(&buf, binary.LittleEndian, uint16(len(images))) // count

	payloads := make([][]byte, len(images))
	offset := 6 + 16*len(images)
	for i, img := range images {
		payloads[i] = dib32(img)
		b := img.Bounds()
		bw, bh := byte(b.Dx()), byte(b.Dy())
		if b.Dx() == 256 {
			bw = 0
		}
		if b.Dy() == 256 {
			bh = 0
		}
		buf.WriteByte(bw)
		buf.WriteByte(bh)
		buf.WriteByte(0)                                                  // colors
		buf.WriteByte(0)                                                  // reserved
		binary.Write(&buf, binary.LittleEndian, uint16(1))                // planes
		binary.Write(&buf, binary.LittleEndian, uint16(32))               // bpp
		binary.Write(&buf, binary.LittleEndian, uint32(len(payloads[i]))) // size
		binary.Write(&buf, binary.LittleEndian, uint32(offset))
		offset += len(payloads[i])
	}
	for _, p := range payloads {
		buf.Write(p)
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func main() {
	out := "app.ico"
	if len(os.Args) > 1 {
		out = os.Args[1]
	}
	var imgs []*image.RGBA
	for _, n := range []int{16, 32, 48} {
		imgs = append(imgs, draw(n))
	}
	if err := writeICO(out, imgs); err != nil {
		fmt.Fprintln(os.Stderr, "ошибка:", err)
		os.Exit(1)
	}
	fmt.Println("создана иконка:", out)
}
