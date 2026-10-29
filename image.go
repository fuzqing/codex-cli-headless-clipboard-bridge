package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math/bits"
)

var errNoImage = errors.New("clipboard is empty or contains no supported image")

type rawImage struct {
	data []byte
	png  bool
}
type cappedBuffer struct {
	bytes.Buffer
	max int
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if len(p) > b.max-b.Len() {
		return 0, errors.New("PNG exceeds max_image_bytes")
	}
	return b.Buffer.Write(p)
}

func normalize(raw rawImage, c Config) ([]byte, error) {
	var im image.Image
	var err error
	if raw.png {
		ic, e := png.DecodeConfig(bytes.NewReader(raw.data))
		if e != nil {
			return nil, e
		}
		if ic.Width <= 0 || ic.Height <= 0 || int64(ic.Width)*int64(ic.Height) > int64(c.MaxPixels) {
			return nil, errors.New("image exceeds max_pixels")
		}
		im, err = png.Decode(bytes.NewReader(raw.data))
	} else {
		im, err = decodeDIB(raw.data, c.MaxPixels)
	}
	if err != nil {
		return nil, err
	}
	b := &cappedBuffer{max: c.MaxImageBytes}
	err = (&png.Encoder{CompressionLevel: png.BestSpeed}).Encode(b, im)
	return b.Bytes(), err
}

// Packed CF_DIB / CF_DIBV5. RLE and embedded color profiles are rejected.
func decodeDIB(b []byte, maxPixels int) (image.Image, error) {
	bad := func() (image.Image, error) { return nil, errors.New("unsupported or malformed DIB") }
	if len(b) < 40 {
		return bad()
	}
	u16 := func(i int) uint16 { return binary.LittleEndian.Uint16(b[i:]) }
	u32 := func(i int) uint32 { return binary.LittleEndian.Uint32(b[i:]) }
	h := int(u32(0))
	if h != 40 && h != 52 && h != 56 && h != 108 && h != 124 {
		return bad()
	}
	if h > len(b) {
		return bad()
	}
	w, sh := int64(int32(u32(4))), int64(int32(u32(8)))
	height := sh
	if height < 0 {
		height = -height
	}
	if w <= 0 || height <= 0 || w > int64(maxPixels) || height > int64(maxPixels)/w || u16(12) != 1 {
		return bad()
	}
	bpp, compression := int(u16(14)), u32(16)
	if bpp != 1 && bpp != 4 && bpp != 8 && bpp != 16 && bpp != 24 && bpp != 32 {
		return bad()
	}
	if compression != 0 && compression != 3 && compression != 6 {
		return bad()
	}
	if compression != 0 && bpp != 16 && bpp != 32 {
		return bad()
	}
	if h == 124 && (u32(112) != 0 || u32(116) != 0) {
		return nil, errors.New("DIB color profiles unsupported; copy as PNG")
	}
	off := h
	masks := [4]uint32{}
	if bpp == 16 {
		masks = [4]uint32{0x7c00, 0x3e0, 0x1f, 0}
	}
	if bpp == 32 {
		masks = [4]uint32{0xff0000, 0xff00, 0xff, 0}
	}
	if compression == 3 || compression == 6 {
		count := 3
		if compression == 6 {
			if h != 40 && h < 56 {
				return bad()
			}
			count = 4
		}
		start := 40
		if h == 40 {
			if len(b) < off+count*4 {
				return bad()
			}
			off += count * 4
		} else if h >= 56 {
			count = 4
		}
		for i := 0; i < count; i++ {
			masks[i] = u32(start + i*4)
		}
	}
	if h >= 56 && bpp == 32 && u32(52) != 0 {
		masks = [4]uint32{u32(40), u32(44), u32(48), u32(52)}
	}
	if bpp == 16 || bpp == 32 {
		var used uint32
		for i, m := range masks {
			if m == 0 {
				if i < 3 {
					return bad()
				}
				continue
			}
			shifted := m >> bits.TrailingZeros32(m)
			if shifted&(shifted+1) != 0 || used&m != 0 || (bpp == 16 && m > 65535) {
				return bad()
			}
			used |= m
		}
	}
	var palette []color.NRGBA
	n := int64(u32(32))
	if bpp <= 8 && n == 0 {
		n = 1 << bpp
	}
	if n > 256 || n < 0 || (bpp <= 8 && n > 1<<bpp) || int64(off)+n*4 > int64(len(b)) {
		return bad()
	}
	if bpp <= 8 {
		palette = make([]color.NRGBA, n)
		for i := range palette {
			v := b[off+i*4:]
			palette[i] = color.NRGBA{R: v[2], G: v[1], B: v[0], A: 255}
		}
	}
	off += int(n) * 4
	stride := ((w*int64(bpp) + 31) / 32) * 4
	if int64(off)+stride*height > int64(len(b)) {
		return bad()
	}
	im := image.NewNRGBA(image.Rect(0, 0, int(w), int(height)))
	component := func(v, m uint32) uint8 {
		if m == 0 {
			return 255
		}
		v = (v & m) >> bits.TrailingZeros32(m)
		max := m >> bits.TrailingZeros32(m)
		return uint8((uint64(v)*255 + uint64(max)/2) / uint64(max))
	}
	for y := 0; y < int(height); y++ {
		sy := y
		if sh > 0 {
			sy = int(height) - 1 - y
		}
		row := b[off+int(stride)*sy:]
		for x := 0; x < int(w); x++ {
			var c color.NRGBA
			switch bpp {
			case 1, 4, 8:
				index := int(row[x*bpp/8]>>uint(8-bpp-(x*bpp%8))) & ((1 << bpp) - 1)
				if index >= len(palette) {
					return bad()
				}
				c = palette[index]
			case 24:
				p := row[x*3:]
				c = color.NRGBA{R: p[2], G: p[1], B: p[0], A: 255}
			case 16, 32:
				var v uint32
				if bpp == 16 {
					v = uint32(binary.LittleEndian.Uint16(row[x*2:]))
				} else {
					v = binary.LittleEndian.Uint32(row[x*4:])
				}
				c = color.NRGBA{R: component(v, masks[0]), G: component(v, masks[1]), B: component(v, masks[2]), A: component(v, masks[3])}
			}
			im.SetNRGBA(x, y, c)
		}
	}
	return im, nil
}

func imageError(stage string, err error) error { return fmt.Errorf("%s: %w", stage, err) }
