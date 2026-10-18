package main

// What NVIDIA App (ShadowPlay) is actually configured to record. Like
// steamcheck.go this only reads: NVIDIA's settings in the registry and the
// newest clip in its recordings folder.
//
// The settings live in HKCU\Software\NVIDIA Corporation\Global\ShadowPlay\NVSPCAPS
// as REG_BINARY values. AudioMode and MicMode are deliberately not interpreted:
// their meaning is undocumented. How many audio tracks a clip gets is read from
// the newest clip instead — NVIDIA names each track there ("System sounds",
// "Microphone").

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"
)

const nvspcapsKey = `Software\NVIDIA Corporation\Global\ShadowPlay\NVSPCAPS`

// Registry values that hold the microphone NVIDIA records.
const (
	nvMicNameValue = "{FCDCC7A2-EF61-4782-AF8A-74FC89A6A11A}" // friendly name, UTF-16
	nvMicIDValue   = "{E8541E5B-8DA8-4BB1-82B5-F841FDA12F5B}" // endpoint ID, UTF-16
)

// regBytes reads a value of any type as raw bytes.
func regBytes(root syscall.Handle, path, name string) ([]byte, bool) {
	p, _ := syscall.UTF16PtrFromString(path)
	var key syscall.Handle
	if err := syscall.RegOpenKeyEx(root, p, 0, syscall.KEY_QUERY_VALUE, &key); err != nil {
		return nil, false
	}
	defer syscall.RegCloseKey(key)
	n, _ := syscall.UTF16PtrFromString(name)
	var typ, size uint32
	if err := syscall.RegQueryValueEx(key, n, nil, &typ, nil, &size); err != nil || size == 0 {
		return nil, false
	}
	buf := make([]byte, size)
	if err := syscall.RegQueryValueEx(key, n, nil, &typ, &buf[0], &size); err != nil {
		return nil, false
	}
	return buf[:size], true
}

// utf16Bytes decodes a NUL-terminated UTF-16LE byte string.
func utf16Bytes(b []byte) string {
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = binary.LittleEndian.Uint16(b[2*i:])
	}
	return syscall.UTF16ToString(u)
}

// nvidiaConfig is the recording part of NVIDIA App's settings.
type nvidiaConfig struct {
	micOn   bool
	micName string
	micID   string
	dir     string // DefaultPathW: where clips are saved
}

// readNvidiaConfig reads NVSPCAPS; false when NVIDIA App is not set up.
func readNvidiaConfig() (nvidiaConfig, bool) {
	var cfg nvidiaConfig
	on, ok := regBytes(syscall.HKEY_CURRENT_USER, nvspcapsKey, "EnableMicrophone")
	if !ok {
		return cfg, false
	}
	cfg.micOn = len(on) >= 4 && binary.LittleEndian.Uint32(on) != 0
	if b, ok := regBytes(syscall.HKEY_CURRENT_USER, nvspcapsKey, nvMicNameValue); ok {
		cfg.micName = utf16Bytes(b)
	}
	if b, ok := regBytes(syscall.HKEY_CURRENT_USER, nvspcapsKey, nvMicIDValue); ok {
		cfg.micID = utf16Bytes(b)
	}
	if b, ok := regBytes(syscall.HKEY_CURRENT_USER, nvspcapsKey, "DefaultPathW"); ok {
		cfg.dir = utf16Bytes(b)
	}
	if cfg.micName == "" {
		cfg.micName = cfg.micID
	}
	return cfg, true
}

// ---- clips ----------------------------------------------------------------------

// nvidiaClipName matches NVIDIA's file naming: "Game 2026.10.14 - 11.41.44.02.mp4".
var nvidiaClipName = regexp.MustCompile(`\d{4}\.\d{2}\.\d{2} - \d{2}\.\d{2}\.\d{2}\.\d{2}`)

// nvidiaClip is the newest clip NVIDIA saved.
type nvidiaClip struct {
	path   string
	age    time.Duration
	tracks []string // audio tracks by the name NVIDIA gave them, "" when unnamed
}

