package main

// Tray icon and the native settings window (Win32 common controls, no
// dependencies). Everything here runs on the main OS thread inside the
// message loop; the mixer runs in its own goroutines.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"unsafe"
)

// Control and menu ids.
const (
	idTraySettings = 2001
	idTrayLog      = 2002
	idTrayExit     = 2003

	idTarget   = 1001
	idOutMeter = 1002
	idSources  = 1003
	idAdd      = 1004
	idDel      = 1005
	idKind     = 1006
	idValue    = 1007
	idGain     = 1008
	idExclude  = 1009
	idSessions = 1010
	idAddProc  = 1011
	idExcl     = 1012
	idApps     = 1013
	idRoute    = 1014
	idUnroute  = 1015
	idSave     = 1016
	idClose    = 1017
	idTimer    = 1
	refreshMS  = 500
)

var kindNames = []struct{ key, label string }{
	{"game", "Игра (авто)"},
	{"process", "Программа"},
	{"auto", "Всё, что звучит"},
	{"device", "Устройство"},
	{"mic", "Микрофон"},
}

func kindLabel(k string) string {
	for _, kn := range kindNames {
		if kn.key == k {
			return tr(kn.label)
		}
	}
	return k
}

// gui owns the tray icon and, when open, the settings window.
type gui struct {
	m      *mixer
	cancel context.CancelFunc
	theme  *theme
	fonts  uiFonts
	font   uintptr
	bold   uintptr
	icon   uintptr // 32 px, window class icon
	small  uintptr // 16 px, tray
	tray   uintptr
	nid    notifyIconDataW
	set    *settingsWin
	exited bool
}

var (
	theGUI      *gui
	wndProcOnce sync.Once
	wndProcPtr  uintptr
)

func wndProc(hwnd, m, w, l uintptr) uintptr {
	g := theGUI
	if g != nil {
		if hwnd == g.tray {
			return g.trayProc(hwnd, uint32(m), w, l)
		}
		if g.set != nil && hwnd == g.set.hwnd {
			return g.set.proc(g, uint32(m), w, l)
		}
	}
	r, _, _ := procDefWindowProcW.Call(hwnd, m, w, l)
	return r
}

func registerClass(name string, icon uintptr) error {
	wndProcOnce.Do(func() { wndProcPtr = syscall.NewCallback(wndProc) })
	inst, _, _ := procGetModuleHandleW.Call(0)
	cursor, _, _ := procLoadCursorW.Call(0, idcArrow)
	wc := wndClassExW{
		Size:       uint32(unsafe.Sizeof(wndClassExW{})),
		WndProc:    wndProcPtr,
		Instance:   inst,
		Icon:       icon,
		Cursor:     cursor,
		Background: colorBtnFace + 1,
		ClassName:  utf16(name),
		IconSm:     icon,
	}
	if r, _, err := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); r == 0 {
		return fmt.Errorf("RegisterClassEx(%s): %w", name, err)
	}
	return nil
}

// newGUI creates the hidden tray window and the notification icon.
func newGUI(m *mixer, cancel context.CancelFunc) (*gui, error) {
	icc := initCommonControlsEx{Size: 8, ICC: iccListView | iccBar | iccProgress}
	procInitCommonControlsEx.Call(uintptr(unsafe.Pointer(&icc)))
	initDarkModeSupport()
	g := &gui{m: m, cancel: cancel, theme: loadTheme(), fonts: loadFonts(), icon: appIcon(32), small: appIcon(16)}
	g.font, g.bold = g.fonts.body, g.fonts.bold
	theGUI = g
	if err := registerClass("maxmixTray", g.icon); err != nil {
		return nil, err
	}
	if err := registerClass("maxmixSettings", g.icon); err != nil {
		return nil, err
	}
	hwnd, _, err := procCreateWindowExW.Call(0, uintptr(unsafe.Pointer(utf16("maxmixTray"))), uintptr(unsafe.Pointer(utf16("maxmix"))),
		wsOverlapped, 0, 0, 0, 0, 0, 0, 0, 0)
	if hwnd == 0 {
		return nil, fmt.Errorf("CreateWindowEx(tray): %w", err)
	}
	g.tray = hwnd
	g.nid = notifyIconDataW{Hwnd: hwnd, ID: 1, Flags: nifMessage | nifIcon | nifTip, CallbackMessage: wmTray, Icon: g.small}
	g.nid.Size = uint32(unsafe.Sizeof(g.nid))
	tip, _ := syscall.UTF16FromString(tr("maxmix — микшер для записи"))
	copy(g.nid.Tip[:], tip)
	if r, _, err := procShellNotifyIconW.Call(nimAdd, uintptr(unsafe.Pointer(&g.nid))); r == 0 {
		return nil, fmt.Errorf("Shell_NotifyIcon: %w", err)
	}
	return g, nil
}

