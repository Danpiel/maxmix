package main

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDbfs(t *testing.T) {
	for _, c := range []struct {
		peak float32
		want string
	}{
		{0, "-inf dBFS"},
		{-1, "-inf dBFS"},
		{1, "0 dBFS"},
		{0.1, "-20 dBFS"},
		{0.5, "-6 dBFS"},
	} {
		if got := dbfs(c.peak); got != c.want {
			t.Errorf("dbfs(%v) = %q, want %q", c.peak, got, c.want)
		}
	}
}

func TestLooksVirtual(t *testing.T) {
	for name, want := range map[string]bool{
		"CABLE Output (VB-Audio Virtual Cable)":          true,
		"Voicemeeter Out B1 (VB-Audio Voicemeeter VAIO)": true,
		"Stereo Mix (Realtek Loopback)":                  true,
		"Microphone (Audeze Maxwell 2)":                  false,
		"Chat-Audeze Maxwell2":                           false,
	} {
		if got := looksVirtual(name); got != want {
			t.Errorf("looksVirtual(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestWriteWAV(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.wav")
	f := sampleFormat{Rate: 48000, Channels: 2}
	if err := writeWAV(path, []float32{0, 1, -1, 2, -2, 0.5}, f); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) != 44+12 {
		t.Fatalf("size = %d, want 56", len(b))
	}
	if string(b[0:4]) != "RIFF" || string(b[8:16]) != "WAVEfmt " || string(b[36:40]) != "data" {
		t.Fatalf("bad header % x", b[:44])
	}
	le16 := func(off int) int16 { return int16(binary.LittleEndian.Uint16(b[off:])) }
	le32 := func(off int) uint32 { return binary.LittleEndian.Uint32(b[off:]) }
	if le32(4) != 36+12 || le16(22) != 2 || le32(24) != 48000 || le32(28) != 48000*4 || le16(34) != 16 || le32(40) != 12 {
		t.Errorf("bad fmt chunk % x", b[:44])
	}
	want := []int16{0, 32767, -32767, 32767, -32767, 16383}
	for i, w := range want {
		if got := le16(44 + 2*i); got != w {
			t.Errorf("sample %d = %d, want %d (clipping/scaling)", i, got, w)
		}
	}
}

func TestAgeText(t *testing.T) {
	old := uiLang
	uiLang = "en"
	defer func() { uiLang = old }()
	for d, want := range map[time.Duration]string{
		30 * time.Second: "30 s",
		10 * time.Minute: "10 min",
		5 * time.Hour:    "5 h",
		72 * time.Hour:   "3 d",
	} {
		if got := ageText(d); got != want {
			t.Errorf("ageText(%v) = %q, want %q", d, got, want)
		}
	}
}
