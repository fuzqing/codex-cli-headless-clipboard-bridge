package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"os"
	"sort"
	"time"
)

func remoteSelftest(ctx context.Context, r *Remote, n int, path string) error {
	if n < 1 || n > 1000 {
		return errors.New("selftest count must be 1..1000")
	}
	var f *os.File
	if path != "" {
		var e error
		f, e = os.Create(path)
		if e != nil {
			return e
		}
		defer f.Close()
	}
	var totals []float64
	for i := 0; i < n; i++ {
		// Distinct alpha-bearing images. No local or remote PNG files are written.
		im := image.NewNRGBA(image.Rect(0, 0, 800, 450))
		for y := 0; y < 450; y++ {
			for x := 0; x < 800; x++ {
				im.SetNRGBA(x, y, color.NRGBA{R: uint8(x + i*17), G: uint8(y + i*31), B: uint8((x*y + i*79) % 251), A: uint8(127 + (x+y+i)%129)})
			}
		}
		m := Timings{}
		start := time.Now()
		b := &cappedBuffer{max: r.c.MaxImageBytes}
		tm := time.Now()
		if err := encodeFixture(b, im); err != nil {
			return err
		}
		m.EncodeMS = ms(tm)
		p, cancel := context.WithTimeout(ctx, time.Duration(r.c.TimeoutMS)*time.Millisecond)
		err := r.Upload(p, b.Bytes(), &m)
		cancel()
		m.TotalMS = ms(start)
		if f != nil {
			_ = json.NewEncoder(f).Encode(struct {
				Index   int  `json:"index"`
				Success bool `json:"success"`
				Timings
			}{i + 1, err == nil, m})
		}
		if err != nil {
			return fmt.Errorf("selftest %d: %w", i+1, err)
		}
		totals = append(totals, m.TotalMS)
		fmt.Printf("%d/%d sha256 verified bytes=%d total_ms=%.2f\n", i+1, n, b.Len(), m.TotalMS)
		if i == n/2 {
			r.Close()
			fmt.Println("transport closed deliberately; next upload must reconnect")
		}
		if (i+1)%10 == 0 || i == n-1 {
			p, cancel := context.WithTimeout(ctx, 5*time.Second)
			count, e := r.ownerCount(p)
			cancel()
			if e != nil {
				return e
			}
			fmt.Printf("remote xclip processes=%d\n", count)
			if count > 2 {
				return fmt.Errorf("unexpected xclip process growth: %d", count)
			}
		}
	}
	sort.Float64s(totals)
	q := func(p float64) float64 { i := int(float64(len(totals)-1)*p + 0.5); return totals[i] }
	fmt.Printf("remote pipeline (excludes Windows clipboard/hotkey/SendInput): n=%d min=%.2f p50=%.2f p95=%.2f p99=%.2f max=%.2f ms\n", len(totals), totals[0], q(.5), q(.95), q(.99), totals[len(totals)-1])
	return nil
}

func (r *Remote) ownerCount(ctx context.Context) (int, error) {
	count := 0
	err := r.withClient(ctx, func() error {
		s, e := r.client.NewSession()
		if e != nil {
			return e
		}
		defer s.Close()
		b := &cappedBuffer{max: 128}
		s.Stdout = b
		e = s.Run(`ps -u "$(id -u)" -o comm= | awk '$1 == "xclip" { n++ } END { print n+0 }'`)
		if e == nil {
			_, e = fmt.Sscan(b.String(), &count)
		}
		return e
	})
	return count, err
}
