//go:build windows

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lxn/walk"
	. "github.com/lxn/walk/declarative"
)

// ---------- справочники подписей ----------

var guiCats = []struct{ key, title string }{
	{"all", "Все категории"},
	{"fav", "★ Избранное"},
	{"report", "Налоги и отчётность"},
	{"citizen", "Гражданам"},
	{"ved", "ВЭД и таможня"},
	{"torg", "Закупки и торги"},
	{"service", "Госуслуги и идентификация"},
	{"ref", "Справочные"},
}

var guiAuthTitles = map[string]string{
	"ecp": "ЭЦП", "mecp": "МЭЦП", "msi": "МСИ",
	"lp": "логин/пароль", "esa": "ЕСА", "ref": "справочно",
}

func catTitleByKey(key string) string {
	for _, c := range guiCats {
		if c.key == key {
			return c.title
		}
	}
	return key
}

func authLine(p *Portal) string {
	var parts []string
	for _, a := range p.Auth {
		if t, ok := guiAuthTitles[a]; ok {
			parts = append(parts, t)
		}
	}
	s := strings.Join(parts, ", ")
	if p.IE {
		if s != "" {
			s += " · "
		}
		s += "IE-режим"
	}
	return s
}

func statusText(st Status, probed bool) string {
	if !probed {
		return "…проверка"
	}
	if st.OK {
		return fmt.Sprintf("✓ доступен (%d мс)", st.MS)
	}
	return "✕ недоступен"
}

// ---------- модель таблицы ----------

type guiModel struct {
	walk.TableModelBase
	mu     sync.Mutex
	rows   []int
	status map[string]Status
	favs   map[string]bool
	query  string
	cat    string
}

func newGuiModel() *guiModel {
	return &guiModel{status: map[string]Status{}, favs: loadFavs(), cat: "all"}
}

func (m *guiModel) RowCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.rows)
}

func (m *guiModel) Value(row, col int) interface{} {
	m.mu.Lock()
	defer m.mu.Unlock()
	if row < 0 || row >= len(m.rows) {
		return ""
	}
	p := portals[m.rows[row]]
	switch col {
	case 0:
		if m.favs[p.ID] {
			return "★"
		}
		return ""
	case 1:
		return p.Name
	case 2:
		return catTitleByKey(p.Cat)
	case 3:
		return authLine(&p)
	case 4:
		st, probed := m.status[p.ID]
		return statusText(st, probed)
	case 5:
		return strings.TrimPrefix(strings.TrimPrefix(p.Site, "https://"), "http://")
	}
	return ""
}

// applyFilter пересчитывает видимые строки. Вызывать из UI-потока.
func (m *guiModel) applyFilter() {
	m.mu.Lock()
	defer m.mu.Unlock()
	q := strings.ToLower(strings.TrimSpace(m.query))
	m.rows = m.rows[:0]
	for i := range portals {
		p := &portals[i]
		if m.cat == "fav" && !m.favs[p.ID] {
			continue
		}
		if m.cat != "all" && m.cat != "fav" && p.Cat != m.cat {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(p.Name+" "+p.Desc+" "+p.Note), q) {
			continue
		}
		m.rows = append(m.rows, i)
	}
	m.PublishRowsReset()
}

func (m *guiModel) setStatus(id string, st Status) {
	m.mu.Lock()
	m.status[id] = st
	m.mu.Unlock()
}

func (m *guiModel) setStatusAllReady(ready map[string]Status) {
	m.mu.Lock()
	m.status = ready
	m.mu.Unlock()
}

func (m *guiModel) probed(id string) (Status, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.status[id]
	return s, ok
}

func (m *guiModel) toggleFav(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.favs[id] = !m.favs[id]
	if !m.favs[id] {
		delete(m.favs, id)
	}
	saveFavs(m.favs)
	return m.favs[id]
}

func (m *guiModel) counts(probeDone map[string]bool) (ok, total, probedCnt int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	total = len(m.rows)
	for _, ri := range m.rows {
		p := portals[ri]
		if st, is := m.status[p.ID]; is {
			probedCnt++
			if st.OK {
				ok++
			}
		}
	}
	return
}

// ---------- избранное (файл в %APPDATA%) ----------

func favsPath() string {
	base := os.Getenv("APPDATA")
	if base == "" {
		base, _ = os.UserHomeDir()
	}
	dir := filepath.Join(base, "GosPortalyRB")
	os.MkdirAll(dir, 0o755)
	return filepath.Join(dir, "favorites.json")
}

