package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"testing"
)

func dib(w, h, bpp int, top bool) []byte {
	stride := ((w*bpp + 31) / 32) * 4
	b := make([]byte, 40+stride*h)
	binary.LittleEndian.PutUint32(b, 40)
	binary.LittleEndian.PutUint32(b[4:], uint32(w))
	if top {
		h = -h
	}
	binary.LittleEndian.PutUint32(b[8:], uint32(int32(h)))
	binary.LittleEndian.PutUint16(b[12:], 1)
	binary.LittleEndian.PutUint16(b[14:], uint16(bpp))
	return b
}
func TestDIBOrientationAndPadding(t *testing.T) {
	for _, top := range []bool{true, false} {
		b := dib(1, 2, 24, top)
		copy(b[40:], []byte{0, 0, 255, 0, 255, 0, 0, 0})
		im, e := decodeDIB(b, 10)
		if e != nil {
			t.Fatal(e)
		}
		want := color.NRGBA{R: 255, A: 255}
		y := 0
		if !top {
			y = 1
		}
		if im.At(0, y) != want {
			t.Fatalf("orientation top=%v: %v", top, im.At(0, y))
		}
	}
}
func TestDIBV5Alpha(t *testing.T) {
	b := make([]byte, 128)
	binary.LittleEndian.PutUint32(b, 124)
	binary.LittleEndian.PutUint32(b[4:], 1)
	binary.LittleEndian.PutUint32(b[8:], 1)
	binary.LittleEndian.PutUint16(b[12:], 1)
	binary.LittleEndian.PutUint16(b[14:], 32)
	binary.LittleEndian.PutUint32(b[16:], 3)
	for i, m := range []uint32{0xff0000, 0xff00, 0xff, 0xff000000} {
		binary.LittleEndian.PutUint32(b[40+i*4:], m)
	}
	copy(b[124:], []byte{10, 20, 30, 64})
	im, e := decodeDIB(b, 1)
	if e != nil {
		t.Fatal(e)
	}
	if im.At(0, 0) != (color.NRGBA{R: 30, G: 20, B: 10, A: 64}) {
		t.Fatal(im.At(0, 0))
	}
	b[127] = 0
	im, e = decodeDIB(b, 1)
	if e != nil {
		t.Fatal(e)
	}
	if im.At(0, 0).(color.NRGBA).A != 0 {
		t.Fatal("explicit alpha lost")
	}
}
func TestDIBReject(t *testing.T) {
	for _, mutate := range []func([]byte) []byte{
		func(b []byte) []byte { return b[:39] }, func(b []byte) []byte { return b[:len(b)-1] },
		func(b []byte) []byte { binary.LittleEndian.PutUint32(b[4:], 0x7fffffff); return b },
		func(b []byte) []byte { binary.LittleEndian.PutUint32(b[8:], 0x80000000); return b },
		func(b []byte) []byte { binary.LittleEndian.PutUint32(b[16:], 1); return b },
		func(b []byte) []byte { binary.LittleEndian.PutUint16(b[14:], 2); return b },
	} {
		if _, e := decodeDIB(mutate(dib(2, 2, 24, false)), 100); e == nil {
			t.Fatal("malformed DIB accepted")
		}
	}
}
func TestDIB16AndOpaque32(t *testing.T) {
	b := dib(1, 1, 16, false)
	binary.LittleEndian.PutUint16(b[40:], 0x7c00)
	im, e := decodeDIB(b, 1)
	if e != nil || im.At(0, 0) != (color.NRGBA{R: 255, A: 255}) {
		t.Fatal(im, e)
	}
	b = dib(1, 1, 32, false)
	copy(b[40:], []byte{3, 2, 1, 0})
	im, e = decodeDIB(b, 1)
	if e != nil || im.At(0, 0) != (color.NRGBA{R: 1, G: 2, B: 3, A: 255}) {
		t.Fatal(im, e)
	}
}
func TestPNGNormalization(t *testing.T) {
	im := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	im.SetNRGBA(0, 0, color.NRGBA{R: 200, G: 33, B: 70, A: 97})
	var in bytes.Buffer
	if e := png.Encode(&in, im); e != nil {
		t.Fatal(e)
	}
	c := Config{MaxImageBytes: 4096, MaxPixels: 4}
	out, e := normalize(rawImage{data: in.Bytes(), png: true}, c)
	if e != nil {
		t.Fatal(e)
	}
	got, e := png.Decode(bytes.NewReader(out))
	if e != nil {
		t.Fatal(e)
	}
	if color.NRGBAModel.Convert(got.At(0, 0)) != im.NRGBAAt(0, 0) {
		t.Fatal("alpha or color lost")
	}
	c.MaxPixels = 3
	if _, e = normalize(rawImage{data: in.Bytes(), png: true}, c); e == nil {
		t.Fatal("pixel limit ignored")
	}
	c.MaxPixels = 4
	c.MaxImageBytes = 20
	if _, e = normalize(rawImage{data: in.Bytes(), png: true}, c); e == nil {
		t.Fatal("byte limit ignored")
	}
}
func FuzzDIB(f *testing.F) {
	f.Add(dib(1, 1, 24, false))
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, b []byte) { _, _ = decodeDIB(b, 1024) })
}
