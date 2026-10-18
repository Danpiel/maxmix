package main

// WinUI-look drawing primitives on GDI: navigation items, settings card rows,
// toggles, dropdown buttons and owner-drawn list rows. Used by settings.go.

import (
	"strings"
	"syscall"
	"unsafe"
)

var (
	procGetWindowRectW = user32.NewProc("GetWindowRect")
	procScreenToClient = user32.NewProc("ScreenToClient")
)

const (
	wmMeasureItem     = 0x002C
	lvsOwnerDrawFixed = 0x0400
	lvmGetColumnWidth = lvmFirst + 29
	lvmGetItemTextW   = lvmFirst + 115
	odtButton         = 4
	odtListView       = 102
	tpmReturnCmd      = 0x0100
	tpmLeftAlign      = 0x0000
	dtRight           = 0x0002
	dtWordBreak       = 0x0010
	dtNoPrefix        = 0x0800
	rowHeight         = 30 // owner-drawn list rows

	glyphGame     = ""
	glyphVolume   = ""
	glyphSpeaker  = ""
	glyphSettings = ""
	glyphRecord   = ""
	glyphInfo     = ""
	glyphPower    = ""
	glyphChevron  = ""
	glyphLog      = ""
	glyphDownload = ""
	glyphStartup  = ""
	glyphMeter    = ""
	glyphLanguage = ""
	glyphHeadset  = ""
	glyphCheck    = ""
	glyphRoute    = ""
)

// measureItemStruct mirrors MEASUREITEMSTRUCT.
type measureItemStruct struct {
	CtlType    uint32
	CtlID      uint32
	ItemID     uint32
	ItemWidth  uint32
	ItemHeight uint32
	_          uint32
	ItemData   uintptr
}

// fonts used by the WinUI look.
type uiFonts struct {
	body, bold, title, small, glyph, glyphSmall uintptr
}

func makeFont(face string, px int, weight int) uintptr {
	f, _, _ := procCreateFontW.Call(^uintptr(px-1), 0, 0, 0, uintptr(weight), 0, 0, 0, 1, 0, 0, 5, 0, uintptr(unsafe.Pointer(utf16(face))))
	return f
}

func loadFonts() uiFonts {
	text, display := uiFontFace(false), uiFontFace(true)
	glyphFace := "Segoe MDL2 Assets"
	if hasFont("Segoe Fluent Icons (TrueType)") {
		glyphFace = "Segoe Fluent Icons"
	}
	return uiFonts{
		body:       makeFont(text, 14, 400),
		bold:       makeFont(display, 15, 600),
		title:      makeFont(display, 28, 600),
		small:      makeFont(text, 12, 400),
		glyph:      makeFont(glyphFace, 20, 400),
		glyphSmall: makeFont(glyphFace, 12, 400),
	}
}

// drawText draws s inside rc with the given DT_ flags, font and colour.
func drawText(hdc uintptr, s string, rc rect, flags uint32, font, color uintptr) {
	if s == "" {
		return
	}
	u, _ := syscall.UTF16FromString(s)
	old, _, _ := procSelectObject.Call(hdc, font)
	procSetBkMode.Call(hdc, transparent)
	procSetTextColor.Call(hdc, color)
	procDrawTextW.Call(hdc, uintptr(unsafe.Pointer(&u[0])), uintptr(len(u)-1), uintptr(unsafe.Pointer(&rc)), uintptr(flags|dtNoPrefix))
	procSelectObject.Call(hdc, old)
}

func fillRound(hdc uintptr, rc rect, brush, pen uintptr, r int) {
	oldPen, _, _ := procSelectObject.Call(hdc, pen)
	oldBrush, _, _ := procSelectObject.Call(hdc, brush)
	procRoundRect.Call(hdc, uintptr(rc.Left), uintptr(rc.Top), uintptr(rc.Right), uintptr(rc.Bottom), uintptr(r), uintptr(r))
	procSelectObject.Call(hdc, oldPen)
	procSelectObject.Call(hdc, oldBrush)
}

// cardRow is one settings row: icon, title, description and a control area.
type cardRow struct {
	rc    rect
	glyph string
	title string
	desc  *string // dynamic text, repainted on change
	ctlW  int32   // width reserved for the control on the right (0 = 360)
}

