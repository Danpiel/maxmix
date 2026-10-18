package main

// Thin user32 / gdi32 / comctl32 / shell32 bindings for the tray icon and the
// settings window. Only what gui.go needs.

import (
	"syscall"
	"unsafe"
)

var (
	gdi32    = syscall.NewLazyDLL("gdi32.dll")
	comctl32 = syscall.NewLazyDLL("comctl32.dll")

	procRegisterClassExW     = user32.NewProc("RegisterClassExW")
	procCreateWindowExW      = user32.NewProc("CreateWindowExW")
	procDefWindowProcW       = user32.NewProc("DefWindowProcW")
	procDestroyWindow        = user32.NewProc("DestroyWindow")
	procShowWindow           = user32.NewProc("ShowWindow")
	procGetMessageW          = user32.NewProc("GetMessageW")
	procTranslateMessage     = user32.NewProc("TranslateMessage")
	procDispatchMessageW     = user32.NewProc("DispatchMessageW")
	procPostQuitMessage      = user32.NewProc("PostQuitMessage")
	procPostMessageW         = user32.NewProc("PostMessageW")
	procSendMessageW         = user32.NewProc("SendMessageW")
	procSetWindowTextW       = user32.NewProc("SetWindowTextW")
	procGetWindowTextW       = user32.NewProc("GetWindowTextW")
	procGetWindowTextLengthW = user32.NewProc("GetWindowTextLengthW")
	procLoadIconW            = user32.NewProc("LoadIconW")
	procLoadCursorW          = user32.NewProc("LoadCursorW")
	procCreatePopupMenu      = user32.NewProc("CreatePopupMenu")
	procAppendMenuW          = user32.NewProc("AppendMenuW")
	procTrackPopupMenu       = user32.NewProc("TrackPopupMenu")
	procDestroyMenu          = user32.NewProc("DestroyMenu")
	procGetCursorPos         = user32.NewProc("GetCursorPos")
	procSetForegroundWindow  = user32.NewProc("SetForegroundWindow")
	procSetTimer             = user32.NewProc("SetTimer")
	procKillTimer            = user32.NewProc("KillTimer")
	procAdjustWindowRect     = user32.NewProc("AdjustWindowRect")
	procMessageBoxW          = user32.NewProc("MessageBoxW")
	procGetSystemMetrics     = user32.NewProc("GetSystemMetrics")
	procEnableWindow         = user32.NewProc("EnableWindow")

	procGetStockObject = gdi32.NewProc("GetStockObject")
	procCreateFontW    = gdi32.NewProc("CreateFontW")

	procInitCommonControlsEx = comctl32.NewProc("InitCommonControlsEx")
	procShellNotifyIconW     = shell32.NewProc("Shell_NotifyIconW")
	procLoadImageW           = user32.NewProc("LoadImageW")
	procShellExecuteW        = shell32.NewProc("ShellExecuteW")
	procGetModuleHandleW     = kernel32.NewProc("GetModuleHandleW")
	procAttachConsole        = kernel32.NewProc("AttachConsole")
	procGetStdHandle         = kernel32.NewProc("GetStdHandle")
)

// Window messages and styles.
const (
	wmNull        = 0x0000
	wmDestroy     = 0x0002
	wmClose       = 0x0010
	wmSetFont     = 0x0030
	wmNotify      = 0x004E
	wmContextMenu = 0x007B
	wmCommand     = 0x0111
	wmTimer       = 0x0113
	wmHScroll     = 0x0114
	wmLButtonDbl  = 0x0203
	wmRButtonUp   = 0x0205
	wmUser        = 0x0400
	wmApp         = 0x8000
	wmTray        = wmApp + 1

	wsOverlapped   = 0x00000000
	wsSysMenu      = 0x00080000
	wsMinimizeBox  = 0x00020000
	wsVisible      = 0x10000000
	wsBorder       = 0x00800000
	wsTabStop      = 0x00010000
	wsVScroll      = 0x00200000
	wsGroup        = 0x00020000
	wsExClientEdge = 0x00000200

	swHide = 0
	swShow = 5

	colorBtnFace   = 15
	idiApplication = 32512
	imageIcon      = 1
	idcArrow       = 32512

	mfString       = 0x0000
	mfSeparator    = 0x0800
	mfGrayed       = 0x0001
	tpmRightButton = 0x0002
	tpmBottomAlign = 0x0020

	mbOK          = 0x0
	mbIconError   = 0x10
	mbIconInfo    = 0x40
	swShowNormalW = 1

	attachParentProcess = ^uintptr(0)
	stdOutputHandle     = ^uintptr(10) // -11
	stdErrorHandle      = ^uintptr(11) // -12
)

