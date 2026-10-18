package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// Source is one input of the mix. Exactly one of Match / Process / Mic is set.
type Source struct {
	// Match captures everything played on the render endpoint whose friendly
	// name contains this string (case-insensitive), e.g. "Game-Audeze Maxwell2".
	Match string `json:"match,omitempty"`
	// Process captures applications wherever they play (process loopback):
	//   "game"        - whatever looks like a game right now (see gamedetect.go)
	//   "auto"        - every process with an audio session, minus Exclude
	//   "Discord.exe" - one executable (its whole process tree)
	Process string `json:"process,omitempty"`
	// Mic captures a recording endpoint (a microphone) into the mix, matched by
	// a substring of its friendly name, e.g. "Chat-Audeze Maxwell2".
	Mic string `json:"mic,omitempty"`
	// Exclude lists executable names skipped by "auto" and "game".
	Exclude []string `json:"exclude,omitempty"`
	Gain    float32  `json:"gain"`
}

// Kind classifies a source for display.
func (s Source) Kind() string {
	switch {
	case s.Mic != "":
		return "mic"
	case s.Match != "":
		return "device"
	case s.Process == "game", s.Process == "auto":
		return s.Process
	default:
		return "process"
	}
}

// Config is loaded from maxmix.json next to the executable (or -config path).
type Config struct {
	Sources []Source `json:"sources"`
	// Target lists candidate render endpoints (substring match, first found wins).
	// The mixed signal is played into this endpoint; recorders capture it via loopback.
	Target []string `json:"target"`
	// Apps are executable names whose default render device is redirected
	// to the target (Windows per-app device preference).
	Apps []string `json:"apps"`
	// SampleRate of the internal mix; 0 = 48000.
	SampleRate int `json:"sample_rate"`
	// Lang is the UI language: "ru" or "en"; empty = follow Windows.
	Lang string `json:"lang,omitempty"`
	// Monitor is the render device that plays the microphone back during the
	// self-check only, so you can hear yourself while testing; empty = off.
	Monitor     string  `json:"monitor,omitempty"`
	MonitorGain float32 `json:"monitor_gain,omitempty"`
}

// defaultAutoExclude keeps browsers, launchers and the recorders themselves out
// of "auto" and "game" sources.
var defaultAutoExclude = []string{
	"Discord.exe", "steam.exe", "steamwebhelper.exe", "explorer.exe",
	"nvcontainer.exe", "NVIDIA Overlay.exe", "NVIDIA app.exe", "NVIDIA Share.exe",
	"chrome.exe", "msedge.exe", "firefox.exe", "waterfox.exe", "brave.exe", "opera.exe",
	"Spotify.exe", "wallpaper64.exe", "wallpaper32.exe", "ShareX.exe", "Teams.exe", "slack.exe",
	"Cursor.exe", "claude.exe", "Code.exe", "devenv.exe", "WindowsTerminal.exe", "Telegram.exe",
	"obs64.exe", "obs32.exe", "vlc.exe", "mpc-hc64.exe", "Zoom.exe", "Notion.exe",
	"Voicemeeter8x64.exe", "Voicemeeter8.exe", "audiodg.exe", "maxmix.exe",
}

func defaultConfig() Config {
	return Config{
		Sources: []Source{
			{Process: "game", Exclude: defaultAutoExclude, Gain: 1.0},
			{Process: "Discord.exe", Gain: 1.0},
		},
		Target: []string{"CABLE Input", "Voicemeeter AUX Input"},
		Apps:   []string{"steam.exe", "nvcontainer.exe", "NVIDIA Overlay.exe"},
	}
}

func defaultConfigPath() string {
	exe, err := os.Executable()
	if err != nil {
		return "maxmix.json"
	}
	return filepath.Join(filepath.Dir(exe), "maxmix.json")
}

func loadConfig(path string) (Config, error) {
	cfg := defaultConfig()
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return finalize(cfg)
		}
		return cfg, err
	}
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF}) // editors and PowerShell like to add a BOM
	var file Config
	if err := json.Unmarshal(data, &file); err != nil {
		return cfg, err
	}
	if len(file.Sources) > 0 {
		cfg.Sources = file.Sources
	}
	if len(file.Target) > 0 {
		cfg.Target = file.Target
	}
	if len(file.Apps) > 0 {
		cfg.Apps = file.Apps
	}
	if file.SampleRate > 0 {
		cfg.SampleRate = file.SampleRate
	}
	cfg.Monitor, cfg.MonitorGain = file.Monitor, file.MonitorGain
	cfg.Lang = file.Lang
	return finalize(cfg)
}

// finalize fills defaults and validates.
func finalize(cfg Config) (Config, error) {
	for i := range cfg.Sources {
		s := &cfg.Sources[i]
		if s.Gain == 0 {
			s.Gain = 1
		}
		set := 0
		for _, v := range []string{s.Match, s.Process, s.Mic} {
			if v != "" {
				set++
			}
		}
		if set != 1 {
			return cfg, errors.New("each source needs exactly one of \"match\", \"process\" or \"mic\"")
		}
		if (s.Process == "auto" || s.Process == "game") && s.Exclude == nil {
			s.Exclude = defaultAutoExclude
		}
	}
	if cfg.MonitorGain <= 0 {
		cfg.MonitorGain = 1
	}
	if cfg.SampleRate == 0 {
		cfg.SampleRate = 48000
	}
	if len(cfg.Target) == 0 {
		return cfg, errors.New("target must list at least one device")
	}
	if cfg.Lang != "ru" && cfg.Lang != "en" {
		cfg.Lang = ""
	}
	uiLang = cfg.Lang
	if uiLang == "" {
		uiLang = systemLang()
	}
	return cfg, nil
}

// save writes the config as JSON.
func (c Config) save(path string) error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

func (c Config) String() string {
	b, _ := json.MarshalIndent(c, "", "  ")
	return string(b)
}