// paintCard draws a card row (or a plain card when title is empty).
func (t *theme) paintCard(hdc uintptr, c cardRow, f uiFonts) {
	fillRound(hdc, c.rc, t.brCard, t.penCard, cardRadius)
	if c.title == "" {
		return
	}
	x := c.rc.Left + 20
	if c.glyph != "" {
		drawText(hdc, c.glyph, rect{x, c.rc.Top, x + 28, c.rc.Bottom}, dtLeft|dtVCenter|dtSingleLine, f.glyph, t.p.text)
		x += 44
	}
	desc := ""
	if c.desc != nil {
		desc = *c.desc
	}
	ctlW := c.ctlW
	if ctlW == 0 {
		ctlW = 360
	}
	right := c.rc.Right - ctlW // control area
	if desc == "" {
		drawText(hdc, c.title, rect{x, c.rc.Top, right, c.rc.Bottom}, dtLeft|dtVCenter|dtSingleLine, f.body, t.p.text)
		return
	}
	// Title on the first line, description below; DT_WORDBREAK alone wraps at
	// spaces (adding DT_END_ELLIPSIS makes DrawText break inside words).
	drawText(hdc, c.title, rect{x, c.rc.Top + 13, right, c.rc.Top + 33}, dtLeft|dtVCenter|dtSingleLine, f.body, t.p.text)
	drawWrapped(hdc, desc, rect{x, c.rc.Top + 35, right, c.rc.Bottom - 6}, f.small, t.p.textDim, 16)
}

// drawNav paints a navigation item: pill when selected, accent bar, glyph, text.
func (t *theme) drawNav(dis *drawItemStruct, glyph, text string, selected bool, f uiFonts) {
	rc := dis.RcItem
	// Background: leave Mica, or fill with window colour when there is none.
	if t.mica {
		procFillRect.Call(dis.Hdc, uintptr(unsafe.Pointer(&rc)), t.brBg)
	} else {
		procFillRect.Call(dis.Hdc, uintptr(unsafe.Pointer(&rc)), t.brBg)
	}
	if selected || dis.ItemState&odsSelected != 0 {
		fillRound(dis.Hdc, rc, t.brCard, t.penCard, 6)
	}
	if selected {
		bar := rect{rc.Left, (rc.Top+rc.Bottom)/2 - 8, rc.Left + 3, (rc.Top+rc.Bottom)/2 + 8}
		fillRound(dis.Hdc, bar, t.brAccent, t.penAccent, 2)
	}
	drawText(dis.Hdc, glyph, rect{rc.Left + 14, rc.Top, rc.Left + 40, rc.Bottom}, dtLeft|dtVCenter|dtSingleLine, f.glyph, t.p.text)
	drawText(dis.Hdc, text, rect{rc.Left + 46, rc.Top, rc.Right - 8, rc.Bottom}, dtLeft|dtVCenter|dtSingleLine, f.body, t.p.text)
}

// drawToggle paints a WinUI toggle switch with its On/Off caption to the left.
func (t *theme) drawToggle(dis *drawItemStruct, on bool, f uiFonts) {
	rc := dis.RcItem
	procFillRect.Call(dis.Hdc, uintptr(unsafe.Pointer(&rc)), t.brCard)
	const w, h = 40, 20
	track := rect{rc.Right - w - 2, (rc.Top+rc.Bottom)/2 - h/2, rc.Right - 2, (rc.Top+rc.Bottom)/2 + h/2}
	label := tr("Выкл")
	if on {
		label = tr("Вкл")
		fillRound(dis.Hdc, track, t.brAccent, t.penAccent, h)
		knob := rect{track.Right - 16, track.Top + 4, track.Right - 4, track.Bottom - 4}
		fillRound(dis.Hdc, knob, t.brCard, t.penCard, 12)
	} else {
		fillRound(dis.Hdc, track, t.brCard, t.penToggle, h)
		knob := rect{track.Left + 4, track.Top + 4, track.Left + 16, track.Bottom - 4}
		fillRound(dis.Hdc, knob, t.brToggleKnob, t.penToggle, 12)
	}
	drawText(dis.Hdc, label, rect{rc.Left, rc.Top, track.Left - 10, rc.Bottom}, dtRight|dtVCenter|dtSingleLine, f.body, t.p.text)
}