func loadFavs() map[string]bool {
	out := map[string]bool{}
	if raw, err := os.ReadFile(favsPath()); err == nil {
		_ = json.Unmarshal(raw, &out)
	}
	return out
}

func saveFavs(f map[string]bool) {
	raw, err := json.MarshalIndent(f, "", "  ")
	if err == nil {
		_ = os.WriteFile(favsPath(), raw, 0o644)
	}
}

// ---------- GUI ----------

var refreshing int32

func runGUI() error {
	model := newGuiModel()

	var (
		mw       *walk.MainWindow
		table    *walk.TableView
		noteTE   *walk.TextEdit
		searchLE *walk.LineEdit
		catCB    *walk.ComboBox
		btnFav   *walk.PushButton
		sbMsg    *walk.StatusBarItem
		sbCount  *walk.StatusBarItem
		sbHint   *walk.StatusBarItem
	)

	var cbItems []string
	for _, c := range guiCats {
		cbItems = append(cbItems, c.title)
	}

	currentPortal := func() (*Portal, bool) {
		i := table.CurrentIndex()
		if i < 0 || i >= model.RowCount() {
			return nil, false
		}
		model.mu.Lock()
		defer model.mu.Unlock()
		return &portals[model.rows[i]], true
	}

	syncSelection := func() {
		p, ok := currentPortal()
		if !ok {
			noteTE.SetText("")
			return
		}
		var b strings.Builder
		if p.Note != "" {
			fmt.Fprintf(&b, "Что нужно для входа: %s", p.Note)
		}
		if p.IE {
			if b.Len() > 0 {
				b.WriteString("\r\n")
			}
			b.WriteString("⚠ Требуется Internet Explorer 11 или Edge в режиме IE.")
		}
		noteTE.SetText(b.String())
		if model.favs[p.ID] {
			btnFav.SetText("★ Убрать из избранного")
		} else {
			btnFav.SetText("☆ В избранное")
		}
	}

	openSelected := func(login bool) {
		p, ok := currentPortal()
		if !ok {
			walk.MsgBox(mw, appName, "Выберите портал в таблице.", walk.MsgBoxIconInformation)
			return
		}
		u := p.Site
		if login && p.Login != "" {
			u = p.Login
		}
		if err := openBrowser(u); err != nil {
			walk.MsgBox(mw, appName, "Не удалось открыть браузер: "+err.Error(), walk.MsgBoxIconError)
		}
	}

	updateCounts := func(probing bool) {
		ok, total, probed := model.counts(nil)
		if probing {
			sbMsg.SetText(fmt.Sprintf("Обновление статусов… %d/%d", probed, total))
		} else {
			sbMsg.SetText("Обновление завершено")
		}
		sbCount.SetText(fmt.Sprintf("Порталов: %d · доступно: %d", total, ok))
		mw.SetTitle(fmt.Sprintf("%s — доступно %d из %d", appName, ok, total))
	}

	refreshStatuses := func() {
		if !atomic.CompareAndSwapInt32(&refreshing, 0, 1) {
			return
		}
		go func() {
			defer atomic.StoreInt32(&refreshing, 0)
			var wg sync.WaitGroup
			ready := map[string]Status{}
			var rm sync.Mutex
			for _, p := range portals {
				wg.Add(1)
				go func(p Portal) {
					defer wg.Done()
					st := probe(p.Site)
					rm.Lock()
					ready[p.ID] = st
					rm.Unlock()
					mw.Synchronize(func() {
						model.setStatus(p.ID, st)
						model.PublishRowsReset()
						updateCounts(true)
					})
				}(p)
			}
			wg.Wait()
			mw.Synchronize(func() { updateCounts(false) })
		}()
	}

	trayReady := false
	var realQuit bool

	create := MainWindow{
		AssignTo: &mw,
		Title:    appName,
		Size:     Size{Width: 1040, Height: 680},
		MinSize:  Size{Width: 860, Height: 560},
		Layout:   VBox{},
		MenuItems: []MenuItem{
			Menu{Text: "&Файл", Items: []MenuItem{
				Action{Text: "Войти в выбранный портал", OnTriggered: func() { openSelected(true) }},
				Action{Text: "Открыть сайт портала", OnTriggered: func() { openSelected(false) }},
				Separator{},
				Action{Text: "Свернуть в трей", OnTriggered: func() { mw.Hide() }},
				Action{Text: "Выход", OnTriggered: func() { realQuit = true; mw.Close() }},
			}},
			Menu{Text: "&Сервис", Items: []MenuItem{
				Action{Text: "Обновить статусы порталов", OnTriggered: refreshStatuses},
				Action{Text: "Проверить систему…", OnTriggered: func() { showChecksDialog(mw) }},
				Separator{},
				Action{Text: "Настройки для входа…", OnTriggered: func() { showSetupDialog(mw) }},
			}},
			Menu{Text: "&Помощь", Items: []MenuItem{
				Menu{Text: "Утилиты и сертификаты", Items: []MenuItem{
					Action{Text: "AVEST CSP (avest.by)", OnTriggered: func() { openTool("avest") }},
					Action{Text: "AvTuneProxy (avtunproxy.by)", OnTriggered: func() { openTool("avtunproxy") }},
					Action{Text: "Сертификаты НУЦ (nca.by)", OnTriggered: func() { openTool("nca") }},
					Action{Text: "ГосСУОК (gosuok.gov.by)", OnTriggered: func() { openTool("gosuok") }},
				}},
				Menu{Text: "Идентификация", Items: []MenuItem{
					Action{Text: "Регистрация МСИ (raschet.by)", OnTriggered: func() { openTool("msi-reg") }},
					Action{Text: "ЕСА (auth.gia.gov.by)", OnTriggered: func() { openTool("esa") }},
				}},
				Separator{},
				Action{Text: "О программе…", OnTriggered: func() {
					walk.MsgBox(mw, "О программе",
						fmt.Sprintf("%s v%s\r\n\r\nПанель госпорталов Республики Беларусь с настройками для входа.\r\nАутентификация — только на официальных сайтах (*.gov.by): приложение не запрашивает и не хранит логины и пароли.\r\n\r\nРежим веб-панели для сети: запуск с флагом -web", appName, appVer),
						walk.MsgBoxIconInformation)
				}},
			}},
		},
		Children: []Widget{
			Composite{Layout: HBox{}, Children: []Widget{
				LineEdit{AssignTo: &searchLE, CueBanner: "Поиск: МНС, ФСЗН, ЭСЧФ…", OnTextChanged: func() {
					model.mu.Lock()
					model.query = searchLE.Text()
					model.mu.Unlock()
					model.applyFilter()
					syncSelection()
				}},
				ComboBox{AssignTo: &catCB, Model: cbItems, CurrentIndex: 0, OnCurrentIndexChanged: func() {
					i := catCB.CurrentIndex()
					if i >= 0 && i < len(guiCats) {
						model.mu.Lock()
						model.cat = guiCats[i].key
						model.mu.Unlock()
						model.applyFilter()
						syncSelection()
					}
				}},
				PushButton{Text: "⟳ Очистить", OnClicked: func() {
					searchLE.SetText("")
					catCB.SetCurrentIndex(0)
				}},
			}},
			TableView{
				AssignTo: &table,
				Columns: []TableViewColumn{
					{Title: "★", Width: 32},
					{Title: "Портал", Width: 250},
					{Title: "Категория", Width: 150},
					{Title: "Способы входа", Width: 175},
					{Title: "Статус", Width: 140},
					{Title: "Адрес", Width: 240},
				},
				OnItemActivated: func() { openSelected(true) },
			},
			TextEdit{
				AssignTo: &noteTE,
				ReadOnly: true,
				MinSize:  Size{Height: 64},
				MaxSize:  Size{Height: 96},
				Text:     "Выберите портал в таблице — здесь появятся «настройки для входа».",
			},
			Composite{Layout: HBox{}, Children: []Widget{
				PushButton{Text: "Войти в портал →", OnClicked: func() { openSelected(true) }},
				PushButton{Text: "Сайт", OnClicked: func() { openSelected(false) }},
				PushButton{AssignTo: &btnFav, Text: "☆ В избранное", OnClicked: func() {
					p, ok := currentPortal()
					if !ok {
						return
					}
					model.toggleFav(p.ID)
					model.applyFilter()
					syncSelection()
				}},
				HSpacer{},
				PushButton{Text: "⚙ Настройки входа…", OnClicked: func() { showSetupDialog(mw) }},
				PushButton{Text: "🩺 Проверить систему", OnClicked: func() { showChecksDialog(mw) }},
				PushButton{Text: "⟳ Статусы", OnClicked: refreshStatuses},
			}},
		},
		StatusBarItems: []StatusBarItem{
			{AssignTo: &sbMsg, Text: "Готово", Width: 320},
			{AssignTo: &sbCount, Text: "Порталов: 0", Width: 200},
			{AssignTo: &sbHint, Text: "Двойной клик / Enter — вход на портал", Width: 300},
		},
	}
	if err := create.Create(); err != nil {
		return fmt.Errorf("создание окна: %w", err)
	}

	// Иконка из ресурсов exe (rsrc: RT_GROUP_ICON = 2).
	var appIcon *walk.Icon
	if ic, err := walk.NewIconFromResourceId(2); err == nil {
		appIcon = ic
		mw.SetIcon(ic)
	}

	// Трей.
	if appIcon != nil {
		if ni, err := walk.NewNotifyIcon(mw); err == nil {
			trayReady = true
			ni.SetIcon(appIcon)
			ni.SetToolTip(appName + " — панель госпорталов")
			showAct := walk.NewAction()
			showAct.SetText("Показать / скрыть")
			showAct.Triggered().Attach(func() {
				if mw.Visible() {
					mw.Hide()
				} else {
					mw.Show()
					mw.BringToTop()
				}
			})
			ni.ContextMenu().Actions().Add(showAct)
			exitAct := walk.NewAction()
			exitAct.SetText("Выход")
			exitAct.Triggered().Attach(func() { realQuit = true; mw.Close() })
			ni.ContextMenu().Actions().Add(exitAct)
			ni.MouseDown().Attach(func(x, y int, button walk.MouseButton) {
				if button == walk.LeftButton {
					if mw.Visible() {
						mw.Hide()
					} else {
						mw.Show()
						mw.BringToTop()
					}
				}
			})
			ni.SetVisible(true)
			defer ni.Dispose()
		}
	}

	// Крестик — свернуть в трей (если он есть), иначе обычный выход.
	mw.Closing().Attach(func(canceled *bool, reason walk.CloseReason) {
		if trayReady && !realQuit {
			*canceled = true
			mw.Hide()
		}
	})

	// Первичное заполнение и проверка статусов.
	model.applyFilter()
	updateCounts(true)
	refreshStatuses()
	go func() {
		t := time.NewTicker(120 * time.Second)
		defer t.Stop()
		for range t.C {
			refreshStatuses()
		}
	}()

	mw.Run()
	return nil
}

