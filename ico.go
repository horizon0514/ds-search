package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"image/png"
)

// decodeICO reads a Windows icon, as many favicons are: its largest image
// up to 64 px, stored as PNG or as a 32-bit DIB.
func decodeICO(b []byte) (image.Image, error) {
	if len(b) < 6 || binary.LittleEndian.Uint16(b[0:]) != 0 || binary.LittleEndian.Uint16(b[2:]) != 1 {
		return nil, errors.New("not an ICO")
	}
	n := int(binary.LittleEndian.Uint16(b[4:]))
	best, bestSize := -1, 0
	for i := 0; i < n && 6+16*(i+1) <= len(b); i++ {
		e := b[6+16*i:]
		w := int(e[0])
		if w == 0 {
			w = 256
		}
		if (w > bestSize && w <= 64) || best < 0 {
			best, bestSize = i, w
		}
	}
	if best < 0 {
		return nil, errors.New("empty ICO")
	}
	e := b[6+16*best:]
	size := int(binary.LittleEndian.Uint32(e[8:]))
	off := int(binary.LittleEndian.Uint32(e[12:]))
	if off < 0 || size <= 0 || off+size > len(b) {
		return nil, errors.New("bad ICO entry")
	}
	data := b[off : off+size]
	if bytes.HasPrefix(data, []byte("\x89PNG")) {
		return png.Decode(bytes.NewReader(data))
	}
	if len(data) < 40 {
		return nil, errors.New("bad DIB")
	}
	hdr := int(binary.LittleEndian.Uint32(data[0:]))
	w := int(int32(binary.LittleEndian.Uint32(data[4:])))
	h := int(int32(binary.LittleEndian.Uint32(data[8:]))) / 2 // with the AND mask
	bpp := int(binary.LittleEndian.Uint16(data[14:]))
	if bpp != 32 || w <= 0 || h <= 0 || w > 256 || h > 256 || hdr+w*h*4 > len(data) {
		return nil, errors.New("unsupported DIB")
	}
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	px := data[hdr:]
	for y := 0; y < h; y++ {
		row := px[(h-1-y)*w*4:]
		for x := 0; x < w; x++ {
			p := row[x*4:]
			img.SetNRGBA(x, y, color.NRGBA{R: p[2], G: p[1], B: p[0], A: p[3]})
		}
	}
	return img, nil
}
