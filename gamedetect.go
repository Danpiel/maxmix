package main

// Heuristic "which process is the game" detection. A process with a render
// audio session counts as a game when any of these holds:
//   - it (or a process in its tree) owns the foreground window,
//   - it (or its tree) has a visible borderless window covering a whole monitor,
//   - its tree was launched by a known game launcher (Steam, Epic, ...).
// The tree root that is captured is the topmost ancestor that is not a
// launcher / shell / system process, so helper processes of the game are
// included and the launcher itself is not.

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"syscall"
	"unsafe"
)

var (
	user32                     = syscall.NewLazyDLL("user32.dll")
	procGetForegroundWindow    = user32.NewProc("GetForegroundWindow")
	procGetWindowThreadProcess = user32.NewProc("GetWindowThreadProcessId")
	procGetAncestor            = user32.NewProc("GetAncestor")
	procEnumWindows            = user32.NewProc("EnumWindows")
	procIsWindowVisible        = user32.NewProc("IsWindowVisible")
	procGetWindowRect          = user32.NewProc("GetWindowRect")
	procMonitorFromWindow      = user32.NewProc("MonitorFromWindow")
	procGetMonitorInfoW        = user32.NewProc("GetMonitorInfoW")
	procGetWindowLongPtrW      = user32.NewProc("GetWindowLongPtrW")
)

const (
	gaRootOwner             = 3
	monitorDefaultToNearest = 2
	gwlStyle                = ^uintptr(15) // -16
	wsCaption               = 0x00C00000
	wsChild                 = 0x40000000
)

type rect struct{ Left, Top, Right, Bottom int32 }

type monitorInfo struct {
	Size    uint32
	Monitor rect
	Work    rect
	Flags   uint32
}

// launchers are parents whose children are games; they are never captured.
var launchers = map[string]bool{
	"steam.exe": true, "epicgameslauncher.exe": true, "battle.net.exe": true,
	"galaxyclient.exe": true, "upc.exe": true, "ubisoftconnect.exe": true,
	"eadesktop.exe": true, "origin.exe": true, "riotclientservices.exe": true,
	"gamelaunchhelper.exe": true, "playnite.desktopapp.exe": true,
	"playnite.fullscreenapp.exe": true, "xboxpcapp.exe": true, "amazon games.exe": true,
}

// shellProcs stop the walk up the tree: nothing above them is game related.
var shellProcs = map[string]bool{
	"explorer.exe": true, "svchost.exe": true, "services.exe": true, "wininit.exe": true,
	"winlogon.exe": true, "cmd.exe": true, "powershell.exe": true, "pwsh.exe": true,
	"conhost.exe": true, "windowsterminal.exe": true, "system": true, "runtimebroker.exe": true,
}

// gameLikeWindow reports whether a window looks like a game: no title bar and
// covering most of its monitor. A maximised editor or browser has a caption and
// must never pass, or every app that beeps while focused becomes "the game".
func gameLikeWindow(hwnd uintptr) bool {
	style, _, _ := procGetWindowLongPtrW.Call(hwnd, gwlStyle)
	if style&wsCaption == wsCaption || style&wsChild != 0 {
		return false
	}
	var r rect
	if ok, _, _ := procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&r))); ok == 0 {
		return false
	}
	w, h := int64(r.Right-r.Left), int64(r.Bottom-r.Top)
	if w < 640 || h < 480 {
		return false
	}
	mon, _, _ := procMonitorFromWindow.Call(hwnd, monitorDefaultToNearest)
	if mon == 0 {
		return false
	}
	mi := monitorInfo{Size: uint32(unsafe.Sizeof(monitorInfo{}))}
	if ok, _, _ := procGetMonitorInfoW.Call(mon, uintptr(unsafe.Pointer(&mi))); ok == 0 {
		return false
	}
	mw := int64(mi.Monitor.Right - mi.Monitor.Left)
	mh := int64(mi.Monitor.Bottom - mi.Monitor.Top)
	return w*h*10 >= mw*mh*8 // at least 80% of the monitor
}

// foregroundPID returns the owner of the foreground window, but only when that
// window is game-like; otherwise 0.
func foregroundPID() uint32 {
	hwnd, _, _ := procGetForegroundWindow.Call()
	if hwnd == 0 {
		return 0
	}
	root, _, _ := procGetAncestor.Call(hwnd, gaRootOwner)
	if root != 0 {
		hwnd = root
	}
	if !gameLikeWindow(hwnd) {
		return 0
	}
	var pid uint32
	procGetWindowThreadProcess.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	return pid
}

var (
	enumMu        sync.Mutex
	enumResult    map[uint32]bool
	enumCallback  uintptr
	enumCallbackO sync.Once
)

// enumWindowProc records the owner of every visible caption-less window that
// covers its monitor exactly (exclusive fullscreen or borderless windowed).
func enumWindowProc(hwnd, _ uintptr) uintptr {
	if v, _, _ := procIsWindowVisible.Call(hwnd); v == 0 {
		return 1
	}
	style, _, _ := procGetWindowLongPtrW.Call(hwnd, gwlStyle)
	if style&wsCaption == wsCaption || style&wsChild != 0 {
		return 1
	}
	var r rect
	if ok, _, _ := procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&r))); ok == 0 {
		return 1
	}
	if r.Right-r.Left < 640 || r.Bottom-r.Top < 480 {
		return 1
	}
	mon, _, _ := procMonitorFromWindow.Call(hwnd, monitorDefaultToNearest)
	if mon == 0 {
		return 1
	}
	mi := monitorInfo{Size: uint32(unsafe.Sizeof(monitorInfo{}))}
	if ok, _, _ := procGetMonitorInfoW.Call(mon, uintptr(unsafe.Pointer(&mi))); ok == 0 {
		return 1
	}
	if r != mi.Monitor {
		return 1
	}
	var pid uint32
	procGetWindowThreadProcess.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	if pid != 0 {
		enumResult[pid] = true
	}
	return 1
}

