package main

// Windows 11 style theming for the settings window, dark or light following
// the system "app mode": Mica backdrop and rounded corners via DWM, content in
// rounded cards painted by the parent, flat owner-drawn buttons, borderless
// inputs, dark control themes from uxtheme ("DarkMode_Explorer" /
// "DarkMode_CFD") for scrollbars, list views and combo boxes, and the
// undocumented uxtheme ordinals (Windows 10 1809+) for dark context menus.

import (
	"sync"
	"syscall"
	"unsafe"
)

var (
	dwmapi                    = syscall.NewLazyDLL("dwmapi.dll")
	procDwmSetWindowAttribute = dwmapi.NewProc("DwmSetWindowAttribute")
	uxtheme                   = syscall.NewLazyDLL("uxtheme.dll")
	procSetWindowTheme        = uxtheme.NewProc("SetWindowTheme")
	procGetProcAddress        = kernel32.NewProc("GetProcAddress")
	procCreateSolidBrush      = gdi32.NewProc("CreateSolidBrush")
	procCreatePen             = gdi32.NewProc("CreatePen")
	procDeleteObject          = gdi32.NewProc("DeleteObject")
	procSelectObject          = gdi32.NewProc("SelectObject")
	procSetTextColor          = gdi32.NewProc("SetTextColor")
	procSetBkColor            = gdi32.NewProc("SetBkColor")
	procSetBkMode             = gdi32.NewProc("SetBkMode")
	procRoundRect             = gdi32.NewProc("RoundRect")
	procFillRect              = user32.NewProc("FillRect")
	procGetClientRect         = user32.NewProc("GetClientRect")
	procRedrawWindow          = user32.NewProc("RedrawWindow")
	procBeginPaint            = user32.NewProc("BeginPaint")
	procEndPaint              = user32.NewProc("EndPaint")
	procDrawTextW             = user32.NewProc("DrawTextW")
	procInvalidateRect        = user32.NewProc("InvalidateRect")

	// uxtheme ordinals; zero when the OS does not have them.
	uxSetPreferredAppMode    uintptr // 135: (mode) -> previous
	uxFlushMenuThemes        uintptr // 136
	uxAllowDarkModeForWindow uintptr // 133: (hwnd, allow)
)

const (
	dwmwaUseImmersiveDarkMode    = 20
	dwmwaUseImmersiveDarkModeOld = 19
	dwmwaWindowCornerPreference  = 33
	dwmwaSystemBackdropType      = 38
	dwmwcpRound                  = 2
	dwmsbtMainWindow             = 2 // Mica

	wmSettingChange   = 0x001A
	wmEraseBkgnd      = 0x0014
	wmPaint           = 0x000F
	wmDrawItem        = 0x002B
	wmPrintClient     = 0x0318
	wmCtlColorEdit    = 0x0133
	wmCtlColorListBox = 0x0134
	wmCtlColorBtn     = 0x0135
	wmCtlColorStatic  = 0x0138

	lvmSetBkColor     = lvmFirst + 1
	lvmGetHeader      = lvmFirst + 31
	lvmSetTextColor   = lvmFirst + 36
	lvmSetTextBkColor = lvmFirst + 38

	nmCustomDraw       = 0xFFFFFFF4 // NM_FIRST(0) - 12
	cddsPrepaint       = 0x1
	cddsItemPrepaint   = 0x10001
	cdrfDoDefault      = 0x0
	cdrfNotifyItemDraw = 0x20
	cdrfSkipDefault    = 0x4
	tbcdTics           = 1
	tbcdThumb          = 2
	tbcdChannel        = 3

	rdwInvalidate    = 0x1
	rdwErase         = 0x4
	rdwFrame         = 0x400
	rdwAllChildren   = 0x80
	appModeAllowDark = 1

	pbmSetBarColor = wmUser + 9
	pbmSetBkColor  = 0x2001 // CCM_SETBKCOLOR
	clrDefault     = 0xFFFFFFFF
	emSetMargins   = 0x00D3
	ecLeftMargin   = 0x1
	ecRightMargin  = 0x2

	bsOwnerDraw  = 0x0B
	odsSelected  = 0x0001
	odsDisabled  = 0x0004
	odsFocus     = 0x0010
	transparent  = 1
	dtCenter     = 0x0001
	dtVCenter    = 0x0004
	dtSingleLine = 0x0020
	psSolid      = 0
	hollowBrush  = 5
	cardRadius   = 8
	buttonRadius = 6
)

