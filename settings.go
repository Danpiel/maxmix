package main

// The settings window in the WinUI 3 / "Mica For Everyone" layout: navigation
// on the left, one page at a time on the right, settings as card rows with an
// icon, a title, a description and a control. Every visual is owner-drawn on
// GDI; the only stock controls left are edits, list views and the trackbar,
// all recoloured. Data binding to Config and the live mixer state lives here.

import (
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"unsafe"
)

const (
	winW, winH = 980, 640 // client size
	navW       = 220
	px         = 240 // right pane left
	pw         = winW - px - 24
	pRight     = px + pw
	contentY   = 72

	idNavFirst    = 1100 // + page index
	idToggleGame  = 1201
	idToggleStart = 1202
	idDDKind      = 1203
	idDDTarget    = 1204
	idValueMenu   = 1205
	idEdValue     = 1206
	idBtnLog      = 1207
	idBtnCable    = 1208
	idBtnExit     = 1209
	idDDLang      = 1210
	idBtnCheck    = 1211
	idCheckList   = 1212
	idDDMonitor   = 1213
	idMonitorGain = 1214
)

type page int

const (
	pageInputs page = iota
	pageSessions
	pageOutput
	pageCheck
	pageGeneral
	pageCount
)

var pageInfo = [pageCount]struct{ glyph, title string }{
	{glyphGame, "Входы"},
	{glyphVolume, "Звучит сейчас"},
	{glyphSpeaker, "Выход и запись"},
	{glyphCheck, "Проверка"},
	{glyphSettings, "Общие"},
}

type sessionRow struct {
	PID      uint32
	Exe      string
	Device   string
	State    string
	Captured bool
}

type ctlRef struct {
	hwnd  uintptr
	class string
}

type settingsWin struct {
	hwnd    uintptr
	cfg     Config
	loading bool
	devices []string
	micDevs []string
	exes    []string
	rows    []sessionRow
	rowsSig string
	sel     int
	page    page

	ctls     []ctlRef
	pageCtls [pageCount][]uintptr
	cards    [pageCount][]cardRow
	bar      rect
	nav      [pageCount]uintptr
	dim      map[uintptr]bool
	primary  map[uintptr]bool
	toggles  map[uintptr]bool
	ddText   map[uintptr]string

	gameDesc, routeDesc, cableDesc, checkDesc, msg string
	outLevel                                       float64

	checkMu      sync.Mutex
	checkRows    []check // self-check report, filled by a worker goroutine
	checkRunning bool
	checkSig     string // what the list view currently shows

	lvSources, lvSessions, lvCheck, tbGain, stGain, edValue, edExclude, edApps, lblValue, lblExclude uintptr
	ddKind, ddTarget, ddLang, ddMonitor, btnValue, tgGame, tgStart, meter, tbMonitor, stMonitor      uintptr
}

// checkHint is the description of the self-check card.
func (s *settingsWin) checkHint() string {
	s.checkMu.Lock()
	defer s.checkMu.Unlock()
	if s.checkRunning {
		return tr("Идёт проверка: включите звук в игре и скажите что-нибудь в микрофон")
	}
	return tr("Замеряет звук каждого входа, микса и микрофона и пишет образцы в WAV")
}

func (g *gui) openSettings() {
	if g.set != nil {
		showWindow(g.set.hwnd, swShow)
		procSetForegroundWindow.Call(g.set.hwnd)
		return
	}
	s := &settingsWin{cfg: g.m.config(), sel: -1, dim: map[uintptr]bool{}, primary: map[uintptr]bool{},
		toggles: map[uintptr]bool{}, ddText: map[uintptr]string{}}
	g.set = s
	style := uintptr(wsOverlapped | wsCaption | wsSysMenu | wsMinimizeBox)
	rc := rect{0, 0, winW, winH}
	procAdjustWindowRect.Call(uintptr(unsafe.Pointer(&rc)), style, 0)
	sw, _, _ := procGetSystemMetrics.Call(0)
	sh, _, _ := procGetSystemMetrics.Call(1)
	w, h := int(rc.Right-rc.Left), int(rc.Bottom-rc.Top)
	hwnd, _, err := procCreateWindowExW.Call(0, uintptr(unsafe.Pointer(utf16("maxmixSettings"))), uintptr(unsafe.Pointer(utf16("maxmix"))),
		style, uintptr((int(sw)-w)/2), uintptr((int(sh)-h)/2), uintptr(w), uintptr(h), 0, 0, 0, 0)
	if hwnd == 0 {
		log.Printf("settings window: %v", err)
		g.set = nil
		return
	}
	s.hwnd = hwnd
	g.theme.applyWindow(hwnd)
	s.build(g)
	s.applyTheme(g.theme)
	s.fillSources()
	s.loadMonitor()
	s.showPage(pageInputs)
	s.refresh(g.m)
	s.refreshRoute()
	if len(s.cfg.Sources) > 0 {
		lvSelect(s.lvSources, 0)
	}
	procSetTimer.Call(hwnd, idTimer, refreshMS, 0)
	showWindow(hwnd, swShow)
	procSetForegroundWindow.Call(hwnd)
}