// fullscreenPIDs returns processes owning a fullscreen/borderless window.
func fullscreenPIDs() map[uint32]bool {
	enumMu.Lock()
	defer enumMu.Unlock()
	enumCallbackO.Do(func() { enumCallback = syscall.NewCallback(enumWindowProc) })
	enumResult = map[uint32]bool{}
	procEnumWindows.Call(enumCallback, 0)
	out := enumResult
	enumResult = nil
	return out
}

// gameDetector holds the (sticky) set of detected game trees.
type gameDetector struct {
	exclude map[string]bool
	chosen  map[uint32]string // root pid -> exe
}

func newGameDetector(exclude []string) *gameDetector {
	d := &gameDetector{exclude: map[string]bool{}, chosen: map[uint32]string{}}
	for _, e := range exclude {
		d.exclude[strings.ToLower(e)] = true
	}
	return d
}

// stops reports whether walking up the tree must stop below exe.
func (d *gameDetector) stops(exe string) bool {
	l := strings.ToLower(exe)
	return launchers[l] || shellProcs[l] || d.exclude[l]
}

// root climbs from pid to the topmost ancestor that is still game related.
func (d *gameDetector) root(table map[uint32]procInfo, pid uint32) uint32 {
	cur := pid
	for depth := 0; depth < 64; depth++ {
		p, ok := table[cur]
		if !ok {
			return cur
		}
		parent, ok := table[p.Parent]
		if !ok || p.Parent == 0 || p.Parent == cur || d.stops(parent.Exe) {
			return cur
		}
		cur = p.Parent
	}
	return cur
}

// scan updates the chosen set from the current sessions and process table and
// returns root pid -> exe of every game to capture.
func (d *gameDetector) scan(table map[uint32]procInfo, sessions []audioSession, self uint32) map[uint32]string {
	// Drop games that exited.
	for pid := range d.chosen {
		if _, alive := table[pid]; !alive {
			delete(d.chosen, pid)
		}
	}
	fgPID := foregroundPID()
	fg := uint32(0)
	if fgPID != 0 {
		fg = d.root(table, fgPID)
	}
	full := map[uint32]bool{}
	for pid := range fullscreenPIDs() {
		full[d.root(table, pid)] = true
	}
	for _, s := range sessions {
		if s.State == audioSessionStateExpired || s.PID == self {
			continue
		}
		p, alive := table[s.PID]
		if !alive || d.stops(p.Exe) {
			continue
		}
		r := d.root(table, s.PID)
		if _, already := d.chosen[r]; already {
			continue
		}
		rp := table[r]
		launched := false
		if parent, ok := table[rp.Parent]; ok && launchers[strings.ToLower(parent.Exe)] {
			launched = true
		}
		if (fg != 0 && r == fg) || full[r] || launched {
			d.chosen[r] = rp.Exe
		}
	}
	out := make(map[uint32]string, len(d.chosen))
	for k, v := range d.chosen {
		out[k] = v
	}
	return out
}

// gameInfo is one detected game for status output.
type gameInfo struct {
	PID uint32 `json:"pid"`
	Exe string `json:"exe"`
}

func sortedGames(m map[uint32]string) []gameInfo {
	out := make([]gameInfo, 0, len(m))
	for pid, exe := range m {
		out = append(out, gameInfo{PID: pid, Exe: exe})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PID < out[j].PID })
	return out
}

// cmdGame runs the detector once and explains, per audio-producing process,
// whether it would be treated as the game and why.
func cmdGame() error {
	sessions, err := listRenderSessions()
	if err != nil {
		return err
	}
	table, err := processTable()
	if err != nil {
		return err
	}
	d := newGameDetector(defaultAutoExclude)
	self := uint32(syscall.Getpid())
	fgRoot := d.root(table, foregroundPID())
	full := map[uint32]bool{}
	for pid := range fullscreenPIDs() {
		full[d.root(table, pid)] = true
	}
	fmt.Printf("foreground: %s (%d)\n", table[fgRoot].Exe, fgRoot)
	fmt.Printf("  %-7s %-34s %-7s %-34s %s\n", "pid", "process", "root", "root exe", "verdict")
	seen := map[uint32]bool{}
	for _, s := range sessions {
		if s.State == audioSessionStateExpired || s.PID == self || seen[s.PID] {
			continue
		}
		seen[s.PID] = true
		p, alive := table[s.PID]
		if !alive {
			continue
		}
		if d.stops(p.Exe) {
			fmt.Printf("  %-7d %-34s %-7s %-34s excluded\n", s.PID, p.Exe, "", "")
			continue
		}
		r := d.root(table, s.PID)
		rp := table[r]
		var why []string
		if r == fgRoot {
			why = append(why, "foreground")
		}
		if full[r] {
			why = append(why, "fullscreen")
		}
		if parent, ok := table[rp.Parent]; ok && launchers[strings.ToLower(parent.Exe)] {
			why = append(why, "launched by "+parent.Exe)
		}
		verdict := "not a game (no window/launcher signal)"
		if len(why) > 0 {
			verdict = "GAME: " + strings.Join(why, ", ")
		}
		fmt.Printf("  %-7d %-34s %-7d %-34s %s\n", s.PID, p.Exe, r, rp.Exe, verdict)
	}
	return nil
}
