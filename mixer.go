package main

import (
	"context"
	"fmt"
	"log"
	"math"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"
)

const (
	tickPeriod       = 5 * time.Millisecond
	renderBufferHNS  = int64(100 * 10000) // 100 ms in 100 ns units
	captureBufferHNS = int64(200 * 10000) // 200 ms
	// Backlog thresholds in ms: above high we trim to low (clock drift / hiccups).
	backlogHighMS = 300
	backlogLowMS  = 60

	retryDelay = 3 * time.Second // after a source or the target fails
	scanPeriod = 2 * time.Second // how often process sources are re-evaluated
)

// ---- ring buffer ------------------------------------------------------------

// ring is a mutex-protected FIFO of interleaved float32 frames with a peak meter.
type ring struct {
	mu       sync.Mutex
	buf      []float32
	channels int
	peak     uint32 // float32 bits, max |sample| since last takePeak
}

func newRing(channels int) *ring { return &ring{channels: channels} }

func (r *ring) push(samples []float32) {
	var mx float32
	for _, v := range samples {
		if v < 0 {
			v = -v
		}
		if v > mx {
			mx = v
		}
	}
	r.bumpPeak(mx)
	r.mu.Lock()
	r.buf = append(r.buf, samples...)
	r.mu.Unlock()
}

func (r *ring) bumpPeak(v float32) {
	for {
		old := atomic.LoadUint32(&r.peak)
		if v <= math.Float32frombits(old) || atomic.CompareAndSwapUint32(&r.peak, old, math.Float32bits(v)) {
			return
		}
	}
}

// takePeak returns the peak since the previous call and resets it.
func (r *ring) takePeak() float32 {
	return math.Float32frombits(atomic.SwapUint32(&r.peak, 0))
}

// pop copies up to frames frames into dst and returns how many were written.
func (r *ring) pop(dst []float32, frames int) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := len(r.buf) / r.channels
	if n > frames {
		n = frames
	}
	copy(dst, r.buf[:n*r.channels])
	r.buf = r.buf[n*r.channels:]
	if len(r.buf) == 0 {
		r.buf = r.buf[:0]
	}
	return n
}

func (r *ring) frames() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.buf) / r.channels
}

// trim drops the oldest frames so that at most keep frames remain.
func (r *ring) trim(keep int) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := len(r.buf) / r.channels
	if n <= keep {
		return 0
	}
	drop := n - keep
	r.buf = append(r.buf[:0], r.buf[drop*r.channels:]...)
	return drop
}

// ---- format conversion ------------------------------------------------------

// converter turns captured frames of format src into the canonical
// float32/dst.Channels/dst.Rate layout, resampling linearly if needed.
type converter struct {
	src, dst sampleFormat
	pos      float64   // fractional read position into the current input block
	last     []float32 // last input frame kept for interpolation across blocks
	a, b     []float32
}

func newConverter(src, dst sampleFormat) *converter {
	return &converter{
		src: src, dst: dst,
		last: make([]float32, dst.Channels),
		a:    make([]float32, dst.Channels),
		b:    make([]float32, dst.Channels),
	}
}

func (c *converter) sample(b []byte) float32 {
	switch {
	case c.src.Float && c.src.Bits == 32:
		return math.Float32frombits(uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24)
	case c.src.Bits == 16:
		return float32(int16(uint16(b[0])|uint16(b[1])<<8)) / 32768
	case c.src.Bits == 24:
		v := int32(uint32(b[0])<<8|uint32(b[1])<<16|uint32(b[2])<<24) >> 8
		return float32(v) / 8388608
	case c.src.Bits == 32:
		v := int32(uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24)
		return float32(v) / 2147483648
	}
	return 0
}

// frame reads input frame i mapped to the dst channel layout.
func (c *converter) frame(data []byte, i int, out []float32) {
	base := i * c.src.BlockAlign()
	bps := c.src.Bits / 8
	for ch := 0; ch < c.dst.Channels; ch++ {
		sch := ch
		if sch >= c.src.Channels {
			sch = c.src.Channels - 1 // mono -> duplicate to both channels
		}
		off := base + sch*bps
		out[ch] = c.sample(data[off : off+bps])
	}
}