// newestNvidiaClip looks at .mp4 files in dir and its immediate subfolders
// (NVIDIA saves into one folder per game) and returns the newest one that
// NVIDIA wrote. The folder may be shared with Steam exports, so a clip counts
// only when its metadata names NVIDIA or it follows NVIDIA's file naming.
func newestNvidiaClip(dir string) (nvidiaClip, bool) {
	type file struct {
		path string
		mod  time.Time
	}
	var files []file
	add := func(d string) {
		entries, err := os.ReadDir(d)
		if err != nil {
			return
		}
		for _, e := range entries {
			if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".mp4") {
				continue
			}
			if info, err := e.Info(); err == nil {
				files = append(files, file{filepath.Join(d, e.Name()), info.ModTime()})
			}
		}
	}
	add(dir)
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				add(filepath.Join(dir, e.Name()))
			}
		}
	}
	sort.Slice(files, func(a, b int) bool { return files[a].mod.After(files[b].mod) })
	for i, f := range files {
		if i == 10 {
			break
		}
		tracks, nvidia, err := mp4AudioTracks(f.path)
		if err != nil { // still being written: no moov yet
			continue
		}
		if nvidia || nvidiaClipName.MatchString(filepath.Base(f.path)) {
			return nvidiaClip{f.path, time.Since(f.mod), tracks}, true
		}
	}
	return nvidiaClip{}, false
}

// mp4Box is one ISO-BMFF box: its type and body.
type mp4Box struct {
	typ  string
	body []byte
}

// mp4Children splits a container body into its boxes.
func mp4Children(b []byte) []mp4Box {
	var out []mp4Box
	for len(b) >= 8 {
		size := uint64(binary.BigEndian.Uint32(b))
		typ, hdr := string(b[4:8]), uint64(8)
		switch size {
		case 0:
			size = uint64(len(b))
		case 1:
			if len(b) < 16 {
				return out
			}
			size, hdr = binary.BigEndian.Uint64(b[8:]), 16
		}
		if size < hdr || size > uint64(len(b)) {
			return out
		}
		out = append(out, mp4Box{typ, b[hdr:size]})
		b = b[size:]
	}
	return out
}

func mp4Child(b []byte, typ string) []byte {
	for _, c := range mp4Children(b) {
		if c.typ == typ {
			return c.body
		}
	}
	return nil
}

// mp4AudioTracks reads the moov box of an MP4 file without decoding any media
// and lists its audio tracks. nvidia reports whether the file's metadata names
// NVIDIA (NVIDIA App writes "Source": "NVIDIA APP" into udta/Xtra).
func mp4AudioTracks(path string) (tracks []string, nvidia bool, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, false, err
	}
	var moov []byte
	var hdr [16]byte
	for pos, end := int64(0), info.Size(); pos+8 <= end; {
		if _, err := f.ReadAt(hdr[:8], pos); err != nil {
			return nil, false, err
		}
		size, n := int64(binary.BigEndian.Uint32(hdr[:4])), int64(8)
		switch size {
		case 0:
			size = end - pos
		case 1:
			if _, err := f.ReadAt(hdr[8:16], pos+8); err != nil {
				return nil, false, err
			}
			size, n = int64(binary.BigEndian.Uint64(hdr[8:16])), 16
		}
		if size < n || pos+size > end {
			return nil, false, fmt.Errorf("%s: broken box at %d", filepath.Base(path), pos)
		}
		if string(hdr[4:8]) == "moov" {
			if size > 64<<20 {
				return nil, false, fmt.Errorf("%s: moov too large", filepath.Base(path))
			}
			moov = make([]byte, size-n)
			if _, err := f.ReadAt(moov, pos+n); err != nil && err != io.EOF {
				return nil, false, err
			}
			break
		}
		pos += size
	}
	if moov == nil {
		return nil, false, fmt.Errorf("%s: no moov box", filepath.Base(path))
	}
	tracks = []string{}
	for _, c := range mp4Children(moov) {
		switch c.typ {
		case "trak":
			hdlr := mp4Child(mp4Child(c.body, "mdia"), "hdlr")
			if len(hdlr) < 12 || string(hdlr[8:12]) != "soun" {
				continue
			}
			name := mp4Child(mp4Child(c.body, "udta"), "name")
			tracks = append(tracks, strings.TrimRight(string(name), "\x00"))
		case "udta":
			nvidia = nvidia || bytes.Contains(c.body, []byte("NVIDIA")) || bytes.Contains(c.body, utf16le("NVIDIA"))
		}
	}
	return tracks, nvidia, nil
}

// utf16le encodes an ASCII string as UTF-16LE.
func utf16le(s string) []byte {
	var out []byte
	for i := 0; i < len(s); i++ {
		out = append(out, s[i], 0)
	}
	return out
}