// rgb builds a COLORREF (0x00BBGGRR) from RGB components.
func rgb(r, g, b uint32) uintptr { return uintptr(r | g<<8 | b<<16) }

// palette is one colour scheme.
type palette struct {
	bg, card, cardBorder, input, text, textDim, accent, accentPressed, accentText,
	btn, btnBorder, btnPressed, channel, sel, toggleKnob uintptr
}

var darkPalette = palette{
	bg: rgb(0x20, 0x20, 0x20), card: rgb(0x2B, 0x2B, 0x2B), cardBorder: rgb(0x38, 0x38, 0x38),
	input: rgb(0x1F, 0x1F, 0x1F), text: rgb(0xFF, 0xFF, 0xFF), textDim: rgb(0xC0, 0xC0, 0xC0),
	accent: rgb(0x4C, 0xC2, 0xFF), accentPressed: rgb(0x3A, 0x9E, 0xD6), accentText: rgb(0x00, 0x1A, 0x2B),
	btn: rgb(0x37, 0x37, 0x37), btnBorder: rgb(0x45, 0x45, 0x45), btnPressed: rgb(0x2C, 0x2C, 0x2C),
	channel: rgb(0x55, 0x55, 0x55), sel: rgb(0x3D, 0x3D, 0x3D), toggleKnob: rgb(0xCC, 0xCC, 0xCC),
}

var lightPalette = palette{
	bg: rgb(0xF3, 0xF3, 0xF3), card: rgb(0xFB, 0xFB, 0xFB), cardBorder: rgb(0xE5, 0xE5, 0xE5),
	input: rgb(0xFF, 0xFF, 0xFF), text: rgb(0x1B, 0x1B, 0x1B), textDim: rgb(0x5D, 0x5D, 0x5D),
	accent: rgb(0x00, 0x5F, 0xB8), accentPressed: rgb(0x00, 0x4C, 0x94), accentText: rgb(0xFF, 0xFF, 0xFF),
	btn: rgb(0xFB, 0xFB, 0xFB), btnBorder: rgb(0xD6, 0xD6, 0xD6), btnPressed: rgb(0xF0, 0xF0, 0xF0),
	channel: rgb(0x8A, 0x8A, 0x8A), sel: rgb(0xE6, 0xE6, 0xE6), toggleKnob: rgb(0x5D, 0x5D, 0x5D),
}

// nmCustomDrawInfo mirrors NMCUSTOMDRAW.
type nmCustomDrawInfo struct {
	Hdr        nmhdr
	DrawStage  uint32
	_          uint32
	Hdc        uintptr
	Rc         rect
	ItemSpec   uintptr
	ItemState  uint32
	_          uint32
	ItemlParam uintptr
}

// drawItemStruct mirrors DRAWITEMSTRUCT.
type drawItemStruct struct {
	CtlType    uint32
	CtlID      uint32
	ItemID     uint32
	ItemAction uint32
	ItemState  uint32
	_          uint32
	HwndItem   uintptr
	Hdc        uintptr
	RcItem     rect
	ItemData   uintptr
}

// paintStruct mirrors PAINTSTRUCT.
type paintStruct struct {
	Hdc       uintptr
	Erase     int32
	RcPaint   rect
	Restore   int32
	IncUpdate int32
	Reserved  [32]byte
}