// convert appends converted frames to out and returns it.
func (c *converter) convert(data []byte, frames int, out []float32) []float32 {
	out = out[:0]
	if c.src.Rate == c.dst.Rate {
		for i := 0; i < frames; i++ {
			c.frame(data, i, c.a)
			out = append(out, c.a...)
		}
		return out
	}
	// Linear resampling. Position -1 refers to c.last (previous block's tail).
	step := float64(c.src.Rate) / float64(c.dst.Rate)
	for c.pos < float64(frames) {
		i0 := int(math.Floor(c.pos))
		frac := float32(c.pos - float64(i0))
		if i0 < 0 {
			copy(c.a, c.last)
		} else {
			c.frame(data, i0, c.a)
		}
		if i0+1 < frames {
			c.frame(data, i0+1, c.b)
		} else {
			copy(c.b, c.a)
		}
		for ch := range c.b {
			c.b[ch] = c.a[ch] + (c.b[ch]-c.a[ch])*frac
		}
		out = append(out, c.b...)
		c.pos += step
	}
	if frames > 0 {
		c.frame(data, frames-1, c.last)
	}
	c.pos -= float64(frames)
	return out
}

// ---- mixer state ------------------------------------------------------------

// capture is one live input, as shown in the UI.
type capture struct {
	Key    string  `json:"key"`
	Kind   string  `json:"kind"`   // device | process | game | auto
	Label  string  `json:"label"`  // device or executable name
	PID    uint32  `json:"pid"`    // 0 for devices
	Source int     `json:"source"` // index into Config.Sources
	Peak   float32 `json:"peak"`
	ring   *ring
}

// mixer owns the set of live inputs; sources add and remove themselves, the
// render loop sums whatever is present at each tick. Apply swaps the config
// at runtime.
type mixer struct {
	cfgPath string
	canon   sampleFormat

	mu        sync.Mutex
	cfg       Config
	captures  map[string]*capture
	treeRoots map[uint32]bool   // PIDs captured with tree mode by explicit/game sources
	games     map[uint32]string // detected games (root pid -> exe)
	target    string            // current output endpoint name
	outPeak   *ring             // peak meter of the mix
	restart   chan struct{}     // closed to restart all loops with the new cfg
}

func newMixer(cfg Config, cfgPath string) *mixer {
	m := &mixer{cfgPath: cfgPath, restart: make(chan struct{})}
	m.setConfig(cfg)
	return m
}

func (m *mixer) setConfig(cfg Config) {
	m.mu.Lock()
	m.cfg = cfg
	m.canon = sampleFormat{Rate: cfg.SampleRate, Channels: 2, Bits: 32, Float: true}
	m.captures = map[string]*capture{}
	m.treeRoots = map[uint32]bool{}
	m.games = map[uint32]string{}
	m.outPeak = newRing(m.canon.Channels)
	m.mu.Unlock()
}

func (m *mixer) config() Config {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cfg
}

func (m *mixer) addCapture(c *capture) *ring {
	c.ring = newRing(m.canon.Channels)
	m.mu.Lock()
	m.captures[c.Key] = c
	m.mu.Unlock()
	return c.ring
}

func (m *mixer) removeCapture(key string) {
	m.mu.Lock()
	delete(m.captures, key)
	m.mu.Unlock()
}

func (m *mixer) rings() []*ring {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*ring, 0, len(m.captures))
	for _, c := range m.captures {
		out = append(out, c.ring)
	}
	return out
}

// status returns a snapshot of live inputs with their peak levels.
func (m *mixer) status() (caps []capture, games []gameInfo, target string, outPeak float32) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range m.captures {
		cc := *c
		cc.Peak = c.ring.takePeak()
		cc.ring = nil
		caps = append(caps, cc)
	}
	sort.Slice(caps, func(i, j int) bool { return caps[i].Key < caps[j].Key })
	return caps, sortedGames(m.games), m.target, m.outPeak.takePeak()
}

func (m *mixer) setTreeRoot(pid uint32, on bool) {
	m.mu.Lock()
	if on {
		m.treeRoots[pid] = true
	} else {
		delete(m.treeRoots, pid)
	}
	m.mu.Unlock()
}

func (m *mixer) treeRootSet() map[uint32]bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[uint32]bool, len(m.treeRoots))
	for k := range m.treeRoots {
		out[k] = true
	}
	return out
}

func (m *mixer) setGames(g map[uint32]string) {
	m.mu.Lock()
	m.games = g
	m.mu.Unlock()
}