func (s *settingsWin) ctl(p page, class, text string, style uint32, x, y, w, h int, id int, font uintptr) uintptr {
	hwnd := createControl(s.hwnd, class, text, style, x, y, w, h, id, font)
	s.ctls = append(s.ctls, ctlRef{hwnd, class})
	if p < pageCount {
		s.pageCtls[p] = append(s.pageCtls[p], hwnd)
	}
	return hwnd
}

func (s *settingsWin) applyTheme(t *theme) {
	t.applyWindow(s.hwnd)
	for _, c := range s.ctls {
		t.applyControl(c.hwnd, c.class)
		if c.class == "SysListView32" {
			subclassListView(c.hwnd)
		}
	}
	redrawAll(s.hwnd)
}

// showPage switches the visible page and repaints.
func (s *settingsWin) showPage(p page) {
	s.page = p
	for i := page(0); i < pageCount; i++ {
		cmd := swHide
		if i == p {
			cmd = swShow
		}
		for _, h := range s.pageCtls[i] {
			showWindow(h, cmd)
		}
		procInvalidateRect.Call(s.nav[i], 0, 1)
	}
	if p == pageInputs {
		s.loadEditor(s.sel) // editor-dependent visibility
	}
	procInvalidateRect.Call(s.hwnd, 0, 1)
}