// Control classes, styles and messages.
const (
	bsPushButton = 0x0
	bsGroupBox   = 0x7
	bsDefPush    = 0x1
	bnClicked    = 0

	ssLeft       = 0x0
	ssOwnerDraw  = 0xD
	ssLeftNoWrap = 0xC
	ssRight      = 0x2

	esMultiline   = 0x0004
	esAutoVScroll = 0x0040
	esAutoHScroll = 0x0080
	enChange      = 0x0300

	cbsDropDown     = 0x0002
	cbsDropDownList = 0x0003
	cbAddString     = 0x0143
	cbGetCurSel     = 0x0147
	cbResetContent  = 0x014B
	cbSetCurSel     = 0x014E
	cbnSelChange    = 1
	cbnEditChange   = 5

	lvsReport        = 0x0001
	lvsSingleSel     = 0x0004
	lvsShowSelAlways = 0x0008
	lvsNoSortHeader  = 0x8000
	lvmFirst         = 0x1000
	lvmGetItemCount  = lvmFirst + 4
	lvmDeleteItem    = lvmFirst + 8
	lvmDeleteAll     = lvmFirst + 9
	lvmGetNextItem   = lvmFirst + 12
	lvmSetItemState  = lvmFirst + 43
	lvmSetExtStyle   = lvmFirst + 54
	lvmInsertItemW   = lvmFirst + 77
	lvmInsertColumnW = lvmFirst + 97
	lvmSetItemTextW  = lvmFirst + 116
	lvsExGridlines   = 0x0001
	lvsExFullRowSel  = 0x0020
	lvcfFmt          = 0x1
	lvcfWidth        = 0x2
	lvcfText         = 0x4
	lvcfSubItem      = 0x8
	lvifText         = 0x1
	lvifState        = 0x8
	lvisFocused      = 0x1
	lvisSelected     = 0x2
	lvniSelected     = 0x2
	lvnItemChanged   = 0xFFFFFF9B // LVN_FIRST(-100) - 1 as uint32

	tbmGetPos    = wmUser
	tbmSetPos    = wmUser + 5
	tbmSetRange  = wmUser + 6
	tbsAutoTicks = 0x0001
	tbsNoTicks   = 0x0010

	pbmSetRange = wmUser + 1
	pbmSetPos   = wmUser + 2

	iccListView = 0x1
	iccBar      = 0x4
	iccProgress = 0x20

	nimAdd     = 0
	nimDelete  = 2
	nifMessage = 0x1
	nifIcon    = 0x2
	nifTip     = 0x4
)

type point struct{ X, Y int32 }

type wndClassExW struct {
	Size       uint32
	Style      uint32
	WndProc    uintptr
	ClsExtra   int32
	WndExtra   int32
	Instance   uintptr
	Icon       uintptr
	Cursor     uintptr
	Background uintptr
	MenuName   *uint16
	ClassName  *uint16
	IconSm     uintptr
}

type msg struct {
	Hwnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      point
	Private uint32
}

type nmhdr struct {
	HwndFrom uintptr
	IDFrom   uintptr
	Code     uint32
}