// ageText formats how long ago something happened.
func ageText(d time.Duration) string {
	switch {
	case d < 90*time.Second:
		return fmt.Sprintf("%.0f %s", d.Seconds(), tr("с"))
	case d < 90*time.Minute:
		return fmt.Sprintf("%.0f %s", d.Minutes(), tr("мин"))
	case d < 48*time.Hour:
		return fmt.Sprintf("%.0f %s", d.Hours(), tr("ч"))
	}
	return fmt.Sprintf("%.0f %s", d.Hours()/24, tr("дн"))
}

// ---- checks ---------------------------------------------------------------------

// nvidiaMicTap opens a capture of the microphone NVIDIA records, so the check
// hears exactly what NVIDIA would put into the microphone track.
func nvidiaMicTap(nv nvidiaConfig, canon sampleFormat) (*tap, error) {
	if !nv.micOn || nv.micID == "" {
		return nil, nil
	}
	return newDeviceTap(nv.micID, nv.micName, eCapture, canon, false)
}

// nvidiaChecks reports how NVIDIA App is configured. mic is the measured capture
// of NVIDIA's microphone (nil with micErr when it could not be opened);
// micInMix says a microphone is an input of the mix and mixMicIDs lists the
// ones that were found; voiceHeard says whether any other microphone picked up
// sound during the measurement.
func nvidiaChecks(nv nvidiaConfig, ok bool, mic *tap, micErr error, micInMix bool, mixMicIDs []string, voiceHeard bool) []check {
	if !ok {
		return []check{{tr("NVIDIA"), checkInfo, tr("настройки записи не найдены")}}
	}
	var out []check

	clip, haveClip := nvidiaClip{}, false
	if nv.dir != "" {
		clip, haveClip = newestNvidiaClip(nv.dir)
	}

	// The microphone track.
	name := nv.micName
	var st checkStatus
	var detail string
	switch {
	case !nv.micOn && micInMix:
		st, detail = checkOK, tr("выключен, но микрофон идёт через микс")
	case !nv.micOn:
		st, detail = checkFail, tr("выключен в NVIDIA: голоса в клипе не будет")
	case nv.micID == "":
		st, detail = checkWarn, tr("включён, но устройство не выбрано")
	case micErr != nil:
		st, detail = checkWarn, name+": "+tr("не удалось слушать: ")+micErr.Error()
	case containsFold(mixMicIDs, nv.micID):
		st, detail = checkWarn, name+": "+tr("этот же микрофон есть во входах микса: голос запишется дважды")
	case mic.peak < signalFloor && (voiceHeard || looksVirtual(name)):
		// Another microphone heard the voice (or this is a virtual cable), so
		// the device NVIDIA records is simply not carrying it.
		switch {
		case micInMix && haveClip && len(clip.tracks) == 1:
			st, detail = checkWarn, name+": "+tr("молчит; выключите микрофон в NVIDIA: голос уже идёт через микс")
		case micInMix:
			st, detail = checkWarn, name+": "+tr("молчит, вторая дорожка будет тихой; выключите микрофон в NVIDIA: голос уже идёт через микс")
		default:
			st, detail = checkFail, name+": "+tr("молчит, голоса в клипе не будет; выберите в NVIDIA настоящий микрофон")
		}
	case mic.peak < signalFloor:
		st, detail = checkWarn, name+": "+tr("тишина: скажите что-нибудь во время проверки")
	case micInMix:
		st, detail = checkWarn, name+": "+tr("включён, а микрофон ещё и в миксе: голос запишется дважды")
	default:
		st, detail = checkOK, name+", "+tr("есть звук, пик ")+dbfs(mic.peak)
	}
	out = append(out, check{tr("NVIDIA: микрофон"), st, detail})

	// What the newest clip actually contains.
	if nv.dir == "" {
		return out
	}
	if !haveClip {
		return append(out, check{tr("NVIDIA: последний клип"), checkInfo, nv.dir + ": " + tr("клипов NVIDIA нет")})
	}
	out = append(out, check{tr("NVIDIA: последний клип"), checkInfo,
		fmt.Sprintf("%s, %s %s", filepath.Base(clip.path), tr("возраст"), ageText(clip.age))})
	st, detail = checkInfo, fmt.Sprint(len(clip.tracks))
	var named []string
	for _, t := range clip.tracks {
		if t != "" {
			named = append(named, t)
		}
	}
	if len(named) > 0 {
		detail += ": " + strings.Join(named, ", ")
	}
	if len(clip.tracks) == 0 {
		st, detail = checkWarn, tr("в последнем клипе нет звука")
	}
	out = append(out, check{tr("NVIDIA: дорожки звука"), st, detail})
	return out
}