// build creates every control of every page (hidden until shown).
func (s *settingsWin) build(g *gui) {
	f := g.fonts
	all := pageCount // controls shared by all pages
	row := func(p page, y, h int, glyph, title string, desc *string) rect {
		rc := rect{px, int32(y), pRight, int32(y + h)}
		s.cards[p] = append(s.cards[p], cardRow{rc: rc, glyph: glyph, title: title, desc: desc})
		return rc
	}
	card := func(p page, y, h int) rect {
		rc := rect{px, int32(y), pRight, int32(y + h)}
		s.cards[p] = append(s.cards[p], cardRow{rc: rc})
		return rc
	}
	lbl := func(p page, text string, x, y, w int, dim bool) uintptr {
		h := s.ctl(p, "STATIC", text, ssLeftNoWrap, x, y, w, 20, 0, f.body)
		if dim {
			s.dim[h] = true
		}
		return h
	}
	btn := func(p page, text string, x, y, w int, id int, primary bool) uintptr {
		h := s.ctl(p, "BUTTON", text, bsOwnerDraw|wsTabStop, x, y, w, 30, id, f.body)
		if primary {
			s.primary[h] = true
		}
		return h
	}
	toggle := func(p page, y int, id int, on bool) uintptr {
		h := s.ctl(p, "BUTTON", "", bsOwnerDraw|wsTabStop, pRight-16-120, y, 120, 30, id, f.body)
		s.toggles[h] = on
		return h
	}
	dropdown := func(p page, x, y, w int, id int, text string) uintptr {
		h := s.ctl(p, "BUTTON", "", bsOwnerDraw|wsTabStop, x, y, w, 30, id, f.body)
		s.ddText[h] = text
		return h
	}
	list := func(p page, x, y, w, h int, id int) uintptr {
		lv := s.ctl(p, "SysListView32", "", lvsReport|lvsSingleSel|lvsShowSelAlways|lvsNoSortHeader|lvsOwnerDrawFixed|wsTabStop, x, y, w, h, id, f.body)
		sendMessage(lv, lvmSetExtStyle, lvsExFullRowSel, lvsExFullRowSel)
		return lv
	}

	// Navigation.
	for i := page(0); i < pageCount; i++ {
		s.nav[i] = s.ctl(all, "BUTTON", tr(pageInfo[i].title), bsOwnerDraw|wsTabStop, 12, 84+int(i)*44, navW-24, 40, idNavFirst+int(i), f.body)
	}

	// ---- Inputs page ----
	p := pageInputs
	s.gameDesc = tr("Игра не найдена")
	row(p, contentY, 72, glyphGame, tr("Определять игру автоматически"), &s.gameDesc)
	s.tgGame = toggle(p, contentY+21, idToggleGame, false)

	c := card(p, contentY+84, 188)
	s.lvSources = list(p, int(c.Left)+16, int(c.Top)+16, pw-32, 120, idSources)
	lvAddColumn(s.lvSources, 0, tr("Тип"), 130)
	lvAddColumn(s.lvSources, 1, tr("Значение"), 190)
	lvAddColumn(s.lvSources, 2, tr("Громкость"), 90)
	lvAddColumn(s.lvSources, 3, tr("Сейчас захвачено"), pw-32-130-190-90-4)
	btn(p, tr("Добавить"), int(c.Left)+16, int(c.Top)+144, 110, idAdd, false)
	btn(p, tr("Удалить"), int(c.Left)+134, int(c.Top)+144, 110, idDel, false)

	c = card(p, contentY+284, 216)
	y := int(c.Top) + 16
	x := int(c.Left) + 16
	lbl(p, tr("Тип"), x, y+6, 40, true)
	s.ddKind = dropdown(p, x+44, y, 180, idDDKind, "")
	s.lblValue = lbl(p, tr("Значение"), x+244, y+6, 70, true)
	s.edValue = s.ctl(p, "EDIT", "", esAutoHScroll|wsTabStop, x+320, y+3, 326, 24, idEdValue, f.body)
	s.btnValue = s.ctl(p, "BUTTON", "", bsOwnerDraw|wsTabStop, x+650, y, 34, 30, idValueMenu, f.body)
	s.ddText[s.btnValue] = ""
	y += 42
	lbl(p, tr("Громкость"), x, y+6, 80, true)
	s.tbGain = s.ctl(p, "msctls_trackbar32", "", tbsNoTicks|wsTabStop, x+84, y, 220, 28, idGain, 0)
	sendMessage(s.tbGain, tbmSetRange, 1, 200<<16)
	s.stGain = lbl(p, "100%", x+312, y+6, 60, false)
	y += 42
	s.lblExclude = lbl(p, tr("Исключить из авто-режима (через запятую)"), x, y, 400, true)
	s.edExclude = s.ctl(p, "EDIT", "", esMultiline|esAutoVScroll|wsVScroll|wsTabStop, x, y+22, pw-32, 76, idExclude, f.body)

	// ---- Sessions page ----
	p = pageSessions
	c = card(p, contentY, 500)
	s.lvSessions = list(p, int(c.Left)+16, int(c.Top)+16, pw-32, 420, idSessions)
	lvAddColumn(s.lvSessions, 0, tr("Процесс"), 220)
	lvAddColumn(s.lvSessions, 1, "PID", 70)
	lvAddColumn(s.lvSessions, 2, tr("Устройство"), 240)
	lvAddColumn(s.lvSessions, 3, tr("Статус"), 80)
	lvAddColumn(s.lvSessions, 4, tr("В записи"), pw-32-220-70-240-80-4)
	btn(p, tr("Добавить как вход"), int(c.Left)+16, int(c.Top)+452, 180, idAddProc, false)
	btn(p, tr("Исключить из авто"), int(c.Left)+204, int(c.Top)+452, 180, idExcl, false)

	// ---- Output page ----
	p = pageOutput
	deviceDesc := tr("Микс играет сюда, Steam и NVIDIA пишут с этого устройства")
	row(p, contentY, 72, glyphSpeaker, tr("Виртуальное устройство вывода"), &deviceDesc)
	s.ddTarget = dropdown(p, pRight-16-340, contentY+21, 340, idDDTarget, "") // filled by refresh
	row(p, contentY+84, 60, glyphMeter, tr("Уровень микса"), nil)
	s.meter = s.ctl(p, "STATIC", "", ssOwnerDraw, pRight-16-340, contentY+84+22, 340, 16, idOutMeter, 0)
	appsDesc := tr("Процессы, чьё устройство по умолчанию подменяется на выход")
	row(p, contentY+156, 72, glyphRecord, tr("Программы записи"), &appsDesc)
	s.edApps = s.ctl(p, "EDIT", strings.Join(s.cfg.Apps, ", "), esAutoHScroll|wsTabStop, pRight-16-340, contentY+156+24, 340, 24, idApps, f.body)
	rr := row(p, contentY+240, 104, glyphRoute, tr("Переназначение"), &s.routeDesc)
	s.cards[p][len(s.cards[p])-1].ctlW = 270
	btn(p, tr("Направить на выход"), int(rr.Right)-16-250, int(rr.Top)+21, 160, idRoute, false)
	btn(p, tr("Снять"), int(rr.Right)-16-82, int(rr.Top)+21, 82, idUnroute, false)

	// ---- Check page ----
	p = pageCheck
	s.checkDesc = s.checkHint()
	cr := row(p, contentY, 72, glyphCheck, tr("Проверка записи"), &s.checkDesc)
	s.cards[p][len(s.cards[p])-1].ctlW = 200
	btn(p, tr("Запустить"), int(cr.Right)-16-180, int(cr.Top)+21, 180, idBtnCheck, true)

	monDesc := tr("Только на время проверки, не постоянно")
	mr := row(p, contentY+84, 72, glyphHeadset, tr("Прослушивать микрофон"), &monDesc)
	s.cards[p][len(s.cards[p])-1].ctlW = 470
	mx := int(mr.Right) - 16 - 446
	my := int(mr.Top) + 21
	s.ddMonitor = dropdown(p, mx, my, 260, idDDMonitor, "")
	s.tbMonitor = s.ctl(p, "msctls_trackbar32", "", tbsNoTicks|wsTabStop, mx+268, my, 120, 28, idMonitorGain, 0)
	sendMessage(s.tbMonitor, tbmSetRange, 1, 200<<16)
	s.stMonitor = lbl(p, "100%", mx+396, my+6, 50, false)

	c = card(p, contentY+168, 296)
	s.lvCheck = list(p, int(c.Left)+16, int(c.Top)+16, pw-32, 264, idCheckList)
	lvAddColumn(s.lvCheck, 0, tr("Проверка"), 230)
	lvAddColumn(s.lvCheck, 1, tr("Итог"), 90)
	lvAddColumn(s.lvCheck, 2, tr("Подробности"), pw-32-230-90-4)

	// ---- General page ----
	p = pageGeneral
	langDesc := tr("Язык интерфейса; окно настроек откроется заново")
	row(p, contentY, 72, glyphLanguage, tr("Язык"), &langDesc)
	s.ddLang = dropdown(p, pRight-16-200, contentY+21, 200, idDDLang, langName(uiLang))
	startDesc := tr("Свёрнуто в трей, при входе в систему")
	row(p, contentY+84, 72, glyphStartup, tr("Запуск вместе с Windows"), &startDesc)
	s.tgStart = toggle(p, contentY+84+21, idToggleStart, runKeyExists())
	logDesc := logPath()
	row(p, contentY+168, 72, glyphLog, tr("Журнал"), &logDesc)
	btn(p, tr("Открыть"), pRight-16-120, contentY+168+21, 120, idBtnLog, false)
	s.cableDesc = tr("Проверка…")
	row(p, contentY+252, 72, glyphDownload, "VB-CABLE", &s.cableDesc)
	btn(p, tr("Установить"), pRight-16-120, contentY+252+21, 120, idBtnCable, false)
	about := tr("Микшер игры и Discord в одно виртуальное устройство для записи Steam / NVIDIA. Go, без зависимостей.")
	row(p, contentY+336, 72, glyphInfo, tr("О программе"), &about)
	exitDesc := tr("Остановить микшер и убрать иконку из трея")
	row(p, contentY+420, 72, glyphPower, tr("Выход"), &exitDesc)
	btn(p, tr("Выйти"), pRight-16-120, contentY+420+21, 120, idBtnExit, false)

	// Bottom bar, shared.
	s.bar = rect{px, winH - 16 - 46, pRight, winH - 16}
	btn(all, tr("Сохранить и применить"), int(s.bar.Right)-16-200, int(s.bar.Top)+8, 200, idSave, true)
	btn(all, tr("Закрыть"), int(s.bar.Right)-16-200-8-100, int(s.bar.Top)+8, 100, idClose, false)
}