type nmListView struct {
	Hdr      nmhdr
	Item     int32
	SubItem  int32
	NewState uint32
	OldState uint32
	Changed  uint32
	PtAction point
	LParam   uintptr
}

type lvColumnW struct {
	Mask      uint32
	Fmt       int32
	Cx        int32
	Text      *uint16
	TextMax   int32
	SubItem   int32
	Image     int32
	Order     int32
	CxMin     int32
	CxDefault int32
	CxIdeal   int32
}

type lvItemW struct {
	Mask      uint32
	Item      int32
	SubItem   int32
	State     uint32
	StateMask uint32
	Text      *uint16
	TextMax   int32
	Image     int32
	LParam    uintptr
	Indent    int32
	GroupID   int32
	Columns   uint32
	PuColumns *uint32
	PiColFmt  *int32
	Group     int32
}

type initCommonControlsEx struct {
	Size uint32
	ICC  uint32
}

type notifyIconDataW struct {
	Size            uint32
	Hwnd            uintptr
	ID              uint32
	Flags           uint32
	CallbackMessage uint32
	Icon            uintptr
	Tip             [128]uint16
	State           uint32
	StateMask       uint32
	Info            [256]uint16
	Version         uint32
	InfoTitle       [64]uint16
	InfoFlags       uint32
	GUID            GUID
	BalloonIcon     uintptr
}

func utf16(s string) *uint16 {
	p, _ := syscall.UTF16PtrFromString(s)
	return p
}

func loword(v uintptr) uint32 { return uint32(v & 0xFFFF) }
func hiword(v uintptr) uint32 { return uint32((v >> 16) & 0xFFFF) }

func sendMessage(hwnd uintptr, m uint32, w, l uintptr) uintptr {
	r, _, _ := procSendMessageW.Call(hwnd, uintptr(m), w, l)
	return r
}

func setText(hwnd uintptr, s string) {
	procSetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(utf16(s))))
}

func getText(hwnd uintptr) string {
	n, _, _ := procGetWindowTextLengthW.Call(hwnd)
	buf := make([]uint16, n+1)
	procGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), n+1)
	return syscall.UTF16ToString(buf)
}

func showWindow(hwnd uintptr, cmd int) { procShowWindow.Call(hwnd, uintptr(cmd)) }

func messageBox(hwnd uintptr, text, title string, flags uint32) {
	procMessageBoxW.Call(hwnd, uintptr(unsafe.Pointer(utf16(text))), uintptr(unsafe.Pointer(utf16(title))), uintptr(flags))
}

// createControl makes a child window of the given class.
func createControl(parent uintptr, class, text string, style uint32, x, y, w, h int, id int, font uintptr) uintptr {
	hwnd, _, _ := procCreateWindowExW.Call(0, uintptr(unsafe.Pointer(utf16(class))), uintptr(unsafe.Pointer(utf16(text))),
		uintptr(style|wsChild|wsVisible), uintptr(x), uintptr(y), uintptr(w), uintptr(h), parent, uintptr(id), 0, 0)
	if font != 0 {
		sendMessage(hwnd, wmSetFont, font, 1)
	}
	return hwnd
}

// ---- list view helpers ----------------------------------------------------------

func lvAddColumn(hwnd uintptr, index int, title string, width int) {
	col := lvColumnW{Mask: lvcfText | lvcfWidth | lvcfSubItem, Cx: int32(width), Text: utf16(title), SubItem: int32(index)}
	sendMessage(hwnd, lvmInsertColumnW, uintptr(index), uintptr(unsafe.Pointer(&col)))
}

func lvInsertRow(hwnd uintptr, row int, cells ...string) {
	it := lvItemW{Mask: lvifText, Item: int32(row), Text: utf16(cells[0])}
	sendMessage(hwnd, lvmInsertItemW, 0, uintptr(unsafe.Pointer(&it)))
	for i := 1; i < len(cells); i++ {
		lvSetCell(hwnd, row, i, cells[i])
	}
}