// drawDropdown paints a combo-box-like button: text left, chevron right.
func (t *theme) drawDropdown(dis *drawItemStruct, text string, f uiFonts) {
	rc := dis.RcItem
	procFillRect.Call(dis.Hdc, uintptr(unsafe.Pointer(&rc)), t.brCard)
	brush := t.brBtn
	if dis.ItemState&odsSelected != 0 {
		brush = t.brBtnPressed
	}
	fillRound(dis.Hdc, rect{rc.Left, rc.Top, rc.Right - 1, rc.Bottom - 1}, brush, t.penBtn, buttonRadius)
	drawText(dis.Hdc, text, rect{rc.Left + 10, rc.Top, rc.Right - 30, rc.Bottom}, dtLeft|dtVCenter|dtSingleLine|dtEndEllips, f.body, t.p.text)
	drawText(dis.Hdc, glyphChevron, rect{rc.Right - 26, rc.Top, rc.Right - 8, rc.Bottom}, dtCenter|dtVCenter|dtSingleLine, f.glyphSmall, t.p.textDim)
}

// drawListRow paints one owner-drawn report row of a list view.
func (t *theme) drawListRow(dis *drawItemStruct, f uiFonts) {
	lv := dis.HwndItem
	rc := dis.RcItem
	bg := t.brInput
	if dis.ItemState&odsSelected != 0 {
		bg = t.brSel
	}
	procFillRect.Call(dis.Hdc, uintptr(unsafe.Pointer(&rc)), t.brInput)
	if dis.ItemState&odsSelected != 0 {
		fillRound(dis.Hdc, rect{rc.Left + 2, rc.Top + 1, rc.Right - 2, rc.Bottom - 1}, bg, t.penSel, 5)
	}
	x := rc.Left
	var buf [512]uint16
	for col := 0; ; col++ {
		w := int32(sendMessage(lv, lvmGetColumnWidth, uintptr(col), 0))
		if w <= 0 {
			break
		}
		item := lvItemW{SubItem: int32(col), Text: &buf[0], TextMax: int32(len(buf))}
		n := sendMessage(lv, lvmGetItemTextW, uintptr(dis.ItemID), uintptr(unsafe.Pointer(&item)))
		text := syscall.UTF16ToString(buf[:n])
		color := t.p.text
		if col > 0 {
			color = t.p.textDim
		}
		drawText(dis.Hdc, text, rect{x + 8, rc.Top, x + w - 6, rc.Bottom}, dtLeft|dtVCenter|dtSingleLine|dtEndEllips, f.body, color)
		x += w
	}
}

// popupMenu shows a dark context menu under the control and returns the chosen
// index or -1.
func popupMenu(owner, anchor uintptr, items []string) int {
	if len(items) == 0 {
		return -1
	}
	menu, _, _ := procCreatePopupMenu.Call()
	defer procDestroyMenu.Call(menu)
	for i, it := range items {
		procAppendMenuW.Call(menu, mfString, uintptr(i+1), uintptr(unsafe.Pointer(utf16(it))))
	}
	var rc rect
	procGetWindowRectW.Call(anchor, uintptr(unsafe.Pointer(&rc)))
	r, _, _ := procTrackPopupMenu.Call(menu, tpmLeftAlign|tpmReturnCmd, uintptr(rc.Left), uintptr(rc.Bottom+2), 0, owner, 0)
	return int(r) - 1
}

var procGetTextExtentPoint32W = gdi32.NewProc("GetTextExtentPoint32W")

type size struct{ Cx, Cy int32 }

// textWidth measures s with the font currently selected into hdc.
func textWidth(hdc uintptr, s string) int32 {
	u, _ := syscall.UTF16FromString(s)
	var sz size
	procGetTextExtentPoint32W.Call(hdc, uintptr(unsafe.Pointer(&u[0])), uintptr(len(u)-1), uintptr(unsafe.Pointer(&sz)))
	return sz.Cx
}

// drawWrapped word-wraps s into rc (explicit "\n" breaks too) and draws it
// line by line, clipping lines that do not fit vertically.
func drawWrapped(hdc uintptr, s string, rc rect, font, color uintptr, lineH int32) {
	old, _, _ := procSelectObject.Call(hdc, font)
	width := rc.Right - rc.Left
	var lines []string
	for _, para := range strings.Split(s, "\n") {
		line := ""
		for _, word := range strings.Fields(para) {
			cand := word
			if line != "" {
				cand = line + " " + word
			}
			if line != "" && textWidth(hdc, cand) > width {
				lines = append(lines, line)
				line = word
			} else {
				line = cand
			}
		}
		lines = append(lines, line)
	}
	y := rc.Top
	for _, l := range lines {
		if y+lineH > rc.Bottom+2 {
			break
		}
		drawText(hdc, l, rect{rc.Left, y, rc.Right, y + lineH}, dtLeft|dtSingleLine|dtEndEllips, font, color)
		y += lineH
	}
	procSelectObject.Call(hdc, old)
}