func openTool(key string) {
	if u, ok := toolsWhitelist[key]; ok {
		_ = openBrowser(u)
	}
}

// showChecksDialog — диагностика «настроек для входа» (асинхронно).
func showChecksDialog(owner walk.Form) {
	var dlg *walk.Dialog
	var report *walk.TextEdit
	d := Dialog{
		AssignTo: &dlg,
		Title:    "Проверка системы — настройки для входа",
		MinSize:  Size{Width: 640, Height: 460},
		Size:     Size{Width: 680, Height: 500},
		Layout:   VBox{},
		Children: []Widget{
			TextEdit{AssignTo: &report, ReadOnly: true, VScroll: true,
				Text: "Проверка системы… обычно занимает 5–15 секунд.\r\n(запускается certutil, читается реестр Windows)"},
			Composite{Layout: HBox{}, Children: []Widget{
				PushButton{Text: "AVEST CSP ↗", OnClicked: func() { openTool("avest") }},
				PushButton{Text: "AvTuneProxy ↗", OnClicked: func() { openTool("avtunproxy") }},
				PushButton{Text: "НУЦ ↗", OnClicked: func() { openTool("nca") }},
				PushButton{Text: "ГосСУОК ↗", OnClicked: func() { openTool("gosuok") }},
				HSpacer{},
				PushButton{Text: "Закрыть", OnClicked: func() { dlg.Cancel() }},
			}},
		},
	}
	if err := d.Create(owner); err != nil {
		walk.MsgBox(owner, appName, err.Error(), walk.MsgBoxIconError)
		return
	}
	go func() {
		checks := runEnvironmentChecks()
		var b strings.Builder
		icons := map[string]string{"ok": "✓", "warn": "!", "fail": "✕", "info": "ℹ"}
		for _, c := range checks {
			fmt.Fprintf(&b, "%s  %s\r\n    %s\r\n", icons[c.Status], strings.ToUpper(c.Title), c.Detail)
			if c.FixURL != "" && c.Status != "ok" {
				fmt.Fprintf(&b, "    → Решение: %s (%s)\r\n", c.FixLabel, c.FixURL)
			}
			b.WriteString("\r\n")
		}
		fmt.Fprintf(&b, "Проверено: %s\r\n", time.Now().Format("15:04:05"))
		dlg.Synchronize(func() { report.SetText(b.String()) })
	}()
	dlg.Run()
}