func (m *mixer) setTarget(name string) {
	m.mu.Lock()
	m.target = name
	m.mu.Unlock()
}

// Apply validates, saves and hot-reloads a new configuration.
func (m *mixer) Apply(cfg Config) error {
	cfg, err := finalize(cfg)
	if err != nil {
		return err
	}
	if m.cfgPath != "" {
		if err := cfg.save(m.cfgPath); err != nil {
			return fmt.Errorf("save %s: %w", m.cfgPath, err)
		}
	}
	m.mu.Lock()
	old := m.restart
	m.restart = make(chan struct{})
	m.mu.Unlock()
	m.setConfig(cfg)
	close(old) // every loop watching the old channel exits; runMixer restarts them
	log.Printf("config applied: sources: %s; target: %v", describeSources(cfg), cfg.Target)
	return nil
}

// ---- capture -----------------------------------------------------------------

// drainPackets moves every pending capture packet into r.
func drainPackets(cc *IAudioCaptureClient, conv *converter, format, canon sampleFormat, gain float32, r *ring, out *[]float32) error {
	for {
		next, err := cc.GetNextPacketSize()
		if err != nil {
			return fmt.Errorf("GetNextPacketSize: %w", err)
		}
		if next == 0 {
			return nil
		}
		data, frames, bflags, err := cc.GetBuffer()
		if err != nil {
			return fmt.Errorf("GetBuffer: %w", err)
		}
		if frames > 0 {
			buf := (*out)[:0]
			if bflags&audclntBufferFlagsSilent != 0 || data == nil {
				n := int(frames) * canon.Rate / format.Rate
				for i := 0; i < n*canon.Channels; i++ {
					buf = append(buf, 0)
				}
			} else {
				raw := unsafe.Slice(data, int(frames)*format.BlockAlign())
				buf = conv.convert(raw, int(frames), buf)
				if gain != 1 {
					for i := range buf {
						buf[i] *= gain
					}
				}
			}
			*out = buf
			r.push(buf)
		}
		if err := cc.ReleaseBuffer(frames); err != nil {
			return fmt.Errorf("ReleaseBuffer: %w", err)
		}
	}
}

func (m *mixer) checkBacklog(name string, r *ring) {
	if r.frames() > backlogHighMS*m.canon.Rate/1000 {
		dropped := r.trim(backlogLowMS * m.canon.Rate / 1000)
		log.Printf("[%s] backlog too high, dropped %d frames", name, dropped)
	}
}

// openLoopback initialises a device loopback client, preferring the canonical
// format (engine converts) and falling back to the device mix format.
func openLoopback(dev *IMMDevice, name string, canon sampleFormat) (*IAudioClient, sampleFormat, error) {
	ac, err := dev.ActivateAudioClient()
	if err != nil {
		return nil, canon, fmt.Errorf("Activate: %w", err)
	}
	flags := uint32(audclntStreamFlagsLoopback | audclntStreamFlagsAutoConvertPCM | audclntStreamFlagsSRCDefaultQuality)
	if err := ac.Initialize(flags, captureBufferHNS, floatFormat(canon.Rate, canon.Channels).wfx()); err == nil {
		return ac, canon, nil
	} else {
		log.Printf("[%s] canonical loopback format rejected (%v); using mix format", name, err)
	}
	ac.Release()
	if ac, err = dev.ActivateAudioClient(); err != nil {
		return nil, canon, fmt.Errorf("Activate: %w", err)
	}
	wfx, err := ac.GetMixFormat()
	if err != nil {
		ac.Release()
		return nil, canon, fmt.Errorf("GetMixFormat: %w", err)
	}
	defer coTaskMemFree(unsafe.Pointer(wfx))
	format, err := parseFormat(wfx)
	if err != nil {
		ac.Release()
		return nil, canon, err
	}
	if err := ac.Initialize(audclntStreamFlagsLoopback, captureBufferHNS, wfx); err != nil {
		ac.Release()
		return nil, canon, fmt.Errorf("Initialize loopback: %w", err)
	}
	return ac, format, nil
}

