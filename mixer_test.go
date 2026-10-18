package main

import (
	"encoding/binary"
	"math"
	"testing"
)

func TestRingPushPopTrim(t *testing.T) {
	r := newRing(2)
	r.push([]float32{0.1, -0.2, 0.3, -0.9, 0.5, 0.6})
	if n := r.frames(); n != 3 {
		t.Fatalf("frames = %d, want 3", n)
	}
	if p := r.takePeak(); p != 0.9 {
		t.Errorf("peak = %v, want 0.9", p)
	}
	if p := r.takePeak(); p != 0 {
		t.Errorf("peak after reset = %v, want 0", p)
	}

	dst := make([]float32, 4)
	if n := r.pop(dst, 2); n != 2 {
		t.Fatalf("pop = %d, want 2", n)
	}
	if dst[0] != 0.1 || dst[3] != -0.9 {
		t.Errorf("pop returned %v", dst)
	}

	r.push([]float32{1, 1, 2, 2, 3, 3})
	if dropped := r.trim(2); dropped != 2 {
		t.Errorf("trim dropped %d, want 2", dropped)
	}
	if n := r.pop(dst, 10); n != 2 || dst[0] != 2 || dst[2] != 3 {
		t.Errorf("after trim pop = %d %v, want the two newest frames", n, dst)
	}
	if r.trim(5) != 0 {
		t.Error("trim on a short ring should drop nothing")
	}
}

func pcm16(vals ...int16) []byte {
	b := make([]byte, 0, len(vals)*2)
	for _, v := range vals {
		b = binary.LittleEndian.AppendUint16(b, uint16(v))
	}
	return b
}

func TestConverterSampleFormats(t *testing.T) {
	dst := sampleFormat{Rate: 48000, Channels: 2, Bits: 32, Float: true}

	c := newConverter(sampleFormat{Rate: 48000, Channels: 2, Bits: 16}, dst)
	out := c.convert(pcm16(16384, -32768), 1, nil)
	if len(out) != 2 || out[0] != 0.5 || out[1] != -1 {
		t.Errorf("16-bit: %v", out)
	}

	c = newConverter(sampleFormat{Rate: 48000, Channels: 2, Bits: 32, Float: true}, dst)
	var f []byte
	f = binary.LittleEndian.AppendUint32(f, math.Float32bits(0.25))
	f = binary.LittleEndian.AppendUint32(f, math.Float32bits(-0.75))
	if out = c.convert(f, 1, nil); out[0] != 0.25 || out[1] != -0.75 {
		t.Errorf("float32: %v", out)
	}

	c = newConverter(sampleFormat{Rate: 48000, Channels: 2, Bits: 24}, dst)
	if out = c.convert([]byte{0, 0, 0x40, 0, 0, 0xC0}, 1, nil); out[0] != 0.5 || out[1] != -0.5 {
		t.Errorf("24-bit: %v", out)
	}
}

func TestConverterMonoToStereo(t *testing.T) {
	c := newConverter(sampleFormat{Rate: 48000, Channels: 1, Bits: 16},
		sampleFormat{Rate: 48000, Channels: 2, Bits: 32, Float: true})
	out := c.convert(pcm16(16384, -16384), 2, nil)
	want := []float32{0.5, 0.5, -0.5, -0.5}
	for i := range want {
		if out[i] != want[i] {
			t.Fatalf("mono->stereo = %v, want %v", out, want)
		}
	}
}

func TestConverterResampleKeepsLengthAndContinuity(t *testing.T) {
	src := sampleFormat{Rate: 44100, Channels: 1, Bits: 16}
	dst := sampleFormat{Rate: 48000, Channels: 1, Bits: 32, Float: true}
	c := newConverter(src, dst)

	// One second of a slow ramp, fed in 10 ms blocks the way WASAPI delivers it.
	const block = 441
	total := 0
	prev := float32(-2)
	for blk := 0; blk < 100; blk++ {
		vals := make([]int16, block)
		for i := range vals {
			vals[i] = int16((blk*block + i) / 2)
		}
		out := c.convert(pcm16(vals...), block, nil)
		total += len(out)
		for _, v := range out {
			if v < prev-1e-6 {
				t.Fatalf("ramp went backwards at block %d: %v after %v", blk, v, prev)
			}
			prev = v
		}
	}
	if total < 47990 || total > 48010 {
		t.Errorf("1 s at 44.1 kHz became %d frames, want ~48000", total)
	}
}