// ---- painting ----------------------------------------------------------------------

func (s *settingsWin) paint(g *gui) {
	var ps paintStruct
	hdc, _, _ := procBeginPaint.Call(s.hwnd, uintptr(unsafe.Pointer(&ps)))
	if hdc == 0 {
		return
	}
	t, f := g.theme, g.fonts
	drawText(hdc, "maxmix", rect{24, 20, navW, 60}, dtLeft|dtVCenter|dtSingleLine, f.title, t.p.text)
	drawText(hdc, tr(pageInfo[s.page].title), rect{px, 16, pRight, 60}, dtLeft|dtVCenter|dtSingleLine, f.title, t.p.text)
	for _, c := range s.cards[s.page] {
		t.paintCard(hdc, c, f)
	}
	fillRound(hdc, s.bar, t.brCard, t.penCard, cardRadius)
	drawText(hdc, s.msg, rect{s.bar.Left + 16, s.bar.Top, s.bar.Right - 330, s.bar.Bottom}, dtLeft|dtVCenter|dtSingleLine|dtEndEllips, f.body, t.p.textDim)
	procEndPaint.Call(s.hwnd, uintptr(unsafe.Pointer(&ps)))
}

func (s *settingsWin) setMsg(text string) {
	s.msg = text
	procInvalidateRect.Call(s.hwnd, uintptr(unsafe.Pointer(&s.bar)), 1)
}

// setDesc updates a dynamic card description and repaints its card.
func (s *settingsWin) setDesc(p page, target *string, text string) {
	if *target == text {
		return
	}
	*target = text
	if s.page != p {
		return
	}
	for _, c := range s.cards[p] {
		if c.desc == target {
			procInvalidateRect.Call(s.hwnd, uintptr(unsafe.Pointer(&c.rc)), 1)
		}
	}
}

func (s *settingsWin) drawItem(g *gui, dis *drawItemStruct) {
	f := g.fonts
	if dis.CtlType == odtListView {
		g.theme.drawListRow(dis, f)
		return
	}
	h := dis.HwndItem
	for i := page(0); i < pageCount; i++ {
		if h == s.nav[i] {
			g.theme.drawNav(dis, pageInfo[i].glyph, tr(pageInfo[i].title), i == s.page, f)
			return
		}
	}
	if on, ok := s.toggles[h]; ok {
		g.theme.drawToggle(dis, on, f)
		return
	}
	if text, ok := s.ddText[h]; ok {
		if h == s.btnValue {
			text = ""
		}
		g.theme.drawDropdown(dis, text, f)
		return
	}
	if h == s.meter {
		g.theme.drawMeter(dis, s.outLevel)
		return
	}
	g.theme.drawButton(dis, s.primary[h], f.body)
}

// ---- data binding ---------------------------------------------------------------

func (s *settingsWin) sourceCells(src Source) (string, string, string) {
	val := src.Process
	if src.Kind() == "device" {
		val = src.Match
	} else if src.Kind() == "game" || src.Kind() == "auto" {
		val = fmt.Sprintf(tr("исключений: %d"), len(src.Exclude))
	}
	return kindLabel(src.Kind()), val, fmt.Sprintf("%d%%", int(math.Round(float64(src.Gain)*100)))
}

func (s *settingsWin) fillSources() {
	lvClear(s.lvSources)
	hasGame := false
	for i, src := range s.cfg.Sources {
		a, b, c := s.sourceCells(src)
		lvInsertRow(s.lvSources, i, a, b, c, "")
		if src.Kind() == "game" {
			hasGame = true
		}
	}
	s.toggles[s.tgGame] = hasGame
	procInvalidateRect.Call(s.tgGame, 0, 1)
}