// runDeviceCapture loopback-captures one render endpoint into r until ctx is
// done or WASAPI fails (device removed).
func (m *mixer) runDeviceCapture(ctx context.Context, ep Endpoint, gain float32, r *ring) error {
	if err := initCOM(); err != nil {
		return err
	}
	dev, err := openDevice(ep.ID)
	if err != nil {
		return err
	}
	defer dev.Release()
	ac, format, err := openLoopback(dev, ep.Name, m.canon)
	if err != nil {
		return err
	}
	defer ac.Release()
	cc, err := ac.CaptureClient()
	if err != nil {
		return fmt.Errorf("IAudioCaptureClient: %w", err)
	}
	defer cc.Release()
	if err := ac.Start(); err != nil {
		return fmt.Errorf("Start: %w", err)
	}
	defer ac.Stop()
	log.Printf("[%s] device loopback started (%s)", ep.Name, format)

	conv := newConverter(format, m.canon)
	out := make([]float32, 0, 8192)
	ticker := time.NewTicker(tickPeriod)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
		if err := drainPackets(cc, conv, format, m.canon, gain, r, &out); err != nil {
			return err
		}
		m.checkBacklog(ep.Name, r)
	}
}

// runProcessCapture captures one process tree (event driven) into r.
func (m *mixer) runProcessCapture(ctx context.Context, pid uint32, name string, tree bool, gain float32, r *ring) error {
	if err := initCOM(); err != nil {
		return err
	}
	ac, format, ev, err := openProcessLoopback(pid, tree, m.canon)
	if err != nil {
		return err
	}
	defer ac.Release()
	defer syscall.CloseHandle(ev)
	cc, err := ac.CaptureClient()
	if err != nil {
		return fmt.Errorf("IAudioCaptureClient: %w", err)
	}
	defer cc.Release()
	if err := ac.Start(); err != nil {
		return fmt.Errorf("Start: %w", err)
	}
	defer ac.Stop()
	log.Printf("[%s pid %d] process loopback started (%s, tree=%v)", name, pid, format, tree)

	conv := newConverter(format, m.canon)
	out := make([]float32, 0, 8192)
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		rc, err := syscall.WaitForSingleObject(ev, 200)
		if err != nil {
			return fmt.Errorf("WaitForSingleObject: %w", err)
		}
		if rc != waitObject0 && rc != waitTimeout {
			return fmt.Errorf("WaitForSingleObject: %#x", rc)
		}
		if err := drainPackets(cc, conv, format, m.canon, gain, r, &out); err != nil {
			return err
		}
		m.checkBacklog(name, r)
	}
}

// ---- source supervisors -------------------------------------------------------

// deviceLoop keeps a device source captured, waiting for the endpoint to
// (re)appear when the headset is off.
func (m *mixer) deviceLoop(ctx context.Context, idx int, s Source) {
	warned := false
	for ctx.Err() == nil {
		eps, err := listEndpoints(eRender)
		var ep Endpoint
		ok := false
		if err == nil {
			ep, ok = findEndpoint(eps, s.Match)
		}
		if !ok {
			if !warned {
				log.Printf("source %q not found, waiting (headset off?)", s.Match)
				warned = true
			}
			sleepCtx(ctx, retryDelay)
			continue
		}
		warned = false
		key := "dev:" + ep.ID
		r := m.addCapture(&capture{Key: key, Kind: "device", Label: ep.Name, Source: idx})
		err = m.runDeviceCapture(ctx, ep, s.Gain, r)
		m.removeCapture(key)
		if ctx.Err() != nil {
			return
		}
		log.Printf("[%s] capture stopped: %v; retrying in %s", ep.Name, err, retryDelay)
		sleepCtx(ctx, retryDelay)
	}
}

// procCapture is one running process loopback capture.
type procCapture struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// procSet supervises a set of process captures keyed by PID.
type procSet struct {
	m        *mixer
	idx      int
	kind     string
	running  map[uint32]*procCapture
	failMu   sync.Mutex
	lastFail map[uint32]time.Time
	gain     float32
}

func newProcSet(m *mixer, idx int, kind string, gain float32) *procSet {
	return &procSet{m: m, idx: idx, kind: kind, running: map[uint32]*procCapture{}, lastFail: map[uint32]time.Time{}, gain: gain}
}