// theme holds the current mode, palette and GDI objects.
type theme struct {
	dark bool
	mica bool // DWM backdrop active: the parent leaves its background unpainted
	p    palette

	brBg, brCard, brInput, brBtn, brBtnPressed, brAccent, brAccentPressed, brChannel, brThumb, brSel, brToggleKnob uintptr
	penCard, penBtn, penAccent, penSel, penToggle                                                                  uintptr
}

func systemPrefersDark() bool {
	var val uint32
	if !regDword(`Software\Microsoft\Windows\CurrentVersion\Themes\Personalize`, "AppsUseLightTheme", &val) {
		return false
	}
	return val == 0
}

func regDword(path, name string, out *uint32) bool {
	p, _ := syscall.UTF16PtrFromString(path)
	var key syscall.Handle
	if err := syscall.RegOpenKeyEx(syscall.HKEY_CURRENT_USER, p, 0, syscall.KEY_QUERY_VALUE, &key); err != nil {
		return false
	}
	defer syscall.RegCloseKey(key)
	n, _ := syscall.UTF16PtrFromString(name)
	var typ uint32
	size := uint32(4)
	return syscall.RegQueryValueEx(key, n, nil, &typ, (*byte)(unsafe.Pointer(out)), &size) == nil
}

// initDarkModeSupport resolves the uxtheme ordinals and lets menus follow the
// system mode. Call once before creating windows.
func initDarkModeSupport() {
	h, err := syscall.LoadLibrary("uxtheme.dll")
	if err != nil {
		return
	}
	ord := func(n uintptr) uintptr {
		p, _, _ := procGetProcAddress.Call(uintptr(h), n)
		return p
	}
	uxSetPreferredAppMode = ord(135)
	uxFlushMenuThemes = ord(136)
	uxAllowDarkModeForWindow = ord(133)
	if uxSetPreferredAppMode != 0 {
		syscall.SyscallN(uxSetPreferredAppMode, appModeAllowDark)
	}
	if uxFlushMenuThemes != 0 {
		syscall.SyscallN(uxFlushMenuThemes)
	}
}

func loadTheme() *theme {
	t := &theme{}
	t.set(systemPrefersDark())
	return t
}

// set switches the mode and (re)creates the GDI objects.
func (t *theme) set(dark bool) {
	t.free()
	t.dark = dark
	t.p = lightPalette
	if dark {
		t.p = darkPalette
	}
	brush := func(c uintptr) uintptr { b, _, _ := procCreateSolidBrush.Call(c); return b }
	pen := func(c uintptr) uintptr { p, _, _ := procCreatePen.Call(psSolid, 1, c); return p }
	t.brBg, t.brCard, t.brInput = brush(t.p.bg), brush(t.p.card), brush(t.p.input)
	t.brBtn, t.brBtnPressed = brush(t.p.btn), brush(t.p.btnPressed)
	t.brAccent, t.brAccentPressed = brush(t.p.accent), brush(t.p.accentPressed)
	t.brChannel, t.brThumb = brush(t.p.channel), brush(t.p.accent)
	t.brSel, t.brToggleKnob = brush(t.p.sel), brush(t.p.toggleKnob)
	t.penCard, t.penBtn = pen(t.p.cardBorder), pen(t.p.btnBorder)
	t.penAccent, t.penSel, t.penToggle = pen(t.p.accent), pen(t.p.sel), pen(t.p.toggleKnob)
	if uxFlushMenuThemes != 0 {
		syscall.SyscallN(uxFlushMenuThemes)
	}
}

func (t *theme) free() {
	for _, o := range []*uintptr{&t.brBg, &t.brCard, &t.brInput, &t.brBtn, &t.brBtnPressed, &t.brAccent, &t.brAccentPressed, &t.brChannel, &t.brThumb, &t.brSel, &t.brToggleKnob, &t.penCard, &t.penBtn, &t.penAccent, &t.penSel, &t.penToggle} {
		if *o != 0 {
			procDeleteObject.Call(*o)
			*o = 0
		}
	}
}