func (s *settingsWin) updateRow(i int) {
	if i < 0 || i >= len(s.cfg.Sources) {
		return
	}
	a, b, c := s.sourceCells(s.cfg.Sources[i])
	lvSetCell(s.lvSources, i, 0, a)
	lvSetCell(s.lvSources, i, 1, b)
	lvSetCell(s.lvSources, i, 2, c)
}

func (s *settingsWin) setDropdown(h uintptr, text string) {
	s.ddText[h] = text
	procInvalidateRect.Call(h, 0, 1)
}

// loadEditor shows source i in the editor controls.
func (s *settingsWin) loadEditor(i int) {
	s.sel = i
	s.loading = true
	defer func() { s.loading = false }()
	valueCtls := []uintptr{s.edValue, s.btnValue, s.lblValue}
	exclCtls := []uintptr{s.edExclude, s.lblExclude}
	if i < 0 || i >= len(s.cfg.Sources) {
		for _, h := range append(valueCtls, exclCtls...) {
			showWindow(h, swHide)
		}
		s.setDropdown(s.ddKind, "")
		return
	}
	src := s.cfg.Sources[i]
	s.setDropdown(s.ddKind, kindLabel(src.Kind()))
	showValue := src.Kind() == "process" || src.Kind() == "device" || src.Kind() == "mic"
	for _, h := range valueCtls {
		showWindow(h, boolCmd(showValue && s.page == pageInputs))
	}
	switch src.Kind() {
	case "process":
		setText(s.edValue, src.Process)
	case "device":
		setText(s.edValue, src.Match)
	case "mic":
		setText(s.edValue, src.Mic)
	}
	excl := src.Kind() == "game" || src.Kind() == "auto"
	for _, h := range exclCtls {
		showWindow(h, boolCmd(excl && s.page == pageInputs))
	}
	if excl {
		setText(s.edExclude, strings.Join(src.Exclude, ", "))
	}
	pos := int(math.Round(float64(src.Gain) * 100))
	sendMessage(s.tbGain, tbmSetPos, 1, uintptr(pos))
	setText(s.stGain, fmt.Sprintf("%d%%", pos))
}

func boolCmd(show bool) int {
	if show {
		return swShow
	}
	return swHide
}

func peakBar(p float32) string {
	n := int(math.Round(math.Sqrt(float64(p)) * 8))
	if n > 8 {
		n = 8
	}
	return strings.Repeat("▰", n) + strings.Repeat("▱", 8-n)
}

// refresh updates the live parts: capture levels, detected game, output meter,
// session table, device list, cable state.
func (s *settingsWin) refresh(m *mixer) {
	caps, games, target, outPeak := m.status()
	bySrc := map[int][]string{}
	captured := map[uint32]bool{}
	for _, c := range caps {
		label := c.Label
		if c.PID != 0 {
			label = fmt.Sprintf("%s (%d)", c.Label, c.PID)
			captured[c.PID] = true
		}
		bySrc[c.Source] = append(bySrc[c.Source], peakBar(c.Peak)+" "+label)
	}
	for i := range s.cfg.Sources {
		lvSetCell(s.lvSources, i, 3, strings.Join(bySrc[i], "   "))
	}
	if len(games) == 0 {
		s.setDesc(pageInputs, &s.gameDesc, tr("Окно на весь экран, на переднем плане или запущено из лаунчера. Сейчас игра не найдена"))
	} else {
		var parts []string
		for _, g := range games {
			parts = append(parts, fmt.Sprintf("%s (%d)", g.Exe, g.PID))
		}
		s.setDesc(pageInputs, &s.gameDesc, tr("Сейчас: ")+strings.Join(parts, ", "))
	}
	if lv := math.Sqrt(float64(outPeak)); math.Abs(lv-s.outLevel) > 0.01 {
		s.outLevel = lv
		procInvalidateRect.Call(s.meter, 0, 1)
	}
	if s.ddText[s.ddTarget] == "" {
		switch {
		case target != "":
			s.setDropdown(s.ddTarget, target)
		case len(s.cfg.Target) > 0:
			s.setDropdown(s.ddTarget, s.cfg.Target[0])
		}
	}
	if target == "" && s.msg == "" {
		s.setMsg(tr("Выход не открыт: устройство не найдено"))
	}

	s.refreshCheck()

	sessions, err := listRenderSessions()
	table, err2 := processTable()
	if err == nil && err2 == nil {
		var rows []sessionRow
		exes := map[string]bool{}
		for _, ss := range sessions {
			exe := "?"
			if p, ok := table[ss.PID]; ok {
				exe = p.Exe
			}
			exes[exe] = true
			rows = append(rows, sessionRow{PID: ss.PID, Exe: exe, Device: ss.Device, State: sessionStateName(ss.State),
				Captured: captured[ss.PID] || hasAncestorIn(table, ss.PID, captured)})
		}
		sort.Slice(rows, func(i, j int) bool {
			if !strings.EqualFold(rows[i].Exe, rows[j].Exe) {
				return strings.ToLower(rows[i].Exe) < strings.ToLower(rows[j].Exe)
			}
			return rows[i].PID < rows[j].PID
		})
		if sig := fmt.Sprint(rows); sig != s.rowsSig {
			s.rowsSig = sig
			s.rows = rows
			sel := lvSelected(s.lvSessions)
			lvClear(s.lvSessions)
			for i, r := range rows {
				state := map[string]string{"active": tr("играет"), "inactive": tr("тихо"), "expired": tr("закрыта")}[r.State]
				cap := tr("нет")
				if r.Captured {
					cap = tr("да")
				}
				lvInsertRow(s.lvSessions, i, r.Exe, fmt.Sprint(r.PID), r.Device, state, cap)
			}
			if sel >= 0 && sel < len(rows) {
				lvSelect(s.lvSessions, sel)
			}
			s.exes = s.exes[:0]
			for e := range exes {
				s.exes = append(s.exes, e)
			}
			sort.Strings(s.exes)
		}
	}
	if eps, err := listEndpoints(eRender); err == nil {
		var names []string
		cable := false
		for _, e := range eps {
			names = append(names, e.Name)
			if strings.Contains(e.Name, cableEndpoint) {
				cable = true
			}
		}
		sort.Strings(names)
		s.devices = names
		if cable {
			s.setDesc(pageGeneral, &s.cableDesc, tr("Установлен: устройство CABLE Input найдено"))
		} else {
			s.setDesc(pageGeneral, &s.cableDesc, tr("Не установлен. Нужен как виртуальное устройство; после установки перезагрузите систему"))
		}
	}
	if eps, err := listEndpoints(eCapture); err == nil {
		var names []string
		for _, e := range eps {
			names = append(names, e.Name)
		}
		sort.Strings(names)
		s.micDevs = names
	}
}

