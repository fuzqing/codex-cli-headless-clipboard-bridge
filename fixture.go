package main

import (
	"image"
	"image/png"
	"io"
)

func encodeFixture(w io.Writer, im image.Image) error {
	return (&png.Encoder{CompressionLevel: png.BestSpeed}).Encode(w, im)
}
