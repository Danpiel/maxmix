package main

// What Steam Game Recording is actually configured to record. maxmix can put
// every source into one device, but Steam still decides whether it captures
// that device, individual applications, and the microphone. These checks read
// Steam's own settings (read-only) and the live recording session on disk.

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

// regString reads a REG_SZ value.
func regString(root syscall.Handle, path, name string) (string, bool) {
	p, _ := syscall.UTF16PtrFromString(path)
	var key syscall.Handle
	if err := syscall.RegOpenKeyEx(root, p, 0, syscall.KEY_QUERY_VALUE, &key); err != nil {
		return "", false
	}
	defer syscall.RegCloseKey(key)
	n, _ := syscall.UTF16PtrFromString(name)
	var typ, size uint32
	if err := syscall.RegQueryValueEx(key, n, nil, &typ, nil, &size); err != nil {
		return "", false
	}
	buf := make([]uint16, size/2+1)
	if err := syscall.RegQueryValueEx(key, n, nil, &typ, (*byte)(unsafe.Pointer(&buf[0])), &size); err != nil {
		return "", false
	}
	return syscall.UTF16ToString(buf), true
}

// steamPath returns the Steam installation directory.
func steamPath() string {
	if p, ok := regString(syscall.HKEY_CURRENT_USER, `Software\Valve\Steam`, "SteamPath"); ok && p != "" {
		return filepath.FromSlash(p)
	}
	for _, p := range []string{`C:\Program Files\Steam`, `C:\Program Files (x86)\Steam`} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// steamConfig is the Game Recording part of localconfig.vdf.
type steamConfig struct {
	path     string
	settings map[string]string
	extra    []string // ExtraAudioSessions session identifiers
}

// readSteamConfig parses the newest localconfig.vdf that has a GameRecording
// block. The format is Valve's KeyValues; a line scan is enough here.
func readSteamConfig() (steamConfig, bool) {
	root := steamPath()
	if root == "" {
		return steamConfig{}, false
	}
	users, err := os.ReadDir(filepath.Join(root, "userdata"))
	if err != nil {
		return steamConfig{}, false
	}
	var best steamConfig
	var bestTime time.Time
	for _, u := range users {
		p := filepath.Join(root, "userdata", u.Name(), "config", "localconfig.vdf")
		info, err := os.Stat(p)
		if err != nil {
			continue
		}
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		cfg := parseGameRecording(string(data))
		if len(cfg.settings) == 0 {
			continue
		}
		if best.settings == nil || info.ModTime().After(bestTime) {
			cfg.path = p
			best, bestTime = cfg, info.ModTime()
		}
	}
	return best, best.settings != nil
}

// parseGameRecording extracts the "GameRecording" block.
func parseGameRecording(text string) steamConfig {
	cfg := steamConfig{settings: map[string]string{}}
	lines := strings.Split(text, "\n")
	start, depth, inBlock, inExtra := -1, 0, false, false
	for i, raw := range lines {
		line := strings.TrimSpace(raw)
		if !inBlock {
			if strings.EqualFold(line, `"GameRecording"`) {
				start, inBlock, depth = i, true, 0
			}
			continue
		}
		_ = start
		switch {
		case line == "{":
			depth++
			continue
		case line == "}":
			depth--
			if depth <= 0 {
				return cfg
			}
			if depth == 1 {
				inExtra = false
			}
			continue
		}
		key, val, ok := vdfPair(line)
		if !ok {
			continue
		}
		if depth == 1 {
			cfg.settings[key] = val
			continue
		}
		if strings.EqualFold(key, "name") || inExtra {
			cfg.extra = append(cfg.extra, val)
		}
	}
	if len(cfg.settings) == 0 {
		return steamConfig{}
	}
	return cfg
}

// vdfPair splits `"key"  "value"`.
func vdfPair(line string) (string, string, bool) {
	if !strings.HasPrefix(line, `"`) {
		return "", "", false
	}
	rest := line[1:]
	end := strings.Index(rest, `"`)
	if end < 0 {
		return "", "", false
	}
	key := rest[:end]
	rest = strings.TrimSpace(rest[end+1:])
	if !strings.HasPrefix(rest, `"`) {
		return "", "", false // a block header, not a pair
	}
	rest = rest[1:]
	if end = strings.LastIndex(rest, `"`); end < 0 {
		return "", "", false
	}
	return key, rest[:end], true
}

// audioOptionName describes Recording_Audio_Option.
func audioOptionName(v string) string {
	switch v {
	case "0":
		return tr("только звук игры")
	case "1":
		return tr("игра и выбранные программы")
	case "2":
		return tr("весь системный звук")
	}
	return v
}

// sessionExe pulls the executable path out of a Steam audio session id.
func sessionExe(id string) string {
	if i := strings.Index(id, "|"); i >= 0 {
		id = id[i+1:]
	}
	if i := strings.Index(id, "%b"); i >= 0 {
		id = id[:i]
	}
	return id
}

// steamChecks reports how Steam is configured and whether it is recording.
func steamChecks(target Endpoint, micInMix bool) []check {
	var out []check
	cfg, ok := readSteamConfig()
	if !ok {
		return append(out, check{tr("Steam"), checkInfo, tr("настройки записи не найдены")})
	}
	if v := cfg.settings["BackgroundRecordMode"]; v == "0" || v == "" {
		out = append(out, check{tr("Steam: фоновая запись"), checkWarn, tr("выключена в настройках Steam")})
	} else {
		out = append(out, check{tr("Steam: фоновая запись"), checkOK, tr("включена")})
	}

	opt := cfg.settings["Recording_Audio_Option"]
	st, detail := checkOK, audioOptionName(opt)
	if opt != "2" {
		st = checkWarn
		detail += "; " + tr("надёжнее «весь системный звук»: тогда пишется весь микс с ") + target.Name
	}
	out = append(out, check{tr("Steam: что пишется"), st, detail})

	// Selected applications: their session ids go stale when an app updates.
	if opt == "1" {
		var live, stale []string
		for _, id := range cfg.extra {
			exe := sessionExe(id)
			name := filepath.Base(strings.ReplaceAll(exe, `\`, `/`))
			if p := devicePathToDOS(exe); p != "" {
				if _, err := os.Stat(p); err == nil {
					live = append(live, name)
					continue
				}
			}
			stale = append(stale, name+" ("+tr("путь устарел")+")")
		}
		sort.Strings(live)
		sort.Strings(stale)
		st, detail := checkOK, strings.Join(live, ", ")
		if len(stale) > 0 {
			st = checkWarn
			if detail != "" {
				detail += "; "
			}
			detail += strings.Join(stale, ", ") + " — " + tr("выберите программу в Steam заново")
		}
		if detail == "" {
			st, detail = checkWarn, tr("список пуст: кроме игры ничего не пишется")
		}
		out = append(out, check{tr("Steam: выбранные программы"), st, detail})
	}

	switch {
	case cfg.settings["Audio_Mic"] != "0" && micInMix:
		out = append(out, check{tr("Steam: микрофон"), checkWarn, tr("включён, а микрофон ещё и в миксе: голос запишется дважды")})
	case cfg.settings["Audio_Mic"] != "0":
		out = append(out, check{tr("Steam: микрофон"), checkOK, tr("включён")})
	case micInMix:
		out = append(out, check{tr("Steam: микрофон"), checkOK, tr("выключен, но микрофон идёт через микс")})
	default:
		out = append(out, check{tr("Steam: микрофон"), checkFail, tr("выключен в настройках записи Steam: голоса в клипе не будет")})
	}

	// Live session on disk.
	if dir := cfg.settings["BackgroundRecordPath"]; dir != "" {
		if newest, age, okDir := newestChunk(filepath.Join(dir, "video")); okDir {
			st, detail := checkWarn, fmt.Sprintf("%s %s, %s %.0f %s", tr("последний фрагмент"), filepath.Base(newest), tr("возраст"), age.Seconds(), tr("с"))
			if age < 30*time.Second {
				st = checkOK
				detail = tr("идёт прямо сейчас: ") + filepath.Base(filepath.Dir(newest))
			}
			out = append(out, check{tr("Steam: запись на диск"), st, detail})
		} else {
			out = append(out, check{tr("Steam: запись на диск"), checkWarn, dir + ": " + tr("файлов записи нет")})
		}
	}
	return out
}

// devicePathToDOS maps \Device\HarddiskVolumeN\... to a drive letter path.
func devicePathToDOS(p string) string {
	if !strings.HasPrefix(p, `\Device\`) {
		return p
	}
	rest := p[len(`\Device\`):]
	i := strings.Index(rest, `\`)
	if i < 0 {
		return ""
	}
	volume, tail := `\Device\`+rest[:i], rest[i:]
	for _, letter := range "ABCDEFGHIJKLMNOPQRSTUVWXYZ" {
		dos := string(letter) + ":"
		if queryDosDevice(dos) == volume {
			return dos + tail
		}
	}
	return ""
}

var procQueryDosDeviceW = kernel32.NewProc("QueryDosDeviceW")

func queryDosDevice(dos string) string {
	name, _ := syscall.UTF16PtrFromString(dos)
	buf := make([]uint16, 512)
	n, _, _ := procQueryDosDeviceW.Call(uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if n == 0 {
		return ""
	}
	return syscall.UTF16ToString(buf)
}

// newestChunk finds the most recently written recording fragment under dir.
func newestChunk(dir string) (string, time.Duration, bool) {
	sessions, err := os.ReadDir(dir)
	if err != nil {
		return "", 0, false
	}
	var newest string
	var newestTime time.Time
	for _, s := range sessions {
		if !s.IsDir() {
			continue
		}
		files, err := os.ReadDir(filepath.Join(dir, s.Name()))
		if err != nil {
			continue
		}
		for _, f := range files {
			if !strings.HasPrefix(f.Name(), "chunk-") {
				continue
			}
			info, err := f.Info()
			if err != nil {
				continue
			}
			if info.ModTime().After(newestTime) {
				newest, newestTime = filepath.Join(dir, s.Name(), f.Name()), info.ModTime()
			}
		}
	}
	if newest == "" {
		return "", 0, false
	}
	return newest, time.Since(newestTime), true
}