// applyWindow sets title bar colour, rounded corners and the Mica backdrop.
func (t *theme) applyWindow(hwnd uintptr) {
	v := uint32(0)
	if t.dark {
		v = 1
	}
	if r, _, _ := procDwmSetWindowAttribute.Call(hwnd, dwmwaUseImmersiveDarkMode, uintptr(unsafe.Pointer(&v)), 4); r != 0 {
		procDwmSetWindowAttribute.Call(hwnd, dwmwaUseImmersiveDarkModeOld, uintptr(unsafe.Pointer(&v)), 4)
	}
	corner := uint32(dwmwcpRound)
	procDwmSetWindowAttribute.Call(hwnd, dwmwaWindowCornerPreference, uintptr(unsafe.Pointer(&corner)), 4)
	backdrop := uint32(dwmsbtMainWindow)
	r, _, _ := procDwmSetWindowAttribute.Call(hwnd, dwmwaSystemBackdropType, uintptr(unsafe.Pointer(&backdrop)), 4)
	t.mica = r == 0
	if uxAllowDarkModeForWindow != 0 {
		syscall.SyscallN(uxAllowDarkModeForWindow, hwnd, uintptr(v))
	}
}

func (t *theme) allowDark(hwnd uintptr) {
	if uxAllowDarkModeForWindow != 0 {
		allow := uintptr(0)
		if t.dark {
			allow = 1
		}
		syscall.SyscallN(uxAllowDarkModeForWindow, hwnd, allow)
	}
}

func setWindowTheme(hwnd uintptr, sub string) {
	if sub == "" {
		procSetWindowTheme.Call(hwnd, 0, 0)
		return
	}
	procSetWindowTheme.Call(hwnd, uintptr(unsafe.Pointer(utf16(sub))), 0)
}

// applyControl themes one child control according to its window class.
func (t *theme) applyControl(hwnd uintptr, class string) {
	t.allowDark(hwnd)
	prefix := ""
	if t.dark {
		prefix = "DarkMode_"
	}
	switch class {
	case "EDIT", "SysListView32", "BUTTON":
		setWindowTheme(hwnd, prefix+"Explorer")
	case "COMBOBOX":
		setWindowTheme(hwnd, prefix+"CFD")
	case "msctls_progress32":
		// The themed progress bar ignores colours: draw it flat ourselves.
		procSetWindowTheme.Call(hwnd, uintptr(unsafe.Pointer(utf16(""))), uintptr(unsafe.Pointer(utf16(""))))
		sendMessage(hwnd, pbmSetBkColor, 0, t.p.input)
		sendMessage(hwnd, pbmSetBarColor, 0, t.p.accent)
	}
	if class == "EDIT" {
		sendMessage(hwnd, emSetMargins, ecLeftMargin|ecRightMargin, 6|6<<16)
	}
	if class == "SysListView32" {
		sendMessage(hwnd, lvmSetExtStyle, lvsExGridlines, 0)
		sendMessage(hwnd, lvmSetBkColor, 0, t.p.input)
		sendMessage(hwnd, lvmSetTextBkColor, 0, t.p.input)
		sendMessage(hwnd, lvmSetTextColor, 0, t.p.text)
		if header := sendMessage(hwnd, lvmGetHeader, 0, 0); header != 0 {
			t.allowDark(header)
			hs := "Explorer"
			if t.dark {
				hs = "ItemsView"
			}
			setWindowTheme(header, hs)
		}
	}
}

// ctlColor answers WM_CTLCOLOR* : every static sits on a card, inputs are flat.
func (t *theme) ctlColor(msg uint32, hdc uintptr, dim bool) uintptr {
	switch msg {
	case wmCtlColorEdit, wmCtlColorListBox:
		procSetTextColor.Call(hdc, t.p.text)
		procSetBkColor.Call(hdc, t.p.input)
		return t.brInput
	default: // statics, button backgrounds
		c := t.p.text
		if dim {
			c = t.p.textDim
		}
		procSetTextColor.Call(hdc, c)
		procSetBkColor.Call(hdc, t.p.card)
		return t.brCard
	}
}