// startSelfTest runs the self-check in the background; the refresh timer picks
// up the report and fills the list.
func (s *settingsWin) startSelfTest(g *gui) {
	s.checkMu.Lock()
	if s.checkRunning {
		s.checkMu.Unlock()
		return
	}
	s.checkRunning = true
	s.checkRows = []check{{tr("Проверка"), checkInfo, tr("измеряем…")}}
	s.checkMu.Unlock()
	s.checkSig = ""
	cfg := g.m.config()
	dir := filepath.Join(os.Getenv("LOCALAPPDATA"), "maxmix")
	os.MkdirAll(dir, 0o755)
	go func() {
		rows := runSelfTest(cfg, selfTestDefault, dir)
		s.checkMu.Lock()
		s.checkRows, s.checkRunning = rows, false
		s.checkMu.Unlock()
	}()
}

// refreshCheck copies a finished (or running) self-check report into the list.
func (s *settingsWin) refreshCheck() {
	s.checkMu.Lock()
	rows := append([]check(nil), s.checkRows...)
	s.checkMu.Unlock()
	if sig := fmt.Sprint(rows); sig != s.checkSig {
		s.checkSig = sig
		lvClear(s.lvCheck)
		for i, r := range rows {
			lvInsertRow(s.lvCheck, i, r.Name, r.Status.label(), r.Detail)
		}
	}
	s.setDesc(pageCheck, &s.checkDesc, s.checkHint())
}

func (s *settingsWin) refreshRoute() {
	rows, err := routeRows(s.cfg.Apps)
	if err != nil {
		s.setDesc(pageOutput, &s.routeDesc, err.Error())
		return
	}
	var parts []string
	for _, r := range rows {
		switch {
		case !r.Running:
			parts = append(parts, r.App+tr(": не запущена"))
		case r.Error != "":
			parts = append(parts, r.App+tr(": пока нельзя, процесс ещё не пользовался звуком"))
		default:
			parts = append(parts, r.App+": "+r.Device)
		}
	}
	s.setDesc(pageOutput, &s.routeDesc, strings.Join(parts, "\n"))
}

// ---- message handling -------------------------------------------------------------

func (s *settingsWin) proc(g *gui, m uint32, w, l uintptr) uintptr {
	switch m {
	case wmTimer:
		s.refresh(g.m)
		return 0
	case wmEraseBkgnd:
		g.theme.eraseBackground(s.hwnd, w)
		return 1
	case wmPaint:
		s.paint(g)
		return 0
	case wmMeasureItem:
		mis := (*measureItemStruct)(unsafe.Pointer(l))
		if mis.CtlType == odtListView {
			mis.ItemHeight = rowHeight
		}
		return 1
	case wmDrawItem:
		s.drawItem(g, (*drawItemStruct)(unsafe.Pointer(l)))
		return 1
	case wmCtlColorStatic, wmCtlColorBtn, wmCtlColorEdit, wmCtlColorListBox:
		return g.theme.ctlColor(m, w, s.dim[l])
	case wmSettingChange:
		if isImmersiveColorSet(l) {
			g.themeChanged()
		}
		return 0
	case wmNotify:
		hdr := (*nmhdr)(unsafe.Pointer(l))
		if hdr.Code == nmCustomDraw && hdr.HwndFrom == s.tbGain {
			return g.theme.trackbarCustomDraw(l)
		}
		if hdr.HwndFrom == s.lvSources && hdr.Code == lvnItemChanged {
			nm := (*nmListView)(unsafe.Pointer(l))
			if nm.NewState&lvisSelected != 0 && nm.OldState&lvisSelected == 0 {
				s.loadEditor(int(nm.Item))
			}
		}
		return 0
	case wmHScroll:
		switch l {
		case s.tbGain:
			if s.sel >= 0 && s.sel < len(s.cfg.Sources) {
				pos := int(sendMessage(s.tbGain, tbmGetPos, 0, 0))
				s.cfg.Sources[s.sel].Gain = float32(pos) / 100
				setText(s.stGain, fmt.Sprintf("%d%%", pos))
				s.updateRow(s.sel)
			}
		case s.tbMonitor:
			pos := int(sendMessage(s.tbMonitor, tbmGetPos, 0, 0))
			s.cfg.MonitorGain = float32(pos) / 100
			setText(s.stMonitor, fmt.Sprintf("%d%%", pos))
		}
		return 0
	case wmCommand:
		s.command(g, loword(w), hiword(w))
		return 0
	case wmClose:
		procDestroyWindow.Call(s.hwnd)
		return 0
	case wmDestroy:
		procKillTimer.Call(s.hwnd, idTimer)
		g.set = nil
		return 0
	}
	r, _, _ := procDefWindowProcW.Call(s.hwnd, uintptr(m), w, l)
	return r
}