func lvSetCell(hwnd uintptr, row, col int, text string) {
	it := lvItemW{Mask: lvifText, Item: int32(row), SubItem: int32(col), Text: utf16(text)}
	sendMessage(hwnd, lvmSetItemTextW, uintptr(row), uintptr(unsafe.Pointer(&it)))
}

func lvCount(hwnd uintptr) int { return int(sendMessage(hwnd, lvmGetItemCount, 0, 0)) }

func lvClear(hwnd uintptr) { sendMessage(hwnd, lvmDeleteAll, 0, 0) }

// lvSelected returns the selected row or -1.
func lvSelected(hwnd uintptr) int {
	r := sendMessage(hwnd, lvmGetNextItem, ^uintptr(0), lvniSelected)
	return int(int32(r))
}

func lvSelect(hwnd uintptr, row int) {
	it := lvItemW{Mask: lvifState, State: lvisSelected | lvisFocused, StateMask: lvisSelected | lvisFocused}
	sendMessage(hwnd, lvmSetItemState, uintptr(row), uintptr(unsafe.Pointer(&it)))
}

// ---- combo box helpers -------------------------------------------------------------

func cbFill(hwnd uintptr, items []string, selected string) {
	sendMessage(hwnd, cbResetContent, 0, 0)
	sel := -1
	for i, s := range items {
		sendMessage(hwnd, cbAddString, 0, uintptr(unsafe.Pointer(utf16(s))))
		if s == selected {
			sel = i
		}
	}
	if sel >= 0 {
		sendMessage(hwnd, cbSetCurSel, uintptr(sel), 0)
	} else if selected != "" {
		setText(hwnd, selected)
	}
}

func cbIndex(hwnd uintptr) int { return int(int32(sendMessage(hwnd, cbGetCurSel, 0, 0))) }

// ---- misc ----------------------------------------------------------------------------

func defaultFont() uintptr {
	f, _, _ := procCreateFontW.Call(^uintptr(12), 0, 0, 0, 400, 0, 0, 0, 1, 0, 0, 5, 0, uintptr(unsafe.Pointer(utf16(uiFontFace(false)))))
	if f == 0 {
		f, _, _ = procGetStockObject.Call(17) // DEFAULT_GUI_FONT
	}
	return f
}

// attachParentConsole makes stdout/stderr work in a -H=windowsgui binary that
// was started from a terminal. Returns false when there is no console.
func attachParentConsole() bool {
	ok, _, _ := procAttachConsole.Call(attachParentProcess)
	if ok == 0 {
		return false
	}
	if h, _, _ := procGetStdHandle.Call(stdOutputHandle); h != 0 && h != ^uintptr(0) {
		osStdout(h)
	}
	if h, _, _ := procGetStdHandle.Call(stdErrorHandle); h != 0 && h != ^uintptr(0) {
		osStderr(h)
	}
	return true
}

func shellOpen(path string) {
	procShellExecuteW.Call(0, uintptr(unsafe.Pointer(utf16("open"))), uintptr(unsafe.Pointer(utf16(path))), 0, 0, swShowNormalW)
}

// appIcon loads the embedded icon (resource id 1 from rsrc_windows_amd64.syso)
// at the requested pixel size; falls back to the generic application icon.
func appIcon(size int) uintptr {
	inst, _, _ := procGetModuleHandleW.Call(0)
	h, _, _ := procLoadImageW.Call(inst, 1, imageIcon, uintptr(size), uintptr(size), 0)
	if h == 0 {
		h, _, _ = procLoadIconW.Call(0, idiApplication)
	}
	return h
}

// boldFont is the section-header font (semibold, slightly larger).
func boldFont() uintptr {
	f, _, _ := procCreateFontW.Call(^uintptr(15), 0, 0, 0, 600, 0, 0, 0, 1, 0, 0, 5, 0, uintptr(unsafe.Pointer(utf16(uiFontFace(true)))))
	if f == 0 {
		return defaultFont()
	}
	return f
}