// eraseBackground handles WM_ERASEBKGND: with Mica the client stays unpainted
// so the backdrop shows between the cards; otherwise fill with the flat colour.
func (t *theme) eraseBackground(hwnd, hdc uintptr) {
	var rc rect
	procGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&rc)))
	procFillRect.Call(hdc, uintptr(unsafe.Pointer(&rc)), t.brBg)
}

// paintCards draws the rounded content panels.
func (t *theme) paintCards(hwnd uintptr, cards []rect) {
	var ps paintStruct
	hdc, _, _ := procBeginPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
	if hdc == 0 {
		return
	}
	oldPen, _, _ := procSelectObject.Call(hdc, t.penCard)
	oldBrush, _, _ := procSelectObject.Call(hdc, t.brCard)
	for _, c := range cards {
		procRoundRect.Call(hdc, uintptr(c.Left), uintptr(c.Top), uintptr(c.Right), uintptr(c.Bottom), cardRadius, cardRadius)
	}
	procSelectObject.Call(hdc, oldPen)
	procSelectObject.Call(hdc, oldBrush)
	procEndPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
}

// drawButton paints a flat rounded button for WM_DRAWITEM.
func (t *theme) drawButton(dis *drawItemStruct, primary bool, font uintptr) {
	hdc := dis.Hdc
	rc := dis.RcItem
	procFillRect.Call(hdc, uintptr(unsafe.Pointer(&rc)), t.brCard) // corners
	brush, pen, textColor := t.brBtn, t.penBtn, t.p.text
	if primary {
		brush, textColor = t.brAccent, t.p.accentText
		pen = t.penBtn
	}
	if dis.ItemState&odsSelected != 0 {
		brush = t.brBtnPressed
		if primary {
			brush = t.brAccentPressed
		}
	}
	if primary {
		// No visible border on the accent button: use the fill as pen colour.
		pen, _, _ = procCreatePen.Call(psSolid, 1, t.p.accent)
		defer procDeleteObject.Call(pen)
	}
	oldPen, _, _ := procSelectObject.Call(hdc, pen)
	oldBrush, _, _ := procSelectObject.Call(hdc, brush)
	procRoundRect.Call(hdc, uintptr(rc.Left), uintptr(rc.Top), uintptr(rc.Right-1), uintptr(rc.Bottom-1), buttonRadius, buttonRadius)
	procSelectObject.Call(hdc, oldPen)
	procSelectObject.Call(hdc, oldBrush)
	if dis.ItemState&odsDisabled != 0 {
		textColor = t.p.textDim
	}
	oldFont, _, _ := procSelectObject.Call(hdc, font)
	procSetBkMode.Call(hdc, transparent)
	procSetTextColor.Call(hdc, textColor)
	text, _ := syscall.UTF16FromString(getText(dis.HwndItem))
	procDrawTextW.Call(hdc, uintptr(unsafe.Pointer(&text[0])), uintptr(len(text)-1), uintptr(unsafe.Pointer(&rc)), dtCenter|dtVCenter|dtSingleLine)
	procSelectObject.Call(hdc, oldFont)
}

// hdItemW mirrors HDITEMW.
type hdItemW struct {
	Mask    uint32
	Cxy     int32
	Text    *uint16
	Bitmap  uintptr
	TextMax int32
	Fmt     int32
	LParam  uintptr
	Image   int32
	Order   int32
	Type    uint32
	Filter  uintptr
	State   uint32
}

const (
	hdmGetItemW = 0x1200 + 11
	hdiText     = 0x2
	dtLeft      = 0x0
	dtEndEllips = 0x8000
)