func (s *settingsWin) command(g *gui, id, code uint32) {
	cur := func() *Source {
		if s.sel < 0 || s.sel >= len(s.cfg.Sources) {
			return nil
		}
		return &s.cfg.Sources[s.sel]
	}
	if id >= idNavFirst && id < idNavFirst+uint32(pageCount) {
		s.showPage(page(id - idNavFirst))
		return
	}
	switch id {
	case idToggleGame:
		if !s.toggles[s.tgGame] {
			s.cfg.Sources = append([]Source{{Process: "game", Exclude: defaultAutoExclude, Gain: 1}}, s.cfg.Sources...)
		} else {
			var keep []Source
			for _, src := range s.cfg.Sources {
				if src.Kind() != "game" {
					keep = append(keep, src)
				}
			}
			s.cfg.Sources = keep
		}
		s.fillSources()
		s.loadEditor(-1)
		s.setMsg(tr("Нажмите «Сохранить и применить»"))
	case idToggleStart:
		on := !s.toggles[s.tgStart]
		if err := setAutostart(on); err != nil {
			s.setMsg(tr("Автозапуск: ") + err.Error())
			return
		}
		s.toggles[s.tgStart] = on
		procInvalidateRect.Call(s.tgStart, 0, 1)
	case idDDKind:
		if src := cur(); src != nil {
			var labels []string
			for _, kn := range kindNames {
				labels = append(labels, tr(kn.label))
			}
			if i := popupMenu(s.hwnd, s.ddKind, labels); i >= 0 {
				switch k := kindNames[i].key; k {
				case "device":
					src.Process, src.Mic, src.Exclude = "", "", nil
				case "mic":
					src.Process, src.Match, src.Exclude = "", "", nil
					if src.Mic == "" && len(s.micDevs) > 0 {
						src.Mic = s.micDevs[0]
					}
				case "process":
					src.Match, src.Mic, src.Exclude = "", "", nil
					if src.Process == "game" || src.Process == "auto" {
						src.Process = ""
					}
				default:
					src.Match, src.Mic, src.Process = "", "", k
					if src.Exclude == nil {
						src.Exclude = defaultAutoExclude
					}
				}
				s.updateRow(s.sel)
				s.loadEditor(s.sel)
				s.fillSources()
				lvSelect(s.lvSources, s.sel)
			}
		}
	case idValueMenu:
		if src := cur(); src != nil {
			list := s.exes
			switch src.Kind() {
			case "device":
				list = s.devices
			case "mic":
				list = s.micDevs
			}
			if i := popupMenu(s.hwnd, s.edValue, list); i >= 0 {
				setText(s.edValue, list[i]) // EN_CHANGE stores it
			}
		}
	case idEdValue:
		if code == enChange && !s.loading {
			if src := cur(); src != nil {
				switch src.Kind() {
				case "device":
					src.Match = getText(s.edValue)
				case "mic":
					src.Mic = getText(s.edValue)
				default:
					src.Process = getText(s.edValue)
				}
				s.updateRow(s.sel)
			}
		}
	case idDDLang:
		var names []string
		for _, l := range languages {
			names = append(names, l.name)
		}
		if i := popupMenu(s.hwnd, s.ddLang, names); i >= 0 && languages[i].code != uiLang {
			cfg := g.m.config()
			cfg.Lang = languages[i].code
			if err := g.m.Apply(cfg); err != nil {
				s.setMsg(tr("Ошибка: ") + err.Error())
				return
			}
			uiLang = cfg.Lang
			g.retitle()
			procDestroyWindow.Call(s.hwnd) // WM_DESTROY clears g.set
			g.openSettings()
			if g.set != nil {
				g.set.showPage(pageGeneral)
			}
			return
		}
	case idDDTarget:
		if i := popupMenu(s.hwnd, s.ddTarget, s.devices); i >= 0 {
			s.setDropdown(s.ddTarget, s.devices[i])
		}
	case idDDMonitor:
		items := append([]string{tr("Выключено")}, s.devices...)
		if i := popupMenu(s.hwnd, s.ddMonitor, items); i >= 0 {
			if i == 0 {
				s.cfg.Monitor = ""
			} else {
				s.cfg.Monitor = s.devices[i-1]
			}
			s.setDropdown(s.ddMonitor, items[i])
			s.setMsg(tr("Нажмите «Сохранить и применить»"))
		}
	case idExclude:
		if code == enChange && !s.loading {
			if src := cur(); src != nil {
				src.Exclude = splitList(getText(s.edExclude))
				s.updateRow(s.sel)
			}
		}
	case idAdd:
		s.cfg.Sources = append(s.cfg.Sources, Source{Process: "", Gain: 1})
		s.fillSources()
		lvSelect(s.lvSources, len(s.cfg.Sources)-1)
	case idDel:
		if s.sel >= 0 && s.sel < len(s.cfg.Sources) {
			s.cfg.Sources = append(s.cfg.Sources[:s.sel], s.cfg.Sources[s.sel+1:]...)
			s.fillSources()
			s.loadEditor(-1)
		}
	case idAddProc:
		if i := lvSelected(s.lvSessions); i >= 0 && i < len(s.rows) {
			exe := s.rows[i].Exe
			for _, src := range s.cfg.Sources {
				if strings.EqualFold(src.Process, exe) {
					s.setMsg(exe + tr(" уже есть во входах"))
					return
				}
			}
			s.cfg.Sources = append(s.cfg.Sources, Source{Process: exe, Gain: 1})
			s.fillSources()
			s.setMsg(tr("Добавлен вход ") + exe + tr(". Нажмите «Сохранить и применить»."))
		}
	case idExcl:
		if i := lvSelected(s.lvSessions); i >= 0 && i < len(s.rows) {
			exe, n := s.rows[i].Exe, 0
			for j := range s.cfg.Sources {
				src := &s.cfg.Sources[j]
				if (src.Kind() == "game" || src.Kind() == "auto") && !containsFold(src.Exclude, exe) {
					src.Exclude = append(src.Exclude, exe)
					n++
				}
			}
			if n == 0 {
				s.setMsg(tr("Нет входов типа «Игра (авто)» или «Всё, что звучит»"))
			} else {
				s.fillSources()
				s.setMsg(exe + tr(" исключён. Нажмите «Сохранить и применить»."))
			}
		}
	case idRoute, idUnroute:
		s.cfg.Apps = splitList(getText(s.edApps))
		deviceID := ""
		if id == idRoute {
			ep, err := targetID(s.cfg)
			if err != nil {
				s.setMsg(err.Error())
				return
			}
			deviceID = ep.ID
		}
		if _, err := routeApps(s.cfg.Apps, deviceID); err != nil {
			s.setMsg(err.Error())
		}
		s.refreshRoute()
	case idBtnCheck:
		s.startSelfTest(g)
	case idBtnLog:
		shellOpen(logPath())
	case idBtnCable:
		s.setMsg(tr("Установка VB-CABLE: подтвердите запрос UAC"))
		go func() {
			if err := installCable(); err != nil {
				log.Printf("install-cable: %v", err)
			}
		}()
	case idBtnExit:
		g.exit()
	case idSave:
		s.save(g)
	case idClose:
		procDestroyWindow.Call(s.hwnd)
	}
}