// loop runs the message loop until exit.
func (g *gui) loop() {
	var m msg
	for {
		r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if r == 0 || int32(r) == -1 {
			return
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
}

// postExit asks the GUI thread to shut down (safe from any goroutine).
func (g *gui) postExit() { procPostMessageW.Call(g.tray, wmClose, 0, 0) }

func (g *gui) exit() {
	if g.exited {
		return
	}
	g.exited = true
	procShellNotifyIconW.Call(nimDelete, uintptr(unsafe.Pointer(&g.nid)))
	if g.set != nil {
		procDestroyWindow.Call(g.set.hwnd)
	}
	g.cancel()
	procDestroyWindow.Call(g.tray)
}

func (g *gui) trayProc(hwnd uintptr, m uint32, w, l uintptr) uintptr {
	switch m {
	case wmTray:
		switch loword(l) {
		case wmRButtonUp, wmContextMenu:
			g.showMenu()
		case wmLButtonDbl:
			g.openSettings()
		}
		return 0
	case wmCommand:
		switch loword(w) {
		case idTraySettings:
			g.openSettings()
		case idTrayLog:
			shellOpen(logPath())
		case idTrayExit:
			g.exit()
		}
		return 0
	case wmSettingChange:
		if isImmersiveColorSet(l) {
			g.themeChanged()
		}
		return 0
	case wmClose:
		g.exit()
		return 0
	case wmDestroy:
		procPostQuitMessage.Call(0)
		return 0
	}
	r, _, _ := procDefWindowProcW.Call(hwnd, uintptr(m), w, l)
	return r
}

// themeChanged re-reads the system mode and restyles the open window.
func (g *gui) themeChanged() {
	dark := systemPrefersDark()
	if dark == g.theme.dark {
		return
	}
	g.theme.set(dark)
	if g.set != nil {
		g.set.applyTheme(g.theme)
	}
}

func (g *gui) showMenu() {
	menu, _, _ := procCreatePopupMenu.Call()
	defer procDestroyMenu.Call(menu)
	procAppendMenuW.Call(menu, mfString, idTraySettings, uintptr(unsafe.Pointer(utf16(tr("Настройки")))))
	procAppendMenuW.Call(menu, mfString, idTrayLog, uintptr(unsafe.Pointer(utf16(tr("Открыть лог")))))
	procAppendMenuW.Call(menu, mfSeparator, 0, 0)
	procAppendMenuW.Call(menu, mfString, idTrayExit, uintptr(unsafe.Pointer(utf16(tr("Выход")))))
	var pt point
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	procSetForegroundWindow.Call(g.tray)
	procTrackPopupMenu.Call(menu, tpmRightButton|tpmBottomAlign, uintptr(pt.X), uintptr(pt.Y), 0, g.tray, 0)
	procPostMessageW.Call(g.tray, wmNull, 0, 0)
}

func logPath() string {
	return filepath.Join(os.Getenv("LOCALAPPDATA"), "maxmix", "maxmix.log")
}

const nimModify = 1

// retitle refreshes the tray tooltip after a language change.
func (g *gui) retitle() {
	tip, _ := syscall.UTF16FromString(tr("maxmix — микшер для записи"))
	for i := range g.nid.Tip {
		g.nid.Tip[i] = 0
	}
	copy(g.nid.Tip[:], tip)
	procShellNotifyIconW.Call(nimModify, uintptr(unsafe.Pointer(&g.nid)))
}

// ---- single instance ---------------------------------------------------------

var (
	procCreateMutexW = kernel32.NewProc("CreateMutexW")
	procFindWindowW  = user32.NewProc("FindWindowW")
)

const errorAlreadyExists = 183

// claimSingleInstance returns false when another maxmix is already running; in
// that case it asks the running one to show its settings window. Two mixers
// would both render into the virtual device and double every source.
func claimSingleInstance() bool {
	name, _ := syscall.UTF16PtrFromString(`Local\maxmix-single-instance`)
	h, _, err := procCreateMutexW.Call(0, 0, uintptr(unsafe.Pointer(name)))
	if h != 0 && err != syscall.Errno(errorAlreadyExists) {
		return true // handle stays open for the lifetime of the process
	}
	cls, _ := syscall.UTF16PtrFromString("maxmixTray")
	if hwnd, _, _ := procFindWindowW.Call(uintptr(unsafe.Pointer(cls)), 0); hwnd != 0 {
		procPostMessageW.Call(hwnd, wmCommand, idTraySettings, 0)
	}
	return false
}