// headerCustomDraw paints list view headers flat: list background, dim
// caption, hairline separator. The themed header ignores colours.
func (t *theme) headerCustomDraw(l uintptr, font uintptr) uintptr {
	cd := (*nmCustomDrawInfo)(unsafe.Pointer(l))
	switch cd.DrawStage {
	case cddsPrepaint:
		procFillRect.Call(cd.Hdc, uintptr(unsafe.Pointer(&cd.Rc)), t.brInput)
		return cdrfNotifyItemDraw
	case cddsItemPrepaint:
		rc := cd.Rc
		procFillRect.Call(cd.Hdc, uintptr(unsafe.Pointer(&rc)), t.brInput)
		sep := rect{rc.Right - 1, rc.Top + 6, rc.Right, rc.Bottom - 6}
		procFillRect.Call(cd.Hdc, uintptr(unsafe.Pointer(&sep)), t.brChannel)
		var buf [256]uint16
		item := hdItemW{Mask: hdiText, Text: &buf[0], TextMax: int32(len(buf))}
		sendMessage(cd.Hdr.HwndFrom, hdmGetItemW, cd.ItemSpec, uintptr(unsafe.Pointer(&item)))
		text := utf16PtrToString(&buf[0])
		u, _ := syscall.UTF16FromString(text)
		oldFont, _, _ := procSelectObject.Call(cd.Hdc, font)
		procSetBkMode.Call(cd.Hdc, transparent)
		procSetTextColor.Call(cd.Hdc, t.p.textDim)
		rc.Left += 6
		rc.Right -= 6
		procDrawTextW.Call(cd.Hdc, uintptr(unsafe.Pointer(&u[0])), uintptr(len(u)-1), uintptr(unsafe.Pointer(&rc)), dtLeft|dtVCenter|dtSingleLine|dtEndEllips)
		procSelectObject.Call(cd.Hdc, oldFont)
		return cdrfSkipDefault
	}
	return cdrfDoDefault
}

// trackbarCustomDraw paints the volume slider flat.
func (t *theme) trackbarCustomDraw(l uintptr) uintptr {
	cd := (*nmCustomDrawInfo)(unsafe.Pointer(l))
	switch cd.DrawStage {
	case cddsPrepaint:
		return cdrfNotifyItemDraw
	case cddsItemPrepaint:
		switch cd.ItemSpec {
		case tbcdChannel:
			rc := cd.Rc
			mid := (rc.Top + rc.Bottom) / 2
			rc.Top, rc.Bottom = mid-2, mid+2
			procFillRect.Call(cd.Hdc, uintptr(unsafe.Pointer(&rc)), t.brChannel)
			return cdrfSkipDefault
		case tbcdThumb:
			rc := cd.Rc
			oldPen, _, _ := procSelectObject.Call(cd.Hdc, t.penCard)
			oldBrush, _, _ := procSelectObject.Call(cd.Hdc, t.brThumb)
			procRoundRect.Call(cd.Hdc, uintptr(rc.Left), uintptr(rc.Top), uintptr(rc.Right), uintptr(rc.Bottom), 6, 6)
			procSelectObject.Call(cd.Hdc, oldPen)
			procSelectObject.Call(cd.Hdc, oldBrush)
			return cdrfSkipDefault
		case tbcdTics:
			return cdrfSkipDefault
		}
	}
	return cdrfDoDefault
}

func redrawAll(hwnd uintptr) {
	procRedrawWindow.Call(hwnd, 0, 0, rdwInvalidate|rdwErase|rdwFrame|rdwAllChildren)
}

// isImmersiveColorSet reports whether a WM_SETTINGCHANGE is a theme change.
func isImmersiveColorSet(l uintptr) bool {
	if l == 0 {
		return false
	}
	return utf16PtrToString((*uint16)(unsafe.Pointer(l))) == "ImmersiveColorSet"
}

