package main

import (
	"os"
	"path/filepath"
	"testing"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "maxmix.json")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadConfigMissingFileGivesDefaults(t *testing.T) {
	cfg, err := loadConfig(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Sources) != 2 || cfg.Sources[0].Process != "game" || cfg.Sources[1].Process != "Discord.exe" {
		t.Errorf("default sources = %+v", cfg.Sources)
	}
	if cfg.SampleRate != 48000 {
		t.Errorf("sample rate = %d, want 48000", cfg.SampleRate)
	}
	if cfg.MonitorGain != 1 {
		t.Errorf("monitor gain = %v, want 1", cfg.MonitorGain)
	}
	if len(cfg.Sources[0].Exclude) == 0 {
		t.Error("game source has no default exclude list")
	}
}

func TestLoadConfigOverridesAndBOM(t *testing.T) {
	path := writeConfig(t, "\xEF\xBB\xBF"+`{
		"sources": [
			{"process": "auto"},
			{"mic": "Headset", "gain": 0.5}
		],
		"target": ["My Cable"],
		"sample_rate": 44100,
		"lang": "en"
	}`)
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Sources[0]; got.Gain != 1 || len(got.Exclude) == 0 {
		t.Errorf("auto source not finalized: %+v", got)
	}
	if got := cfg.Sources[1]; got.Gain != 0.5 || got.Kind() != "mic" {
		t.Errorf("mic source = %+v", got)
	}
	if len(cfg.Target) != 1 || cfg.Target[0] != "My Cable" {
		t.Errorf("target = %v", cfg.Target)
	}
	if len(cfg.Apps) != 3 {
		t.Errorf("apps should keep defaults, got %v", cfg.Apps)
	}
	if cfg.SampleRate != 44100 || cfg.Lang != "en" {
		t.Errorf("rate/lang = %d/%q", cfg.SampleRate, cfg.Lang)
	}
}

func TestLoadConfigRejectsAmbiguousSource(t *testing.T) {
	for name, body := range map[string]string{
		"two kinds": `{"sources": [{"process": "game", "mic": "x"}]}`,
		"no kind":   `{"sources": [{"gain": 2}]}`,
		"bad json":  `{"sources": [`,
	} {
		if _, err := loadConfig(writeConfig(t, body)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestLoadConfigUnknownLangFollowsSystem(t *testing.T) {
	cfg, err := loadConfig(writeConfig(t, `{"lang": "de"}`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Lang != "" {
		t.Errorf("lang = %q, want empty", cfg.Lang)
	}
}

func TestConfigSaveRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "maxmix.json")
	want := defaultConfig()
	want.Sources = append(want.Sources, Source{Match: "Speakers", Gain: 0.7})
	want.Lang = "ru"
	if err := want.save(path); err != nil {
		t.Fatal(err)
	}
	got, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Sources) != 3 || got.Sources[2].Kind() != "device" || got.Sources[2].Gain != 0.7 || got.Lang != "ru" {
		t.Errorf("round trip lost data: %+v", got)
	}
}

func TestSourceKind(t *testing.T) {
	for _, c := range []struct {
		src  Source
		want string
	}{
		{Source{Mic: "m"}, "mic"},
		{Source{Match: "d"}, "device"},
		{Source{Process: "game"}, "game"},
		{Source{Process: "auto"}, "auto"},
		{Source{Process: "Discord.exe"}, "process"},
	} {
		if got := c.src.Kind(); got != c.want {
			t.Errorf("%+v.Kind() = %q, want %q", c.src, got, c.want)
		}
	}
}
