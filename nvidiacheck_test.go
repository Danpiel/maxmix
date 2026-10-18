package main

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// box builds one ISO-BMFF box from its type and children.
func box(typ string, body ...[]byte) []byte {
	var b []byte
	for _, part := range body {
		b = append(b, part...)
	}
	out := binary.BigEndian.AppendUint32(nil, uint32(8+len(b)))
	return append(append(out, typ...), b...)
}

// track builds a trak with a handler type and an optional udta/name.
func track(handler, name string) []byte {
	hdlr := box("hdlr", make([]byte, 8), []byte(handler), make([]byte, 12))
	parts := [][]byte{box("mdia", hdlr)}
	if name != "" {
		parts = append(parts, box("udta", box("name", []byte(name+"\x00"))))
	}
	return box("trak", parts...)
}

func TestMp4AudioTracks(t *testing.T) {
	moov := box("moov",
		track("vide", "Video"),
		track("soun", "System sounds"),
		track("soun", "Microphone"),
		box("udta", box("Xtra", utf16le("NVIDIA APP"))),
	)
	file := append(box("ftyp", []byte("isom")), box("mdat", make([]byte, 64))...)
	file = append(file, moov...)
	path := filepath.Join(t.TempDir(), "clip.mp4")
	if err := os.WriteFile(path, file, 0o644); err != nil {
		t.Fatal(err)
	}

	tracks, nvidia, err := mp4AudioTracks(path)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(tracks, []string{"System sounds", "Microphone"}) {
		t.Errorf("tracks = %q", tracks)
	}
	if !nvidia {
		t.Error("NVIDIA marker in udta not detected")
	}
}

func TestMp4AudioTracksErrors(t *testing.T) {
	dir := t.TempDir()
	noMoov := filepath.Join(dir, "nomoov.mp4")
	os.WriteFile(noMoov, box("ftyp", []byte("isom")), 0o644)
	if _, _, err := mp4AudioTracks(noMoov); err == nil {
		t.Error("file without moov: expected an error")
	}

	broken := filepath.Join(dir, "broken.mp4")
	b := box("ftyp", []byte("isom"))
	binary.BigEndian.PutUint32(b, 1000) // claims to be larger than the file
	os.WriteFile(broken, b, 0o644)
	if _, _, err := mp4AudioTracks(broken); err == nil {
		t.Error("truncated box: expected an error")
	}
}

func TestMp4ChildrenStopsOnGarbage(t *testing.T) {
	good := box("free", []byte("abc"))
	got := mp4Children(append(good, 0, 0, 0, 3, 'x', 'x', 'x', 'x'))
	if len(got) != 1 || got[0].typ != "free" || string(got[0].body) != "abc" {
		t.Errorf("mp4Children = %+v", got)
	}
}