// hasFont reports whether a font family is installed (checks the Fonts registry).
func hasFont(displayName string) bool {
	p, _ := syscall.UTF16PtrFromString(`SOFTWARE\Microsoft\Windows NT\CurrentVersion\Fonts`)
	var key syscall.Handle
	if err := syscall.RegOpenKeyEx(syscall.HKEY_LOCAL_MACHINE, p, 0, syscall.KEY_QUERY_VALUE, &key); err != nil {
		return false
	}
	defer syscall.RegCloseKey(key)
	n, _ := syscall.UTF16PtrFromString(displayName)
	var typ, size uint32
	return syscall.RegQueryValueEx(key, n, nil, &typ, nil, &size) == nil
}

// uiFontFace picks Segoe UI Variable on Windows 11, Segoe UI elsewhere.
func uiFontFace(display bool) string {
	if hasFont("Segoe UI Variable (TrueType)") {
		if display {
			return "Segoe UI Variable Display"
		}
		return "Segoe UI Variable Text"
	}
	return "Segoe UI"
}

// ---- list view subclass ------------------------------------------------------------
//
// The header control is a child of the list view, so its NM_CUSTOMDRAW goes to
// the list view and never reaches our window. Subclass every list view and
// answer the header's custom draw there.

var (
	procSetWindowSubclass = comctl32.NewProc("SetWindowSubclass")
	procDefSubclassProc   = comctl32.NewProc("DefSubclassProc")
	lvSubclassOnce        sync.Once
	lvSubclassPtr         uintptr
)

func lvSubclassProc(hwnd, m, w, l, id, ref uintptr) uintptr {
	if m == wmNotify && theGUI != nil {
		hdr := (*nmhdr)(unsafe.Pointer(l))
		if hdr.Code == nmCustomDraw && hdr.HwndFrom == sendMessage(hwnd, lvmGetHeader, 0, 0) {
			return theGUI.theme.headerCustomDraw(l, theGUI.font)
		}
	}
	r, _, _ := procDefSubclassProc.Call(hwnd, m, w, l)
	return r
}

// subclassListView installs the header-drawing subclass once per control.
func subclassListView(hwnd uintptr) {
	lvSubclassOnce.Do(func() { lvSubclassPtr = syscall.NewCallback(lvSubclassProc) })
	procSetWindowSubclass.Call(hwnd, lvSubclassPtr, 1, 0)
}

// drawMeter paints the output level meter (owner-drawn static): flat track
// with an accent bar. level is 0..1.
func (t *theme) drawMeter(dis *drawItemStruct, level float64) {
	rc := dis.RcItem
	procFillRect.Call(dis.Hdc, uintptr(unsafe.Pointer(&rc)), t.brCard)
	oldPen, _, _ := procSelectObject.Call(dis.Hdc, t.penCard)
	oldBrush, _, _ := procSelectObject.Call(dis.Hdc, t.brInput)
	procRoundRect.Call(dis.Hdc, uintptr(rc.Left), uintptr(rc.Top), uintptr(rc.Right), uintptr(rc.Bottom), 6, 6)
	if level > 0 {
		w := int32(float64(rc.Right-rc.Left-4) * level)
		if w < 4 {
			w = 4
		}
		bar := rect{rc.Left + 2, rc.Top + 2, rc.Left + 2 + w, rc.Bottom - 2}
		procSelectObject.Call(dis.Hdc, t.brAccent)
		pen, _, _ := procCreatePen.Call(psSolid, 1, t.p.accent)
		procSelectObject.Call(dis.Hdc, pen)
		procRoundRect.Call(dis.Hdc, uintptr(bar.Left), uintptr(bar.Top), uintptr(bar.Right), uintptr(bar.Bottom), 4, 4)
		procSelectObject.Call(dis.Hdc, oldPen)
		procDeleteObject.Call(pen)
	}
	procSelectObject.Call(dis.Hdc, oldPen)
	procSelectObject.Call(dis.Hdc, oldBrush)
}
