package main

import "testing"

func TestVdfPair(t *testing.T) {
	for _, c := range []struct {
		line, key, val string
		ok             bool
	}{
		{`"Recording_Audio_Option"		"2"`, "Recording_Audio_Option", "2", true},
		{`"name"	"C:\\Games\\game.exe"`, "name", `C:\\Games\\game.exe`, true},
		{`"empty"	""`, "empty", "", true},
		{`"GameRecording"`, "", "", false},
		{`{`, "", "", false},
		{`no quotes`, "", "", false},
	} {
		key, val, ok := vdfPair(c.line)
		if key != c.key || val != c.val || ok != c.ok {
			t.Errorf("vdfPair(%q) = %q, %q, %v; want %q, %q, %v", c.line, key, val, ok, c.key, c.val, c.ok)
		}
	}
}

const localConfigSample = `"UserLocalConfigStore"
{
	"friends"
	{
		"name"		"not this one"
	}
	"GameRecording"
	{
		"BackgroundRecordMode"		"1"
		"Recording_Audio_Option"		"1"
		"RecordingAudioApps"
		{
			"0"
			{
				"name"		"Discord.exe"
			}
		}
		"MicEnabled"		"0"
	}
	"Other"
	{
		"Recording_Audio_Option"		"0"
	}
}
`

func TestParseGameRecording(t *testing.T) {
	cfg := parseGameRecording(localConfigSample)
	want := map[string]string{
		"BackgroundRecordMode":   "1",
		"Recording_Audio_Option": "1",
		"MicEnabled":             "0",
	}
	if len(cfg.settings) != len(want) {
		t.Fatalf("settings = %v, want %v", cfg.settings, want)
	}
	for k, v := range want {
		if cfg.settings[k] != v {
			t.Errorf("settings[%q] = %q, want %q", k, cfg.settings[k], v)
		}
	}
	if len(cfg.extra) != 1 || cfg.extra[0] != "Discord.exe" {
		t.Errorf("extra = %v, want [Discord.exe]", cfg.extra)
	}
}

func TestParseGameRecordingMissingBlock(t *testing.T) {
	cfg := parseGameRecording("\"UserLocalConfigStore\"\n{\n}\n")
	if cfg.settings != nil {
		t.Errorf("expected empty config, got %+v", cfg)
	}
}