// sync starts captures for wanted PIDs not yet running and stops the rest.
func (ps *procSet) sync(ctx context.Context, wanted map[uint32]string) {
	for pid, pc := range ps.running {
		select {
		case <-pc.done: // exited on its own (error or process gone)
			delete(ps.running, pid)
			continue
		default:
		}
		if _, ok := wanted[pid]; !ok {
			pc.cancel()
			<-pc.done
			delete(ps.running, pid)
		}
	}
	for pid, name := range wanted {
		if _, ok := ps.running[pid]; ok {
			continue
		}
		ps.failMu.Lock()
		t, failed := ps.lastFail[pid]
		ps.failMu.Unlock()
		if failed && time.Since(t) < retryDelay {
			continue
		}
		ps.start(ctx, pid, name)
	}
}

func (ps *procSet) start(ctx context.Context, pid uint32, name string) {
	cctx, cancel := context.WithCancel(ctx)
	pc := &procCapture{cancel: cancel, done: make(chan struct{})}
	ps.running[pid] = pc
	ps.m.setTreeRoot(pid, true)
	key := fmt.Sprintf("proc:%d", pid)
	go func() {
		defer close(pc.done)
		r := ps.m.addCapture(&capture{Key: key, Kind: ps.kind, Label: name, PID: pid, Source: ps.idx})
		err := ps.m.runProcessCapture(cctx, pid, name, true, ps.gain, r)
		ps.m.removeCapture(key)
		ps.m.setTreeRoot(pid, false)
		if cctx.Err() == nil {
			log.Printf("[%s pid %d] capture stopped: %v", name, pid, err)
			ps.failMu.Lock()
			ps.lastFail[pid] = time.Now()
			ps.failMu.Unlock()
		}
	}()
}

func (ps *procSet) stopAll() {
	for pid, pc := range ps.running {
		pc.cancel()
		<-pc.done
		delete(ps.running, pid)
	}
}

// processLoop captures every process tree rooted at an executable named s.Process.
func (m *mixer) processLoop(ctx context.Context, idx int, s Source) {
	ps := newProcSet(m, idx, "process", s.Gain)
	defer ps.stopAll()
	warned := false
	for ctx.Err() == nil {
		table, err := processTable()
		if err != nil {
			log.Printf("process table: %v", err)
			sleepCtx(ctx, retryDelay)
			continue
		}
		wanted := map[uint32]string{}
		for _, pid := range rootPIDs(table, s.Process) {
			wanted[pid] = s.Process
		}
		if len(wanted) == 0 && !warned {
			log.Printf("[%s] not running; will capture when it starts", s.Process)
			warned = true
		} else if len(wanted) > 0 {
			warned = false
		}
		ps.sync(ctx, wanted)
		sleepCtx(ctx, scanPeriod)
	}
}

// autoLoop captures every process that has an audio session, minus exclusions,
// minus anything already covered by an explicit process source or by another
// captured ancestor (tree captures would otherwise duplicate the child).
func (m *mixer) autoLoop(ctx context.Context, idx int, s Source) {
	ps := newProcSet(m, idx, "auto", s.Gain)
	defer ps.stopAll()
	exclude := map[string]bool{}
	for _, e := range s.Exclude {
		exclude[strings.ToLower(e)] = true
	}
	self := uint32(syscall.Getpid())
	for ctx.Err() == nil {
		sessions, err := listRenderSessions()
		table, err2 := processTable()
		if err != nil || err2 != nil {
			log.Printf("auto scan: %v %v", err, err2)
			sleepCtx(ctx, retryDelay)
			continue
		}
		candidates := map[uint32]bool{}
		for _, ss := range sessions {
			if ss.State == audioSessionStateExpired || ss.PID == self {
				continue
			}
			p, alive := table[ss.PID]
			if !alive || exclude[strings.ToLower(p.Exe)] {
				continue
			}
			candidates[ss.PID] = true
		}
		covered := m.treeRootSet()
		for pid := range candidates {
			covered[pid] = true
		}
		wanted := map[uint32]string{}
		for pid := range candidates {
			if hasAncestorIn(table, pid, covered) {
				continue
			}
			wanted[pid] = table[pid].Exe
		}
		ps.sync(ctx, wanted)
		sleepCtx(ctx, scanPeriod)
	}
}

