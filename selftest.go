package main

// Self-check: verify that a recording would actually contain sound from every
// configured source, from the mix that recorders capture, and from the
// microphone. Every measurement is an independent WASAPI capture, so it proves
// the OS state rather than the mixer's own bookkeeping.
//
// The microphone is read, never re-routed: maxmix can add it as an input of the
// mix, and monitoring plays it back for the duration of the check only.
// Which device records the mic system-wide is never changed.

import (
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

const (
	selfTestDefault = 6 * time.Second
	signalFloor     = 0.002 // ~ -54 dBFS peak counts as "there is sound"
	pumpPeriod      = 10 * time.Millisecond
	wavMaxSeconds   = 30
)

type checkStatus int

const (
	checkOK checkStatus = iota
	checkWarn
	checkFail
	checkInfo
)

func (c checkStatus) label() string {
	switch c {
	case checkOK:
		return tr("ок")
	case checkWarn:
		return tr("внимание")
	case checkFail:
		return tr("ошибка")
	}
	return tr("справка")
}

func (c checkStatus) mark() string {
	switch c {
	case checkOK:
		return "[ ok ]"
	case checkWarn:
		return "[warn]"
	case checkFail:
		return "[fail]"
	}
	return "[info]"
}

// check is one line of the self-check report.
type check struct {
	Name   string
	Status checkStatus
	Detail string
}

// dbfs formats a peak amplitude as dBFS.
func dbfs(p float32) string {
	if p <= 0 {
		return "-inf dBFS"
	}
	return fmt.Sprintf("%.0f dBFS", 20*math.Log10(float64(p)))
}

// ---- taps -------------------------------------------------------------------

// tap is one independent capture used for measuring (and optionally recording).
type tap struct {
	label   string
	ac      *IAudioClient
	cc      *IAudioCaptureClient
	conv    *converter
	format  sampleFormat
	canon   sampleFormat
	peak    float32
	frames  int
	rec     []float32 // interleaved canonical samples when recording
	keep    bool
	mon     *monitorOut // live playback while the check runs
	monGain float32
	buf     []float32
	err     error
}

func (t *tap) close() {
	t.mon.close()
	t.mon = nil
	if t.ac == nil {
		return
	}
	t.ac.Stop()
	if t.cc != nil {
		t.cc.Release()
	}
	t.ac.Release()
	t.ac, t.cc = nil, nil
}

// start finishes the setup once the client is initialised.
func (t *tap) start(canon sampleFormat) error {
	cc, err := t.ac.CaptureClient()
	if err != nil {
		t.ac.Release()
		t.ac = nil
		return err
	}
	t.cc = cc
	t.canon = canon
	t.conv = newConverter(t.format, canon)
	t.buf = make([]float32, 0, 8192)
	if err := t.ac.Start(); err != nil {
		t.close()
		return err
	}
	return nil
}

// pump drains every pending packet, tracking the peak and recording samples.
func (t *tap) pump() {
	if t.cc == nil || t.err != nil {
		return
	}
	limit := t.canon.Rate * t.canon.Channels * wavMaxSeconds
	for {
		next, err := t.cc.GetNextPacketSize()
		if err != nil {
			t.err = err
			return
		}
		if next == 0 {
			return
		}
		data, frames, flags, err := t.cc.GetBuffer()
		if err != nil {
			t.err = err
			return
		}
		if frames > 0 {
			silent := flags&audclntBufferFlagsSilent != 0 || data == nil
			if silent {
				n := int(frames) * t.canon.Rate / t.format.Rate
				t.buf = t.buf[:0]
				for i := 0; i < n*t.canon.Channels; i++ {
					t.buf = append(t.buf, 0)
				}
			} else {
				raw := unsafe.Slice(data, int(frames)*t.format.BlockAlign())
				t.buf = t.conv.convert(raw, int(frames), t.buf)
				for _, v := range t.buf {
					if v < 0 {
						v = -v
					}
					if v > t.peak {
						t.peak = v
					}
				}
			}
			if t.mon != nil {
				if err := t.mon.push(t.buf, t.monGain); err != nil {
					t.mon.close()
					t.mon = nil
				}
			}
			t.frames += len(t.buf) / t.canon.Channels
			if t.keep && len(t.rec) < limit {
				t.rec = append(t.rec, t.buf...)
			}
		}
		if err := t.cc.ReleaseBuffer(frames); err != nil {
			t.err = err
			return
		}
	}
}

// openCaptureClient initialises a plain (non-loopback) shared capture stream.
func openCaptureClient(dev *IMMDevice) (*IAudioClient, sampleFormat, error) {
	ac, err := dev.ActivateAudioClient()
	if err != nil {
		return nil, sampleFormat{}, err
	}
	wfx, err := ac.GetMixFormat()
	if err != nil {
		ac.Release()
		return nil, sampleFormat{}, err
	}
	defer coTaskMemFree(unsafe.Pointer(wfx))
	f, err := parseFormat(wfx)
	if err != nil {
		ac.Release()
		return nil, f, err
	}
	if err := ac.Initialize(0, captureBufferHNS, wfx); err != nil {
		ac.Release()
		return nil, f, err
	}
	return ac, f, nil
}

// newDeviceTap loopback-captures (or, for a capture endpoint, records) a device.
func newDeviceTap(id, label string, flow uint32, canon sampleFormat, keep bool) (*tap, error) {
	dev, err := openDevice(id)
	if err != nil {
		return nil, err
	}
	defer dev.Release()
	t := &tap{label: label, keep: keep}
	if flow == eRender {
		ac, format, err := openLoopback(dev, label, canon)
		if err != nil {
			return nil, err
		}
		t.ac, t.format = ac, format
	} else {
		ac, format, err := openCaptureClient(dev)
		if err != nil {
			return nil, err
		}
		t.ac, t.format = ac, format
	}
	if err := t.start(canon); err != nil {
		return nil, err
	}
	return t, nil
}

// newProcessTap captures one process tree.
func newProcessTap(pid uint32, label string, canon sampleFormat) (*tap, error) {
	ac, format, ev, err := openProcessLoopback(pid, true, canon)
	if err != nil {
		return nil, err
	}
	syscall.CloseHandle(ev) // polled, not event driven
	t := &tap{label: label, ac: ac, format: format}
	if err := t.start(canon); err != nil {
		return nil, err
	}
	return t, nil
}

// ---- what the mixer would capture ---------------------------------------------

// plannedTap is one input the configuration asks for, resolved to real targets.
type plannedTap struct {
	source   int
	label    string
	deviceID string   // device or microphone endpoint
	flow     uint32   // eRender for device sources, eCapture for microphones
	pids     []uint32 // process sources
	note     string   // why there is nothing to capture
}

// plannedSources resolves the configured sources the same way the mixer does.
func plannedSources(cfg Config) ([]plannedTap, error) {
	table, err := processTable()
	if err != nil {
		return nil, err
	}
	sessions, err := listRenderSessions()
	if err != nil {
		return nil, err
	}
	eps, err := listEndpoints(eRender)
	if err != nil {
		return nil, err
	}
	self := uint32(syscall.Getpid())
	var out []plannedTap
	for i, src := range cfg.Sources {
		p := plannedTap{source: i}
		switch src.Kind() {
		case "device":
			p.label = tr("Устройство") + ": " + src.Match
			if ep, ok := findEndpoint(eps, src.Match); ok {
				p.deviceID, p.label = ep.ID, ep.Name
			} else {
				p.note = tr("устройство не найдено")
			}
		case "mic":
			p.flow = eCapture
			p.label = tr("Микрофон") + ": " + src.Mic
			if caps, err := listEndpoints(eCapture); err == nil {
				if ep, ok := findEndpoint(caps, src.Mic); ok {
					p.deviceID, p.label = ep.ID, ep.Name
				} else {
					p.note = tr("микрофон не найден")
				}
			}
		case "process":
			p.label = src.Process
			p.pids = rootPIDs(table, src.Process)
			if len(p.pids) == 0 {
				p.note = tr("процесс не запущен")
			}
		case "game":
			p.label = tr("Игра (авто)")
			games := newGameDetector(src.Exclude).scan(table, sessions, self)
			for _, g := range sortedGames(games) {
				p.pids = append(p.pids, g.PID)
				p.label = tr("Игра (авто)") + ": " + g.Exe
			}
			if len(p.pids) == 0 {
				p.note = tr("игра не найдена")
			}
		case "auto":
			p.label = tr("Всё, что звучит")
			exclude := map[string]bool{}
			for _, e := range src.Exclude {
				exclude[strings.ToLower(e)] = true
			}
			cand := map[uint32]bool{}
			for _, ss := range sessions {
				if ss.State == audioSessionStateExpired || ss.PID == self {
					continue
				}
				if pr, ok := table[ss.PID]; ok && !exclude[strings.ToLower(pr.Exe)] {
					cand[ss.PID] = true
				}
			}
			for pid := range cand {
				if !hasAncestorIn(table, pid, cand) {
					p.pids = append(p.pids, pid)
				}
			}
			sort.Slice(p.pids, func(a, b int) bool { return p.pids[a] < p.pids[b] })
			if len(p.pids) == 0 {
				p.note = tr("нет процессов со звуком")
			}
		}
		out = append(out, p)
	}
	return out, nil
}

// ---- the self-check ------------------------------------------------------------

// runSelfTest measures every input, the mix and the microphone for dur and
// returns a report. When wavDir is not empty it also writes selftest-mix.wav
// and selftest-mic.wav there.
func runSelfTest(cfg Config, dur time.Duration, wavDir string) []check {
	if err := initCOM(); err != nil {
		return []check{{tr("COM"), checkFail, err.Error()}}
	}
	canon := sampleFormat{Rate: cfg.SampleRate, Channels: 2, Bits: 32, Float: true}
	var out []check

	// The device that recorders capture.
	target, err := targetID(cfg)
	if err != nil {
		return append(out, check{tr("Виртуальное устройство"), checkFail, err.Error()})
	}
	out = append(out, check{tr("Виртуальное устройство"), checkOK, target.Name})

	var taps []*tap
	defer func() {
		for _, t := range taps {
			t.close()
		}
	}()

	mix, err := newDeviceTap(target.ID, target.Name, eRender, canon, wavDir != "")
	if err != nil {
		out = append(out, check{tr("Звук в записи"), checkFail, tr("не удалось слушать выход: ") + err.Error()})
	} else {
		taps = append(taps, mix)
	}

	// Microphones: the default capture endpoints every recorder uses.
	defaults, _ := currentDefaults()
	capEps, _ := listEndpoints(eCapture)
	renderEps, _ := listEndpoints(eRender)
	micIDs := []string{}
	for _, id := range defaults.Capture {
		if id != "" && !containsFold(micIDs, id) {
			micIDs = append(micIDs, id)
		}
	}
	var mics []*tap
	for _, id := range micIDs {
		name := id
		for _, e := range capEps {
			if e.ID == id {
				name = e.Name
			}
		}
		t, err := newDeviceTap(id, name, eCapture, canon, wavDir != "" && len(mics) == 0)
		if err != nil {
			out = append(out, check{tr("Микрофон"), checkWarn, name + ": " + err.Error()})
			continue
		}
		mics = append(mics, t)
		taps = append(taps, t)
	}

	// Monitoring: play the microphone back for the duration of the check only,
	// so the speaker can hear themselves while testing.
	if cfg.Monitor != "" && len(mics) > 0 {
		gain := cfg.MonitorGain
		if gain <= 0 {
			gain = 1
		}
		if mon, err := openMonitor(cfg.Monitor, canon); err != nil {
			out = append(out, check{tr("Прослушивание микрофона"), checkWarn, err.Error()})
		} else {
			mics[0].mon, mics[0].monGain = mon, gain
			out = append(out, check{tr("Прослушивание микрофона"), checkInfo,
				fmt.Sprintf("%s %s, %.0f%%", tr("только на время проверки:"), mon.name, gain*100)})
		}
	}

	// Every configured input, captured independently of the mixer.
	planned, perr := plannedSources(cfg)
	if perr != nil {
		out = append(out, check{tr("Источники"), checkFail, perr.Error()})
	}
	type srcTaps struct {
		plan plannedTap
		taps []*tap
	}
	var sources []*srcTaps
	for _, p := range planned {
		st := &srcTaps{plan: p}
		switch {
		case p.deviceID != "":
			if t, err := newDeviceTap(p.deviceID, p.label, p.flow, canon, false); err == nil {
				st.taps = append(st.taps, t)
				taps = append(taps, t)
			} else {
				st.plan.note = err.Error()
			}
		default:
			for _, pid := range p.pids {
				label := fmt.Sprintf("%s (%d)", p.label, pid)
				if t, err := newProcessTap(pid, label, canon); err == nil {
					st.taps = append(st.taps, t)
					taps = append(taps, t)
				} else {
					st.plan.note = err.Error()
				}
			}
		}
		sources = append(sources, st)
	}

	// The microphone NVIDIA App records, captured on its own.
	nv, nvOK := readNvidiaConfig()
	var nvMic *tap
	var nvMicErr error
	if nvOK {
		if nvMic, nvMicErr = nvidiaMicTap(nv, canon); nvMic != nil {
			taps = append(taps, nvMic)
		}
	}

	// Measure.
	deadline := time.Now().Add(dur)
	for time.Now().Before(deadline) {
		for _, t := range taps {
			t.pump()
		}
		time.Sleep(pumpPeriod)
	}
	for _, t := range taps {
		t.pump()
	}

	// Evaluate: the mix a recorder would get.
	if mix != nil {
		st, detail := checkFail, tr("тишина: запись будет без звука")
		if mix.peak >= signalFloor {
			st, detail = checkOK, tr("есть звук, пик ")+dbfs(mix.peak)
		}
		if mix.err != nil {
			st, detail = checkFail, mix.err.Error()
		}
		if mute, level, err := endpointVolume(target.ID); err == nil && (mute || level < 0.02) && st == checkOK {
			st = checkWarn
			detail += "; " + tr("но устройство приглушено в Windows")
			_ = level
		}
		out = append(out, check{tr("Звук в записи"), st, detail})
	}

	// Evaluate: each configured input.
	var live, silent []string
	for _, st := range sources {
		var peak float32
		for _, t := range st.taps {
			if t.peak > peak {
				peak = t.peak
			}
		}
		name := st.plan.label
		switch {
		case len(st.taps) == 0:
			note := st.plan.note
			if note == "" {
				note = tr("нечего захватывать")
			}
			out = append(out, check{tr("Вход: ") + name, checkWarn, note})
			silent = append(silent, name)
		case peak >= signalFloor:
			out = append(out, check{tr("Вход: ") + name, checkOK, fmt.Sprintf("%s, %s %d", tr("есть звук, пик ")+dbfs(peak), tr("захватов:"), len(st.taps))})
			live = append(live, name)
		default:
			out = append(out, check{tr("Вход: ") + name, checkWarn, tr("захват работает, но источник молчит")})
			silent = append(silent, name)
		}
	}
	multi := check{Name: tr("Несколько источников")}
	switch {
	case len(live) >= 2:
		multi.Status, multi.Detail = checkOK, fmt.Sprintf("%d: %s", len(live), strings.Join(live, ", "))
	case len(live) == 1:
		multi.Status = checkWarn
		multi.Detail = fmt.Sprintf("%s %s; %s %s", tr("звук только с"), live[0], tr("молчат:"), strings.Join(silent, ", "))
	default:
		multi.Status, multi.Detail = checkFail, tr("ни один источник не дал звука")
	}
	out = append(out, multi)

	// Evaluate: microphone. Recorders capture it themselves.
	if len(mics) == 0 {
		out = append(out, check{tr("Микрофон"), checkWarn, tr("устройство записи по умолчанию не найдено")})
	} else {
		for i, t := range mics {
			st, detail := checkWarn, tr("тишина: скажите что-нибудь во время проверки")
			if t.peak >= signalFloor {
				st, detail = checkOK, tr("есть звук, пик ")+dbfs(t.peak)
			}
			if mute, level, err := endpointVolume(micIDs[i]); err == nil {
				switch {
				case mute:
					st, detail = checkFail, tr("микрофон выключен (mute): голоса в записи не будет")
				case level < 0.02:
					st, detail = checkFail, fmt.Sprintf("%s %.0f%%", tr("громкость микрофона почти на нуле:"), level*100)
				default:
					detail = fmt.Sprintf("%s, %s %.0f%%", detail, tr("громкость"), level*100)
				}
			}
			out = append(out, check{tr("Микрофон: ") + t.label, st, detail})
		}
	}
	micInMix := false
	for _, src := range cfg.Sources {
		if src.Kind() == "mic" {
			micInMix = true
		}
	}
	if micInMix {
		out = append(out, check{tr("Микрофон и микшер"), checkInfo, tr("микрофон добавлен во вход микса, поэтому голос попадёт в запись даже с выключенным микрофоном в рекордере")})
	} else {
		out = append(out, check{tr("Микрофон и микшер"), checkInfo, tr("микрофон пишут Steam и NVIDIA сами; maxmix его не трогает и не переключает")})
	}

	// Evaluate: obvious misconfigurations.
	if id := defaults.Capture[eConsole]; id != "" {
		name := id
		for _, e := range capEps {
			if e.ID == id {
				name = e.Name
			}
		}
		st, detail := checkOK, name
		if looksVirtual(name) {
			st = checkWarn
			detail = name + ": " + tr("это виртуальный кабель, а не микрофон")
		}
		out = append(out, check{tr("Устройство записи по умолчанию"), st, detail})
	}
	if id := defaults.Render[eConsole]; id != "" {
		name := id
		for _, e := range renderEps {
			if e.ID == id {
				name = e.Name
			}
		}
		st, detail := checkOK, name
		if id == target.ID {
			st = checkWarn
			detail = name + ": " + tr("совпадает с выходом микшера, наушники будут молчать")
		}
		out = append(out, check{tr("Устройство вывода по умолчанию"), st, detail})
	}

	// Evaluate: recorder routing.
	if rows, err := routeRows(cfg.Apps); err == nil {
		var okApps, pending []string
		for _, r := range rows {
			switch {
			case !r.Running:
				pending = append(pending, r.App+" ("+tr("не запущена")+")")
			case r.Error != "":
				pending = append(pending, r.App)
			case strings.EqualFold(r.Device, target.Name):
				okApps = append(okApps, r.App)
			default:
				pending = append(pending, r.App+" -> "+r.Device)
			}
		}
		st := checkOK
		detail := strings.Join(okApps, ", ")
		if len(pending) > 0 {
			st = checkWarn
			if detail != "" {
				detail += "; "
			}
			detail += tr("не на выходе: ") + strings.Join(pending, ", ")
		}
		out = append(out, check{tr("Программы записи"), st, detail})
	}

	out = append(out, steamChecks(target, micInMix)...)

	// NVIDIA records a microphone track itself; compare it with what the mix
	// already carries.
	var mixMicIDs []string
	voiceHeard := false
	for _, t := range mics {
		voiceHeard = voiceHeard || t.peak >= signalFloor
	}
	for _, st := range sources {
		if st.plan.flow != eCapture || st.plan.deviceID == "" {
			continue
		}
		mixMicIDs = append(mixMicIDs, st.plan.deviceID)
		for _, t := range st.taps {
			voiceHeard = voiceHeard || t.peak >= signalFloor
		}
	}
	out = append(out, nvidiaChecks(nv, nvOK, nvMic, nvMicErr, micInMix, mixMicIDs, voiceHeard)...)

	// Write the samples so the result can be listened to.
	if wavDir != "" {
		var written []string
		if mix != nil && len(mix.rec) > 0 {
			p := filepath.Join(wavDir, "selftest-mix.wav")
			if err := writeWAV(p, mix.rec, canon); err == nil {
				written = append(written, filepath.Base(p))
			}
		}
		if len(mics) > 0 && len(mics[0].rec) > 0 {
			p := filepath.Join(wavDir, "selftest-mic.wav")
			if err := writeWAV(p, mics[0].rec, canon); err == nil {
				written = append(written, filepath.Base(p))
			}
		}
		if len(written) > 0 {
			out = append(out, check{tr("Записанные образцы"), checkInfo, wavDir + ": " + strings.Join(written, ", ")})
		}
	}
	return out
}

// looksVirtual reports whether a capture endpoint name is a virtual cable
// rather than a real microphone.
func looksVirtual(name string) bool {
	l := strings.ToLower(name)
	for _, s := range []string{"cable output", "voicemeeter out", "virtual", "loop-back", "loopback"} {
		if strings.Contains(l, s) {
			return true
		}
	}
	return false
}

// writeWAV stores interleaved float32 samples as 16-bit PCM.
func writeWAV(path string, samples []float32, f sampleFormat) error {
	data := make([]byte, 0, len(samples)*2)
	for _, v := range samples {
		if v > 1 {
			v = 1
		} else if v < -1 {
			v = -1
		}
		s := int16(v * 32767)
		data = append(data, byte(uint16(s)), byte(uint16(s)>>8))
	}
	var hdr []byte
	put32 := func(v uint32) { hdr = binary.LittleEndian.AppendUint32(hdr, v) }
	put16 := func(v uint16) { hdr = binary.LittleEndian.AppendUint16(hdr, v) }
	hdr = append(hdr, "RIFF"...)
	put32(uint32(36 + len(data)))
	hdr = append(hdr, "WAVEfmt "...)
	put32(16)
	put16(waveFormatPCM)
	put16(uint16(f.Channels))
	put32(uint32(f.Rate))
	put32(uint32(f.Rate * f.Channels * 2))
	put16(uint16(f.Channels * 2))
	put16(16)
	hdr = append(hdr, "data"...)
	put32(uint32(len(data)))
	return os.WriteFile(path, append(hdr, data...), 0o644)
}

// cmdSelfTest runs the self-check from the command line.
func cmdSelfTest(cfg Config, args []string) error {
	dur := selfTestDefault
	if len(args) > 0 {
		var secs float64
		if _, err := fmt.Sscanf(args[0], "%f", &secs); err == nil && secs > 0 {
			dur = time.Duration(secs * float64(time.Second))
		}
	}
	dir := filepath.Join(os.Getenv("LOCALAPPDATA"), "maxmix")
	os.MkdirAll(dir, 0o755)
	fmt.Printf("measuring %s — play game sound and speak into the microphone\n\n", dur)
	rows := runSelfTest(cfg, dur, dir)
	worst := checkOK
	for _, r := range rows {
		fmt.Printf("  %s %-38s %s\n", r.Status.mark(), r.Name, r.Detail)
		if r.Status == checkFail || (r.Status == checkWarn && worst == checkOK) {
			worst = r.Status
		}
	}
	if worst == checkFail {
		return fmt.Errorf("self-check failed")
	}
	return nil
}