// showSetupDialog — справочник настроек для входа.
func showSetupDialog(owner walk.Form) {
	const text = `ЭЦП (юрлица и ИП) — общая схема подключения:

  1. Установите криптопровайдер AVEST CSP (avest.by, есть бесплатный тест-период).
  2. Получите сертификат ключа ЭЦП в удостоверяющем центре
     (РУЦ ОСТ, НУЦ, ГосСУОК — для бюджетных организаций).
  3. Установите корневые сертификаты НУЦ/ГосСУОК в «Доверенные корневые» Windows
     (скачать: nca.by, gosuok.gov.by) — иначе порталы пишут «сертификат не доверенный».
  4. МНС (физическая ЭЦП) и «Электронный респондент» Белстата требуют режим IE:
     Edge → Параметры → «Браузер по умолчанию» → «Страницы в режиме
     Internet Explorer» → добавьте адрес портала.

Вход БЕЗ ЭЦП:

  • МСИ — межбанковская система идентификации: регистрация на raschet.by
    (ioauth.raschet.by/client), в банке или у мобильного оператора.
    Открывает: ЛК МНС для физлиц, ФСЗН «без ЭЦП» (там нужен ещё AvTuneProxy),
    банковские сервисы.
  • ЕСА — единая учётная запись для portal.gov.by и «Обращений»:
    регистрация на портале услуг, в сервисном центре ОАИП или через МСИ.
  • Личный кабинет физлица МНС (lkfl.portal.nalog.gov.by): учётную запись
    бесплатно выдают в любой налоговой по паспорту.

Особенности ведомств:

  • МНС (portal.nalog.gov.by): ЮЛ — только физическая ЭЦП (IE 11 / Edge IE-режим).
    ИП — ЭЦП или МЭЦП. Логин/пароль — только для физлиц.
  • ФСЗН (portal.ssf.gov.by / ФСЗН 2.0: portal2.ssf.gov.by/mainPage):
    «Войти» → «Плательщики взносов»; без ЭЦП — по МСИ с AvTuneProxy.
  • Белстат (e-respondent.belstat.gov.by): логин/пароль выдаются
    при подключении ключа ЭЦП; нужна страница в режиме IE.
  • Белгосстрах (reporting.bgs.by:147): отчёты страхователей по ЭЦП;
    my.bgs.by — личный кабинет физлиц по e-mail и паролю.

Безопасность: вход выполняется только на официальных доменах (*.gov.by
и официальные сервисы). Приложение не запрашивает и не хранит учётные данные.`

	var dlg *walk.Dialog
	var report *walk.TextEdit
	d := Dialog{
		AssignTo: &dlg,
		Title:    "Настройки для входа на госпорталы РБ",
		MinSize:  Size{Width: 660, Height: 520},
		Size:     Size{Width: 700, Height: 580},
		Layout:   VBox{},
		Children: []Widget{
			TextEdit{AssignTo: &report, ReadOnly: true, VScroll: true, Text: text},
			Composite{Layout: HBox{}, Children: []Widget{
				PushButton{Text: "AVEST CSP ↗", OnClicked: func() { openTool("avest") }},
				PushButton{Text: "Регистрация МСИ ↗", OnClicked: func() { openTool("msi-reg") }},
				PushButton{Text: "ЕСА ↗", OnClicked: func() { openTool("esa") }},
				PushButton{Text: "НУЦ ↗", OnClicked: func() { openTool("nca") }},
				HSpacer{},
				PushButton{Text: "Закрыть", OnClicked: func() { dlg.Cancel() }},
			}},
		},
	}
	if err := d.Create(owner); err != nil {
		walk.MsgBox(owner, appName, err.Error(), walk.MsgBoxIconError)
		return
	}
	report.SetText(text)
	dlg.Run()
}