// gameLoop captures whatever the detector decides is a game.
func (m *mixer) gameLoop(ctx context.Context, idx int, s Source) {
	ps := newProcSet(m, idx, "game", s.Gain)
	defer ps.stopAll()
	defer m.setGames(nil)
	det := newGameDetector(s.Exclude)
	self := uint32(syscall.Getpid())
	var last string
	for ctx.Err() == nil {
		sessions, err := listRenderSessions()
		table, err2 := processTable()
		if err != nil || err2 != nil {
			log.Printf("game scan: %v %v", err, err2)
			sleepCtx(ctx, retryDelay)
			continue
		}
		wanted := det.scan(table, sessions, self)
		m.setGames(wanted)
		if desc := fmt.Sprint(sortedGames(wanted)); desc != last {
			last = desc
			if len(wanted) == 0 {
				log.Print("game: none detected")
			} else {
				log.Printf("game: detected %s", desc)
			}
		}
		ps.sync(ctx, wanted)
		sleepCtx(ctx, scanPeriod)
	}
}

// ---- render ------------------------------------------------------------------

// runRender sums all live rings and plays the mix into the target endpoint.
func (m *mixer) runRender(ctx context.Context, target Endpoint) error {
	if err := initCOM(); err != nil {
		return err
	}
	dev, err := openDevice(target.ID)
	if err != nil {
		return err
	}
	defer dev.Release()
	ac, err := dev.ActivateAudioClient()
	if err != nil {
		return fmt.Errorf("Activate: %w", err)
	}
	defer ac.Release()
	flags := uint32(audclntStreamFlagsAutoConvertPCM | audclntStreamFlagsSRCDefaultQuality)
	if err := ac.Initialize(flags, renderBufferHNS, floatFormat(m.canon.Rate, m.canon.Channels).wfx()); err != nil {
		return fmt.Errorf("Initialize render: %w", err)
	}
	bufFrames, err := ac.GetBufferSize()
	if err != nil {
		return err
	}
	rc, err := ac.RenderClient()
	if err != nil {
		return fmt.Errorf("IAudioRenderClient: %w", err)
	}
	defer rc.Release()
	if err := ac.Start(); err != nil {
		return fmt.Errorf("Start: %w", err)
	}
	defer ac.Stop()
	log.Printf("[%s] render started (%s, buffer %d frames)", target.Name, m.canon, bufFrames)
	m.setTarget(target.Name)
	defer m.setTarget("")

	ch := m.canon.Channels
	mix := make([]float32, int(bufFrames)*ch)
	tmp := make([]float32, int(bufFrames)*ch)
	ticker := time.NewTicker(tickPeriod)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
		padding, err := ac.GetCurrentPadding()
		if err != nil {
			return fmt.Errorf("GetCurrentPadding: %w", err)
		}
		avail := int(bufFrames) - int(padding)
		if avail <= 0 {
			continue
		}
		mix = mix[:avail*ch]
		for i := range mix {
			mix[i] = 0
		}
		for _, r := range m.rings() {
			n := r.pop(tmp, avail)
			for i := 0; i < n*ch; i++ {
				mix[i] += tmp[i]
			}
		}
		var mx float32
		for i, v := range mix {
			if v > 1 {
				v = 1
			} else if v < -1 {
				v = -1
			}
			mix[i] = v
			if v < 0 {
				v = -v
			}
			if v > mx {
				mx = v
			}
		}
		m.outPeak.bumpPeak(mx)
		data, err := rc.GetBuffer(uint32(avail))
		if err != nil {
			return fmt.Errorf("GetBuffer: %w", err)
		}
		dst := unsafe.Slice((*float32)(unsafe.Pointer(data)), avail*ch)
		copy(dst, mix)
		if err := rc.ReleaseBuffer(uint32(avail), 0); err != nil {
			return fmt.Errorf("ReleaseBuffer: %w", err)
		}
	}
}

// renderLoop keeps the output open, waiting for the target to exist.
func (m *mixer) renderLoop(ctx context.Context) {
	for ctx.Err() == nil {
		target, err := targetID(m.config())
		if err != nil {
			log.Printf("%v; run `maxmix install-cable` or edit maxmix.json; retrying in %s", err, retryDelay)
			sleepCtx(ctx, retryDelay)
			continue
		}
		err = m.runRender(ctx, target)
		if ctx.Err() != nil {
			return
		}
		log.Printf("[%s] render stopped: %v; retrying in %s", target.Name, err, retryDelay)
		sleepCtx(ctx, retryDelay)
	}
}