func (s *settingsWin) save(g *gui) {
	cfg := s.cfg
	cfg.Apps = splitList(getText(s.edApps))
	if t := strings.TrimSpace(s.ddText[s.ddTarget]); t != "" {
		targets := []string{t}
		for _, old := range s.cfg.Target {
			if old != t {
				targets = append(targets, old)
			}
		}
		cfg.Target = targets
	}
	for i := range cfg.Sources {
		src := &cfg.Sources[i]
		if src.Kind() == "process" && src.Process == "" {
			s.setMsg(fmt.Sprintf(tr("Вход %d: не задано имя программы"), i+1))
			return
		}
		if src.Kind() == "device" && src.Match == "" {
			s.setMsg(fmt.Sprintf(tr("Вход %d: не задано устройство"), i+1))
			return
		}
		if src.Kind() == "mic" && src.Mic == "" {
			s.setMsg(fmt.Sprintf(tr("Вход %d: не задан микрофон"), i+1))
			return
		}
	}
	if err := g.m.Apply(cfg); err != nil {
		s.setMsg(tr("Ошибка: ") + err.Error())
		return
	}
	s.cfg = g.m.config()
	s.fillSources()
	if s.sel >= 0 && s.sel < len(s.cfg.Sources) {
		lvSelect(s.lvSources, s.sel)
	}
	s.setMsg(tr("Сохранено и применено"))
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func containsFold(list []string, s string) bool {
	for _, x := range list {
		if strings.EqualFold(x, s) {
			return true
		}
	}
	return false
}

// loadMonitor shows the check-time microphone monitoring settings.
func (s *settingsWin) loadMonitor() {
	name := tr("Выключено")
	if s.cfg.Monitor != "" {
		name = s.cfg.Monitor
	}
	s.setDropdown(s.ddMonitor, name)
	pos := int(math.Round(float64(s.cfg.MonitorGain) * 100))
	if pos <= 0 {
		pos = 100
	}
	sendMessage(s.tbMonitor, tbmSetPos, 1, uintptr(pos))
	setText(s.stMonitor, fmt.Sprintf("%d%%", pos))
}