// ---- top level -------------------------------------------------------------------

func sleepCtx(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

// describeSources renders the source list for the log.
func describeSources(cfg Config) string {
	var parts []string
	for _, s := range cfg.Sources {
		switch s.Kind() {
		case "device":
			parts = append(parts, "device:"+s.Match)
		case "mic":
			parts = append(parts, "mic:"+s.Mic)
		case "game", "auto":
			parts = append(parts, fmt.Sprintf("%s(-%d excluded)", s.Process, len(s.Exclude)))
		default:
			parts = append(parts, "process:"+s.Process)
		}
	}
	sort.Strings(parts)
	return strings.Join(parts, ", ")
}

// runOnce starts the render loop and one supervisor per source and returns
// when ctx ends or the mixer asks for a restart (config applied).
func (m *mixer) runOnce(ctx context.Context) {
	m.mu.Lock()
	cfg, restart := m.cfg, m.restart
	m.mu.Unlock()
	rctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); m.renderLoop(rctx) }()
	for i, s := range cfg.Sources {
		i, s := i, s
		wg.Add(1)
		go func() {
			defer wg.Done()
			switch s.Kind() {
			case "device":
				m.deviceLoop(rctx, i, s)
			case "mic":
				m.micLoop(rctx, i, s)
			case "auto":
				m.autoLoop(rctx, i, s)
			case "game":
				m.gameLoop(rctx, i, s)
			default:
				m.processLoop(rctx, i, s)
			}
		}()
	}
	select {
	case <-ctx.Done():
	case <-restart:
	}
	cancel()
	wg.Wait()
}

// run keeps the mixer alive across config reloads until ctx ends.
func (m *mixer) run(ctx context.Context) {
	for ctx.Err() == nil {
		m.runOnce(ctx)
	}
}

// ---- microphone --------------------------------------------------------------

// monitorOut plays the microphone back to a render endpoint with low latency,
// so the speaker hears themselves. It is optional and independent of the mix.
type monitorOut struct {
	ac        *IAudioClient
	rc        *IAudioRenderClient
	bufFrames uint32
	channels  int
	pending   []float32
	name      string
}

const monitorBufferHNS = int64(60 * 10000) // 60 ms, keeps monitoring responsive

func openMonitor(name string, canon sampleFormat) (*monitorOut, error) {
	eps, err := listEndpoints(eRender)
	if err != nil {
		return nil, err
	}
	ep, ok := findEndpoint(eps, name)
	if !ok {
		return nil, fmt.Errorf("monitor device %q not found", name)
	}
	dev, err := openDevice(ep.ID)
	if err != nil {
		return nil, err
	}
	defer dev.Release()
	ac, err := dev.ActivateAudioClient()
	if err != nil {
		return nil, fmt.Errorf("%s: Activate: %w", ep.Name, err)
	}
	flags := uint32(audclntStreamFlagsAutoConvertPCM | audclntStreamFlagsSRCDefaultQuality)
	if err := ac.Initialize(flags, monitorBufferHNS, floatFormat(canon.Rate, canon.Channels).wfx()); err != nil {
		ac.Release()
		return nil, fmt.Errorf("%s: Initialize monitor: %w", ep.Name, err)
	}
	bufFrames, err := ac.GetBufferSize()
	if err != nil {
		ac.Release()
		return nil, err
	}
	rc, err := ac.RenderClient()
	if err != nil {
		ac.Release()
		return nil, err
	}
	if err := ac.Start(); err != nil {
		rc.Release()
		ac.Release()
		return nil, err
	}
	return &monitorOut{ac: ac, rc: rc, bufFrames: bufFrames, channels: canon.Channels, name: ep.Name}, nil
}

func (mo *monitorOut) close() {
	if mo == nil {
		return
	}
	mo.ac.Stop()
	mo.rc.Release()
	mo.ac.Release()
}

// push queues samples and writes as much as the device can take. Anything that
// does not fit is dropped: monitoring must stay close to real time.
func (mo *monitorOut) push(samples []float32, gain float32) error {
	if mo == nil {
		return nil
	}
	if gain != 1 {
		scaled := make([]float32, len(samples))
		for i, v := range samples {
			scaled[i] = v * gain
		}
		samples = scaled
	}
	mo.pending = append(mo.pending, samples...)
	padding, err := mo.ac.GetCurrentPadding()
	if err != nil {
		return err
	}
	avail := int(mo.bufFrames) - int(padding)
	have := len(mo.pending) / mo.channels
	if have > int(mo.bufFrames) { // fell behind: keep only the freshest audio
		drop := (have - int(mo.bufFrames)) * mo.channels
		mo.pending = append(mo.pending[:0], mo.pending[drop:]...)
		have = int(mo.bufFrames)
	}
	n := have
	if n > avail {
		n = avail
	}
	if n <= 0 {
		return nil
	}
	data, err := mo.rc.GetBuffer(uint32(n))
	if err != nil {
		return err
	}
	dst := unsafe.Slice((*float32)(unsafe.Pointer(data)), n*mo.channels)
	copy(dst, mo.pending[:n*mo.channels])
	if err := mo.rc.ReleaseBuffer(uint32(n), 0); err != nil {
		return err
	}
	mo.pending = append(mo.pending[:0], mo.pending[n*mo.channels:]...)
	return nil
}

// runMicCapture records a microphone endpoint into r and, when configured,
// plays it back to the monitor device.
func (m *mixer) runMicCapture(ctx context.Context, ep Endpoint, s Source, r *ring) error {
	if err := initCOM(); err != nil {
		return err
	}
	dev, err := openDevice(ep.ID)
	if err != nil {
		return err
	}
	defer dev.Release()
	ac, format, err := openCaptureClient(dev)
	if err != nil {
		return fmt.Errorf("%s: %w", ep.Name, err)
	}
	defer ac.Release()
	cc, err := ac.CaptureClient()
	if err != nil {
		return fmt.Errorf("%s: IAudioCaptureClient: %w", ep.Name, err)
	}
	defer cc.Release()
	if err := ac.Start(); err != nil {
		return fmt.Errorf("%s: Start: %w", ep.Name, err)
	}
	defer ac.Stop()

	log.Printf("[%s] microphone capture started (%s)", ep.Name, format)

	conv := newConverter(format, m.canon)
	out := make([]float32, 0, 8192)
	ticker := time.NewTicker(tickPeriod)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
		for {
			next, err := cc.GetNextPacketSize()
			if err != nil {
				return fmt.Errorf("%s: GetNextPacketSize: %w", ep.Name, err)
			}
			if next == 0 {
				break
			}
			data, frames, flags, err := cc.GetBuffer()
			if err != nil {
				return fmt.Errorf("%s: GetBuffer: %w", ep.Name, err)
			}
			if frames > 0 {
				out = out[:0]
				if flags&audclntBufferFlagsSilent != 0 || data == nil {
					n := int(frames) * m.canon.Rate / format.Rate
					for i := 0; i < n*m.canon.Channels; i++ {
						out = append(out, 0)
					}
				} else {
					raw := unsafe.Slice(data, int(frames)*format.BlockAlign())
					out = conv.convert(raw, int(frames), out)
				}
				if s.Gain != 1 {
					for i := range out {
						out[i] *= s.Gain
					}
				}
				r.push(out)
			}
			if err := cc.ReleaseBuffer(frames); err != nil {
				return fmt.Errorf("%s: ReleaseBuffer: %w", ep.Name, err)
			}
		}
		m.checkBacklog(ep.Name, r)
	}
}

// micLoop keeps a microphone source captured, waiting for the device to appear.
func (m *mixer) micLoop(ctx context.Context, idx int, s Source) {
	warned := false
	for ctx.Err() == nil {
		eps, err := listEndpoints(eCapture)
		var ep Endpoint
		ok := false
		if err == nil {
			ep, ok = findEndpoint(eps, s.Mic)
		}
		if !ok {
			if !warned {
				log.Printf("microphone %q not found, waiting", s.Mic)
				warned = true
			}
			sleepCtx(ctx, retryDelay)
			continue
		}
		warned = false
		key := "mic:" + ep.ID
		r := m.addCapture(&capture{Key: key, Kind: "mic", Label: ep.Name, Source: idx})
		err = m.runMicCapture(ctx, ep, s, r)
		m.removeCapture(key)
		if ctx.Err() != nil {
			return
		}
		log.Printf("[%s] microphone capture stopped: %v; retrying in %s", ep.Name, err, retryDelay)
		sleepCtx(ctx, retryDelay)
	}
}
