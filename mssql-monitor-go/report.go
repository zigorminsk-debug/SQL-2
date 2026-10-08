// report.go — построение самодостаточного HTML-отчёта (порт report.py).
package main

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

type ReportMeta struct {
	ServerDisplay string
	ServerFull    string
	GeneratedStr  string
	Interval      int
	Driver        string
}

type Report struct {
	d        *DeepData
	findings []Finding
	health   Health
	peaks    []PeakWindow
	samples  []Sample
	blocking []BlockingEvent
	longq    []LongQuery
	meta     ReportMeta
}

func NewReport(d *DeepData, findings []Finding, health Health, peaks []PeakWindow,
	samples []Sample, blocking []BlockingEvent, longq []LongQuery,
	meta ReportMeta) *Report {
	return &Report{d: d, findings: findings, health: health, peaks: peaks,
		samples: samples, blocking: blocking, longq: longq, meta: meta}
}

// ---------- утилиты таблиц ----------

func tblCell(text, cls string) string {
	if cls != "" {
		return "<td class='" + cls + "'>" + text + "</td>"
	}
	return "<td>" + text + "</td>"
}

func tbl(headers []string, rows []string, empty string) string {
	if len(rows) == 0 {
		if empty == "" {
			empty = "Нет данных"
		}
		return "<div class='nodata'>" + esc(empty) + "</div>"
	}
	var h, b strings.Builder
	for _, x := range headers {
		fmt.Fprintf(&h, "<th>%s</th>", esc(x))
	}
	for _, r := range rows {
		fmt.Fprintf(&b, "<tr>%s</tr>", r)
	}
	return "<table class='tbl'><thead><tr>" + h.String() + "</tr></thead><tbody>" +
		b.String() + "</tbody></table>"
}

func codeBlock(text string) string {
	return "<pre class='sql'>" + esc(text) + "</pre>"
}

func tspan(ts int64) string { return unixFmt(ts, "15:04:05") }

// ---------- сборка ----------

func (r *Report) Build() string {
	title := r.meta.ServerDisplay
	if title == "" {
		title = "MS SQL Server"
	}
	head := strings.Replace(headCSS, "%TITLE%", escQ(title), 1)
	return head + r.body()
}

var navItems = [][2]string{
	{"summary", "Сводка"}, {"timeline", "Графики"}, {"peaks", "Замедления"},
	{"waits", "Ожидания"}, {"queries", "Запросы"}, {"sessions", "Сессии"},
	{"deadlocks", "Блокировки"}, {"indexes", "Индексы"}, {"databases", "Базы"},
	{"disks", "Диски"}, {"tempdb", "tempdb"}, {"config", "Настройки"},
	{"audit", "Аудит по статьям"}, {"ops", "Бэкапы"}, {"recs", "Рекомендации"},
}

func (r *Report) header() string {
	per := "нет накопленных данных (режим снимка)"
	if len(r.samples) > 0 {
		per = unixFmt(r.samples[0].TS, "02.01.2006 15:04") + " — " +
			unixFmt(r.samples[len(r.samples)-1].TS, "02.01 15:04")
	}
	h := r.health
	var nav strings.Builder
	for _, it := range navItems {
		fmt.Fprintf(&nav, "<a href='#%s'>%s</a>", it[0], esc(it[1]))
	}
	product := "—"
	if r.d != nil && r.d.Server != nil {
		product = gs(r.d.Server, "product_line")
	}
	return fmt.Sprintf(`
<div class='hero card'>
  <div class='hero-grid'>
    <div>
      <div class='brand'>MS SQL Monitor — отчёт о диагностике</div>
      <div class='h1'>%s</div>
      <table class='kv'>
        <tr><td>Версия:</td><td>%s</td></tr>
        <tr><td>Сервер / экземпляр:</td><td>%s</td></tr>
        <tr><td>Отчёт сформирован:</td><td>%s</td></tr>
        <tr><td>Период мониторинга:</td><td>%s (%s точек, интервал %s с)</td></tr>
        <tr><td>Драйвер:</td><td>%s</td></tr>
      </table>
    </div>
    <div class='hero-score'>
      %s
      <div class='grade'>%s</div>
      <div class='score-cards'>
        <span class='sc crit'>⛔ Критично: <b>%d</b></span>
        <span class='sc warn'>⚠️ Важно: <b>%d</b></span>
        <span class='sc info'>💡 Рекомендаций: <b>%d</b></span>
      </div>
    </div>
  </div>
</div>
<nav class='nav no-print' id='top'>
 %s
</nav>`,
		esc(firstNonEmpty(r.meta.ServerDisplay, "?")), esc(product),
		esc(firstNonEmpty(r.meta.ServerFull, "?")), esc(firstNonEmpty(r.meta.GeneratedStr, "—")),
		esc(per), fint(len(r.samples)), fint(r.meta.Interval), esc(r.meta.Driver),
		svgGauge(h.Score), esc(h.Grade), h.Crit, h.Warn, h.Info, nav.String())
}

func (r *Report) summary() string {
	items := ""
	n := 0
	for _, f := range r.findings {
		if n >= 6 {
			break
		}
		items += fmt.Sprintf("<li><a href='#recs' class='sevlink s-%s'>%s %s</a></li>",
			f.Severity, sevIcons[f.Severity], esc(f.Title))
		n++
	}
	if items == "" {
		items = "<li>Замечаний не найдено</li>"
	}
	errs := r.checkErrors()
	return fmt.Sprintf(`
<section class='card' id='summary'><h2>Сводка</h2>
<p class='lead'>Оценка состояния сервера — <b>%d/100</b> (%s).
Обнаружено проблем: <b class='t-crit'>%d критичных</b>, <b class='t-warn'>%d важных</b>,
%d рекомендаций. Ниже — самые значимые; полный список в разделе «Рекомендации».</p>
<ul class='toplist'>%s</ul>
%s
</section>`, r.health.Score, esc(r.health.Grade), r.health.Crit, r.health.Warn,
		r.health.Info, items, errs)
}

func (r *Report) checkErrors() string {
	if r.d == nil || len(r.d.Errors) == 0 {
		return ""
	}
	keys := make([]string, 0, len(r.d.Errors))
	for k := range r.d.Errors {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var rows strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&rows, "<li>%s — <span class='muted'>%s</span></li>", esc(k),
			esc(r.d.Errors[k]))
	}
	return "<div class='warnbox'><b>Часть проверок недоступна</b> (обычно из-за " +
		"недостатка прав; полный отчёт требует VIEW SERVER STATE):<ul>" + rows.String() +
		"</ul></div>"
}

// ---------- графики ----------

func (r *Report) timeline() string {
	if len(r.samples) < 2 {
		return "<section class='card' id='timeline'><h2>Графики нагрузки " +
			"(суточный мониторинг)</h2><div class='nodata'>Данных мониторинга пока " +
			"недостаточно. Запустите мониторинг — и на следующем отчёте здесь " +
			"появятся графики загрузки за сутки.</div></section>"
	}
	S := r.samples
	t0, t1 := S[0].TS, S[len(S)-1].TS
	var regions [][2]int64
	for _, w := range r.peaks {
		regions = append(regions, [2]int64{w.T0, w.T1})
	}
	mk := func(get func(*Sample) *float64) []linePoint {
		pts := make([]linePoint, len(S))
		for i := range S {
			pts[i] = linePoint{TS: S[i].TS, Val: get(&S[i])}
		}
		return pts
	}
	ym100 := 100.0
	cpu := svgLine([]lineSeries{
		{Name: "SQL CPU, %", Color: "#1a5fb4", Points: mk(func(s *Sample) *float64 { return s.SQLCPU })},
		{Name: "Общий CPU, %", Color: "#e8710a", Dash: true,
			Points: mk(func(s *Sample) *float64 { return s.SysCPU })},
	}, t0, t1, 0, &ym100, 0, "%", regions)
	batch := svgLine([]lineSeries{
		{Name: "Batch req/sec", Color: "#188038",
			Points: mk(func(s *Sample) *float64 { return s.BatchReq })},
	}, t0, t1, 0, nil, 0, "запр/с", regions)
	ple := svgLine([]lineSeries{
		{Name: "PLE", Color: "#c62828",
			Points: mk(func(s *Sample) *float64 {
				if s.PLE == nil {
					return nil
				}
				v := float64(*s.PLE)
				return &v
			})},
	}, t0, t1, 0, nil, 0, "с", regions)
	mem := svgLine([]lineSeries{
		{Name: "Занято SQL, МБ", Color: "#1a5fb4",
			Points: mk(func(s *Sample) *float64 { return s.MemTotalMB })},
		{Name: "Цель SQL, МБ", Color: "#f9a825", Dash: true,
			Points: mk(func(s *Sample) *float64 { return s.MemTargetMB })},
	}, t0, t1, 0, nil, 0, "МБ", regions)
	io := svgLine([]lineSeries{
		{Name: "Чтение, мс", Color: "#e8710a",
			Points: mk(func(s *Sample) *float64 { return s.IORdLat })},
		{Name: "Запись, мс", Color: "#00838f",
			Points: mk(func(s *Sample) *float64 { return s.IOWrLat })},
	}, t0, t1, 0, nil, 0, "мс", regions)
	tmp := svgLine([]lineSeries{
		{Name: "tempdb занято, МБ", Color: "#5d4037",
			Points: mk(func(s *Sample) *float64 { return s.TempDBUsedMB })},
	}, t0, t1, 0, nil, 0, "МБ", regions)

	var blockedItems []hbarItem
	for i := range S {
		if S[i].Blocked > 0 {
			blockedItems = append(blockedItems, hbarItem{
				Label: tspan(S[i].TS) + " ⛔", Val: float64(S[i].Blocked)})
		}
	}
	blockedChart := "<div class='nodata'>Блокировок за период не зафиксировано</div>"
	if len(blockedItems) > 0 {
		blockedChart = htmlHbars(blockedItems, "эпизодов блокировки в минуту",
			func(v float64) string { return fint(v) }, nil)
	}
	legend := ""
	if len(regions) > 0 {
		legend = "<span class='lg'><span class='sw region'></span> окно замедления</span>"
	}
	return fmt.Sprintf(`
<section class='card' id='timeline'><h2>Графики нагрузки (суточный мониторинг)</h2>
<p class='muted'>Период: %s — %s, точек: %s. <div class='legend no-print'>%s</div></p>
%s
<div class='chart-block'><h3>Загрузка процессора</h3>%s
 <div class='legend'><span class='lg'><span class='sw' style='background:#1a5fb4'></span>SQL Server, %%</span>
 <span class='lg'><span class='sw' style='background:#e8710a'></span>система в целом, %%</span></div></div>
<div class='chart-block'><h3>Скорость обработки — запросов в секунду (Batch Requests/sec)</h3>%s</div>
<div class='chart-block'><h3>Время жизни страниц в памяти (Page Life Expectancy)</h3>%s
 <p class='muted'>300 с — эвристика, не норматив. Сравните PLE с эталоном системы, lazy writes и физическими чтениями.</p></div>
<div class='chart-block'><h3>Память SQL Server</h3>%s</div>
<div class='chart-block'><h3>Задержки ввода-вывода (мс на операцию)</h3>%s
 <p class='muted'>Норма: до 10–20 мс на чтение и запись. Выше 30 мс — диск не справляется.</p></div>
<div class='chart-block'><h3>tempdb — занято, МБ</h3>%s</div>
<div class='chart-block'><h3>Блокировки по времени</h3>%s</div>
</section>`,
		esc(unixFmt(t0, "02.01.2006 15:04")), esc(unixFmt(t1, "02.01 15:04")),
		fint(len(S)), legend, r.timelineStats(), cpu, batch, ple, mem, io, tmp, blockedChart)
}

func (r *Report) timelineStats() string {
	S := r.samples
	agg := func(get func(*Sample) *float64, fn func([]float64) float64) string {
		var vals []float64
		for i := range S {
			if v := get(&S[i]); v != nil {
				vals = append(vals, *v)
			}
		}
		if len(vals) == 0 {
			return "—"
		}
		return fnum(fn(vals), 1)
	}
	mean := func(v []float64) float64 {
		s := 0.0
		for _, x := range v {
			s += x
		}
		return s / float64(len(v))
	}
	blockedTicks := 0
	var maxWait []float64
	for i := range S {
		if S[i].Blocked > 0 {
			blockedTicks++
		}
		if S[i].BlockedMaxWait > 0 {
			maxWait = append(maxWait, S[i].BlockedMaxWait)
		}
	}
	maxWaitStr := "—"
	if len(maxWait) > 0 {
		maxWaitStr = fhms(maxOf(maxWait))
	}
	cells := [][2]string{
		{"CPU SQL (средн./макс.)", agg(func(s *Sample) *float64 { return s.SQLCPU }, mean) +
			"% / " + agg(func(s *Sample) *float64 { return s.SQLCPU }, maxOf) + "%"},
		{"Запросов/с (средн./макс.)",
			agg(func(s *Sample) *float64 { return s.BatchReq }, mean) + " / " +
				agg(func(s *Sample) *float64 { return s.BatchReq }, maxOf)},
		{"PLE (мин.)", agg(func(s *Sample) *float64 {
			if s.PLE == nil {
				return nil
			}
			v := float64(*s.PLE)
			return &v
		}, minOf)},
		{"Буферный кэш hit, %", agg(func(s *Sample) *float64 { return s.BCHR }, mean)},
		{"Задержка чтения, мс (макс.)", agg(func(s *Sample) *float64 { return s.IORdLat }, maxOf)},
		{"Задержка записи, мс (макс.)", agg(func(s *Sample) *float64 { return s.IOWrLat }, maxOf)},
		{"Тиков с блокировками", fint(blockedTicks)},
		{"Макс. ожидание блокировки", maxWaitStr},
	}
	var rows strings.Builder
	for _, c := range cells {
		fmt.Fprintf(&rows, "<div class='stat'><div class='stat-v'>%s</div>"+
			"<div class='stat-k'>%s</div></div>", c[1], esc(c[0]))
	}
	return "<div class='stats'>" + rows.String() + "</div>"
}

// ---------- замедления ----------

func (r *Report) peaksSection() string {
	if len(r.peaks) == 0 {
		return "<section class='card' id='peaks'><h2>Анализ замедлений</h2>" +
			"<div class='nodata'>Явных окон замедления на таймлайне не выделено " +
			"(или данных мониторинга недостаточно).</div></section>"
	}
	var blocks strings.Builder
	for i, w := range r.peaks {
		var waitItems []hbarItem
		for _, wt := range w.Waits {
			waitItems = append(waitItems, hbarItem{Label: wt.Type, Val: wt.MS * 1000.0,
				Tip: fmt.Sprintf("%s: %s", wt.Type, fmtMSru(wt.MS*1000.0))})
		}
		var blRows []string
		for j, e := range w.Blocked {
			if j >= 12 {
				break
			}
			blRows = append(blRows, tblCell(tspan(e.TS), "")+tblCell(fint(e.SessionID), "")+
				tblCell("← "+fmt.Sprint(e.BlockingSessionID), "")+tblCell(esc(e.WaitType), "")+
				tblCell(fnum(e.WaitTimeS, 0)+" с", "")+
				tblCell("<code class='small'>"+esc(strHead(e.TextHead, 120))+"</code>", ""))
		}
		sortedLong := append([]LongQuery(nil), w.Long...)
		sort.SliceStable(sortedLong, func(a, b int) bool { return sortedLong[a].ElapsedS > sortedLong[b].ElapsedS })
		var lqRows []string
		for j, q := range sortedLong {
			if j >= 8 {
				break
			}
			lqRows = append(lqRows, tblCell(tspan(q.TS), "")+tblCell(fint(q.SessionID), "")+
				tblCell(fhms(q.ElapsedS), "")+tblCell(fhms(q.CPUS), "")+
				tblCell(fint(q.LogicalReads), "")+
				tblCell("<code class='small'>"+esc(strHead(q.TextHead, 120))+"</code>", ""))
		}
		var reasons strings.Builder
		for _, rs := range w.Reasons {
			fmt.Fprintf(&reasons, "<li>%s</li>", esc(rs))
		}
		pleStr := "—"
		if w.PLEMinSet {
			pleStr = fnum(w.PLEMin, 0)
		}
		waitsHTML := ""
		if len(waitItems) > 0 {
			waitsHTML = "<h4>Ожидания в этом окне</h4>" +
				htmlHbars(waitItems, "время ожидания", func(v float64) string { return fmtMSru(v) }, nil)
		}
		blTbl := ""
		if len(blRows) > 0 {
			blTbl = "<h4>Блокировки в этом окне</h4>" +
				tbl([]string{"Время", "Сессия", "Блокирует", "Тип ожидания", "Ожидание", "Запрос"}, blRows, "")
		}
		lqTbl := ""
		if len(lqRows) > 0 {
			lqTbl = "<h4>Долгие запросы в этом окне</h4>" +
				tbl([]string{"Время", "Сессия", "Длительность", "CPU", "Чтений", "Запрос"}, lqRows, "")
		}
		fmt.Fprintf(&blocks, `
<div class='peak'>
 <h3>Окно №%d: %s — %s (%d мин, %d точек)</h3>
 <div class='peak-stats'>CPU: <b>%s%%</b> (макс. %s%%) · запросов/с: <b>%s</b> ·
   блокировок: <b class='t-warn'>%d</b> · PLE мин.: <b>%s</b> с</div>
 <div class='reasons'><b>Причины замедления:</b><ul>%s</ul></div>
 %s
 %s
 %s
</div>`, i+1, esc(unixFmt(w.T0, "02.01 15:04")), esc(unixFmt(w.T1, "15:04")),
			(w.T1-w.T0)/60, w.N, fnum(w.CPUAvg, 0), fnum(w.CPUMax, 0), fnum(w.BatchAvg, 1),
			len(w.Blocked), pleStr, reasons.String(), waitsHTML, blTbl, lqTbl)
	}
	return "<section class='card' id='peaks'><h2>Анализ замедлений — разбор причин</h2>" +
		"<p class='muted'>Автоматически выделены интервалы повышенной нагрузки и " +
		"проблем. Для каждого окна приведены доминирующие ожидания, блокировки и " +
		"долгие запросы именно этого периода.</p>" + blocks.String() + "</section>"
}

// ---------- ожидания ----------

func (r *Report) waits() string {
	if r.d == nil || r.d.WaitStats == nil || len(r.d.WaitStats.Rows) == 0 {
		return "<section class='card' id='waits'><h2>Ожидания (wait stats)</h2>" +
			"<div class='nodata'>Данные ожиданий недоступны.</div></section>"
	}
	ws := r.d.WaitStats
	total := ws.TotalMS
	if total == 0 {
		total = 1
	}
	cats := waitCategoryTotals(ws.Rows)
	type kv struct {
		k string
		v float64
	}
	var catList []kv
	for k, v := range cats {
		catList = append(catList, kv{k, v})
	}
	sort.Slice(catList, func(i, j int) bool { return catList[i].v > catList[j].v })
	var donutParts []donutPart
	for _, c := range catList {
		if c.v > 0 {
			donutParts = append(donutParts, donutPart{
				Label: waitCategories[c.k].Title, Val: c.v,
				Color: catColor(c.k)})
		}
	}
	var trows []string
	for i, wr := range ws.Rows {
		if i >= 25 {
			break
		}
		e := waitInfo(wr.Type)
		share := 100.0 * wr.WaitMS / total
		trows = append(trows,
			tblCell("<span class='sev-dot' style='background:"+catColor(e.Cat)+
				"'></span>"+esc(wr.Type), "")+
				tblCell(esc(e.Title), "nowrap")+
				tblCell(fnum(share, 1)+"%", "")+
				tblCell(fmtMSru(wr.WaitMS), "")+
				tblCell(fmtMSru(wr.MaxMS), "")+
				tblCell(fint(wr.Tasks), ""))
	}
	var catRows strings.Builder
	for _, c := range catList {
		if c.v <= total*0.005 {
			continue
		}
		cat := waitCategories[c.k]
		var acts strings.Builder
		for _, a := range cat.Actions {
			fmt.Fprintf(&acts, "<li>%s</li>", esc(a))
		}
		fmt.Fprintf(&catRows, "<div class='cat-card'><div class='cat-h'>"+
			"<span class='sev-dot' style='background:%s'></span><b>%s</b> — %s</div>"+
			"<div class='muted'>%s</div><ul>%s</ul></div>",
			catColor(c.k), esc(cat.Title), fmtMSru(c.v), esc(cat.Desc), acts.String())
	}
	var legend strings.Builder
	for _, p := range donutParts {
		fmt.Fprintf(&legend, "<span class='lg'><span class='sw' style='background:%s'>"+
			"</span>%s</span>", p.Color, esc(p.Label))
	}
	return fmt.Sprintf(`
<section class='card' id='waits'><h2>Ожидания (wait stats) — на что тратится время</h2>
<div class='waits-grid'>
  <div class='waits-left'>%s
    <div class='legend'>%s</div>
    <p class='muted'>Signal wait: <b>%s%%</b> (доля ожидания процессора; &gt;20%% — нехватка CPU)</p>
  </div>
  <div>%s</div>
</div>
<h3>Категории ожиданий и что с ними делать</h3>
<div class='cats'>%s</div>
</section>`,
		svgDonut(donutParts, "ожидания"), legend.String(),
		fnum(100.0*ws.SignalMS/total, 1),
		tbl([]string{"Тип", "Что означает", "Доля", "Всего", "Макс.", "Ожиданий (раз)"}, trows, ""),
		catRows.String())
}

func catColor(cat string) string {
	if c, ok := catColors[cat]; ok {
		return c
	}
	return "#999"
}

// ---------- топ запросов ----------

func qtableRows(lst []map[string]any) []string {
	var rows []string
	for i, m := range lst {
		if i >= 15 {
			break
		}
		rows = append(rows,
			tblCell(fint(gfi(m, "execution_count")), "")+
				tblCell(fnum(gfi(m, "avg_elapsed_ms"), 0)+" мс", "")+
				tblCell(fnum(gfi(m, "avg_worker_ms"), 0)+" мс", "")+
				tblCell(fint(gfi(m, "total_logical_reads")), "")+
				tblCell(esc(gs(m, "db_name")), "")+
				tblCell(esc(orDash(gs(m, "object_name"))), "")+
				tblCell("<details><summary>показать</summary><code class='small'>"+
					esc(strHead(gs(m, "sql_text"), 1500))+"</code></details>", ""))
	}
	return rows
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func (r *Report) queries() string {
	out := "<section class='card' id='queries'><h2>Топ запросов (скорость обработки)</h2>"
	if r.d == nil || r.d.QStats == nil {
		return out + "<div class='nodata'>Статистика запросов недоступна.</div></section>"
	}
	qs := r.d.QStats
	qtable := func(lst []map[string]any, title, note string) string {
		return "<h3>" + esc(title) + "</h3><p class='muted'>" + esc(note) + "</p>" +
			tbl([]string{"Выполнений", "Ср. длит.", "Ср. CPU", "Чтений (всего)", "База",
				"Объект", "Текст"}, qtableRows(lst), "")
	}
	out += qtable(qs.ByCPU, "Самые дорогие по CPU",
		"Суммарное время процессора с момента запуска сервера.")
	out += qtable(qs.ByAvg, "Самые долгие в среднем",
		"Средняя длительность выполнения (не менее 10 выполнений).")
	out += qtable(qs.ByReads, "Самые «читающие»", "Суммарные логические чтения страниц.")
	if len(qs.DupPlans) > 0 {
		var drows []string
		for _, d := range qs.DupPlans {
			drows = append(drows, tblCell(fint(gfi(d, "plan_count")), "")+
				tblCell(fint(gfi(d, "execs")), "")+
				tblCell("<code class='small'>"+esc(strHead(gs(d, "sql_text"), 300))+"</code>", ""))
		}
		out += "<h3>Один запрос — много планов (возможен parameter sniffing)</h3>" +
			tbl([]string{"Планов", "Выполнений", "Текст"}, drows, "")
	}
	return out + "</section>"
}

// ---------- сессии и блокировки ----------

func (r *Report) sessions() string {
	lr := r.d.Live
	var rows []string
	if lr != nil {
		sorted := append([]map[string]any(nil), lr.Requests...)
		sort.SliceStable(sorted, func(a, b int) bool {
			return gfi(sorted[a], "elapsed_s") > gfi(sorted[b], "elapsed_s")
		})
		for i, m := range sorted {
			if i >= 20 {
				break
			}
			bl := gii(m, "blocking_session_id")
			blStr := "—"
			if bl != 0 {
				blStr = "<span class='t-warn'>← " + fint(bl) + "</span>"
			}
			rows = append(rows,
				tblCell(fint(gii(m, "session_id")), "")+
					tblCell(esc(gs(m, "status")), "")+
					tblCell(fhms(gfi(m, "elapsed_s")), "")+
					tblCell(fhms(gfi(m, "cpu_s")), "")+
					tblCell(blStr, "")+
					tblCell(esc(gs(m, "wait_type")), "")+
					tblCell(esc(gs(m, "login_name")), "")+
					tblCell("<details><summary>"+esc(gs(m, "command"))+"</summary>"+
						"<code class='small'>"+esc(strHead(gs(m, "sql_text"), 1200))+
						"</code></details>", ""))
		}
	}
	var btbl []string
	if lr != nil {
		for _, b := range lr.Blockers {
			btbl = append(btbl,
				tblCell(fint(gii(b, "session_id")), "")+
					tblCell(esc(gs(b, "status")), "")+
					tblCell(fint(gii(b, "tran_count")), "")+
					tblCell(esc(gs(b, "login_name")), "")+
					tblCell(esc(gs(b, "program_name")), "")+
					tblCell("<code class='small'>"+esc(strHead(gs(b, "sql_text"), 300))+"</code>", ""))
		}
	}
	blockingSection := ""
	if len(r.blocking) > 0 {
		seen := map[string]bool{}
		var brows []string
		for _, e := range r.blocking {
			key := fmt.Sprintf("%d|%d|%s", e.SessionID, e.BlockingSessionID, e.TextHead)
			if seen[key] {
				continue
			}
			seen[key] = true
			brows = append(brows,
				tblCell(esc(unixFmt(e.TS, "02.01 15:04")), "")+
					tblCell(fint(e.SessionID), "")+
					tblCell("<span class='t-warn'>← "+fint(e.BlockingSessionID)+"</span>", "")+
					tblCell(esc(e.WaitType), "")+
					tblCell(fnum(e.WaitTimeS, 0)+" с", "")+
					tblCell("<details><summary>"+esc(e.Command)+"</summary>"+
						"<code class='small'>"+esc(strHead(e.TextHead, 1000))+"</code></details>", ""))
		}
		blockingSection = fmt.Sprintf("<h3>История блокировок за период мониторинга "+
			"(%d эпизодов)</h3>%s", len(r.blocking),
			tbl([]string{"Время", "Сессия", "Блокирует", "Тип", "Ожидание", "Запрос"}, brows, ""))
	}
	blSection := "<p class='muted'>Активных блокировщиков на момент снимка нет.</p>"
	if lr != nil && len(lr.Blockers) > 0 {
		blSection = "<h3>Сессии-«виновники» блокировок (head blockers)</h3>" +
			tbl([]string{"Сессия", "Статус", "Транзакций", "Логин", "Приложение",
				"Последний запрос"}, btbl, "Сейчас активных блокировщиков нет")
	}
	return fmt.Sprintf(`
<section class='card' id='sessions'><h2>Активные сессии и блокировки</h2>
<h3>Запросы на момент диагностики</h3>
%s
%s
%s
</section>`,
		tbl([]string{"Сессия", "Статус", "Длится", "CPU", "Блокир.", "Ожидание", "Логин",
			"Запрос"}, rows, "Активных пользовательских запросов нет"),
		blSection, blockingSection)
}

func (r *Report) longQueries() string {
	if len(r.longq) == 0 {
		return ""
	}
	sorted := append([]LongQuery(nil), r.longq...)
	sort.SliceStable(sorted, func(a, b int) bool { return sorted[a].ElapsedS > sorted[b].ElapsedS })
	var rows []string
	for i, q := range sorted {
		if i >= 40 {
			break
		}
		rows = append(rows,
			tblCell(esc(unixFmt(q.TS, "02.01 15:04:05")), "")+
				tblCell(fint(q.SessionID), "")+
				tblCell(fhms(q.ElapsedS), "")+
				tblCell(fhms(q.CPUS), "")+
				tblCell(fint(q.LogicalReads), "")+
				tblCell(esc(q.DB), "")+
				tblCell("<details><summary>"+esc(q.Command)+"</summary>"+
					"<code class='small'>"+esc(strHead(q.TextHead, 1200))+"</code></details>", ""))
	}
	return "<section class='card' id='longq'><h2>Долгие запросы за период " +
		"мониторинга</h2>" + tbl([]string{"Время", "Сессия", "Длительность", "CPU",
		"Чтений", "База", "Запрос"}, rows, "") + "</section>"
}

func (r *Report) deadlocks() string {
	dls := r.d.Deadlocks
	if len(dls) == 0 {
		return "<section class='card' id='deadlocks'><h2>Взаимоблокировки " +
			"(deadlock)</h2><div class='nodata'>За последние 48 ч " +
			"взаимоблокировок не зафиксировано.</div></section>"
	}
	var blocks strings.Builder
	for i, d := range dls {
		var prows []string
		for _, p := range d.Processes {
			victim := ""
			if p.ID == d.Victim {
				victim = "⛔ жертва"
			}
			prows = append(prows,
				tblCell(victim, "")+tblCell(fint(parseIntSafe(p.Spid)), "")+
					tblCell(esc(p.Hostname), "")+tblCell(esc(p.Loginname), "")+
					tblCell(esc(p.Isolation), "")+tblCell(esc(p.Waitresource), "")+
					tblCell("<code class='small'>"+esc(strHead(p.Inputbuf, 500))+"</code>", ""))
		}
		objs := "—"
		if len(d.Objects) > 0 {
			objs = strings.Join(d.Objects, ", ")
		}
		timeStr := "—"
		if d.Time != nil {
			timeStr = d.Time.Format("02.01.2006 15:04")
		}
		fmt.Fprintf(&blocks, `
<div class='peak'><h3>Взаимоблокировка №%d — %s</h3>
<p class='muted'>Объекты: %s</p>
%s</div>`, i+1, esc(timeStr), esc(objs),
			tbl([]string{"", "SPID", "Хост", "Логин", "Изоляция", "Ресурс", "Запрос"}, prows, ""))
	}
	return fmt.Sprintf("<section class='card' id='deadlocks'><h2>Взаимоблокировки "+
		"(deadlock) — %d за последние 48 ч</h2>%s</section>", len(dls), blocks.String())
}

func parseIntSafe(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// ---------- индексы ----------

func (r *Report) indexes() string {
	out := "<section class='card' id='indexes'><h2>Индексы и статистика</h2>"
	mi := r.d.MissingIdx
	if len(mi) > 0 {
		var rows []string
		for i, m := range mi {
			if i >= 20 {
				break
			}
			rows = append(rows,
				tblCell(esc(gs(m, "db")), "")+
					tblCell("<code>"+esc(gs(m, "obj"))+"</code>", "")+
					tblCell(esc(orDash(gs(m, "equality_columns"))), "")+
					tblCell(esc(orDash(gs(m, "inequality_columns"))), "")+
					tblCell(esc(orDash(gs(m, "included_columns"))), "")+
					tblCell(fint(gfi(m, "user_seeks")), "")+
					tblCell(fnum(gfi(m, "avg_user_impact"), 0)+"%", ""))
		}
		out += fmt.Sprintf("<h3>Отсутствующие индексы (%d рекомендаций)</h3>"+
			"<p class='muted'>Оценка потенциального эффекта от оптимизатора. "+
			"Проверяйте на тестовой среде — индексы замедляют изменение данных.</p>%s",
			len(mi), tbl([]string{"База", "Таблица", "Колонки (равенство)",
				"Колонки (диапазон)", "INCLUDE", "Seeks", "Эффект"}, rows, ""))
		var ddl strings.Builder
		for i, m := range mi {
			if i >= 15 {
				break
			}
			if gs(m, "ddl") == "" {
				continue
			}
			fmt.Fprintf(&ddl, "<details class='ddl'><summary>%s</summary>%s</details>",
				esc(lastPart(gs(m, "obj"), ".")), codeBlock(gs(m, "ddl")))
		}
		out += "<h4>Готовые DDL</h4>" + ddl.String()
	}
	frag := r.d.Frag
	if len(frag) > 0 {
		var rows []string
		for i, m := range frag {
			if i >= 30 {
				break
			}
			f := gfi(m, "frag")
			act := "REORGANIZE"
			cls := "warn"
			if f >= 30 {
				act, cls = "REBUILD", "crit"
			}
			rows = append(rows,
				tblCell(esc(gs(m, "db")), "")+
					tblCell(esc(gs(m, "sch"))+"."+esc(gs(m, "tbl")), "")+
					tblCell(esc(gs(m, "ix_name")), "")+
					tblCell("<span class='t-"+cls+"'>"+fnum(f, 1)+"%</span>", "")+
					tblCell(fint(gfi(m, "page_count")), "")+
					tblCell("<code>"+act+"</code>", ""))
		}
		out += "<h3>Фрагментация индексов (топ-30)</h3>" +
			tbl([]string{"База", "Таблица", "Индекс", "Фрагментация", "Страниц (8КБ)",
				"Действие"}, rows, "")
	}
	un := r.d.UnusedIdx
	if len(un) > 0 {
		var rows []string
		for i, m := range un {
			if i >= 25 {
				break
			}
			rows = append(rows,
				tblCell(esc(gs(m, "db")), "")+
					tblCell(esc(gs(m, "sch"))+"."+esc(gs(m, "tbl")), "")+
					tblCell(esc(gs(m, "ix_name")), "")+
					tblCell(fint(gfi(m, "updates")), "")+
					tblCell(fnum(gfi(m, "size_mb"), 0)+" МБ", "")+
					tblCell(fnum(gfi(m, "reads"), 0), ""))
		}
		out += "<h3>Неиспользуемые индексы (кандидаты на удаление)</h3>" +
			"<p class='muted'>Счётчики использования сбрасываются при перезапуске " +
			"SQL Server — учитывайте аптайм.</p>" +
			tbl([]string{"База", "Таблица", "Индекс", "Изменений", "Размер", "Чтений"}, rows, "")
	}
	st := r.d.Stats
	if len(st) > 0 {
		var bad []map[string]any
		for _, m := range st {
			rowsN := gfi(m, "rows")
			mods := gfi(m, "modification_counter")
			stale := rowsN > 10000 && mods > 0.25*rowsN
			if !stale {
				if h, ok := hoursAgo(m, "last_updated"); ok {
					stale = h > 336 && mods > 0.05*rowsN
				}
			}
			if stale {
				bad = append(bad, m)
			}
		}
		sort.SliceStable(bad, func(a, b int) bool {
			return gfi(bad[a], "modification_counter") > gfi(bad[b], "modification_counter")
		})
		if len(bad) > 0 {
			var rows []string
			for i, m := range bad {
				if i >= 25 {
					break
				}
				lu := "—"
				if t, ok := gtime(m, "last_updated"); ok {
					lu = t.Format("02.01.2006 15:04")
				}
				rows = append(rows,
					tblCell(esc(gs(m, "db")), "")+
						tblCell(esc(gs(m, "sch"))+"."+esc(gs(m, "tbl")), "")+
						tblCell(esc(gs(m, "stat_name")), "")+
						tblCell(fint(gfi(m, "rows")), "")+
						tblCell(fint(gfi(m, "modification_counter")), "")+
						tblCell(esc(lu), ""))
			}
			out += fmt.Sprintf("<h3>Устаревшая статистика (%d)</h3>%s", len(bad),
				tbl([]string{"База", "Таблица", "Статистика", "Строк", "Изменено",
					"Обновлялась"}, rows, ""))
		} else {
			out += "<h3>Устаревшая статистика</h3><div class='okbox'>Устаревшей статистики не найдено.</div>"
		}
	}
	if len(mi) == 0 && len(frag) == 0 && len(un) == 0 && len(st) == 0 {
		out += "<div class='nodata'>Данные по индексам недоступны.</div>"
	}
	return out + "</section>"
}

// ---------- базы/файлы ----------

func (r *Report) databases() string {
	if r.d.DBs == nil {
		return ""
	}
	var rows []string
	for _, m := range r.d.DBs.Databases {
		logPct := "—"
		if v, ok := gf(m, "log_used_pct"); ok {
			logPct = fnum(v, 0) + "%"
		}
		vlf := "—"
		if v, ok := gf(m, "vlf"); ok {
			vlf = fint(v)
		}
		rcsi := "—"
		if gii(m, "is_read_committed_snapshot_on") == 1 {
			rcsi = "✓"
		}
		rows = append(rows,
			tblCell("<b>"+esc(gs(m, "name"))+"</b>", "")+
				tblCell(esc(gs(m, "state_desc")), "")+
				tblCell(esc(gs(m, "recovery_model_desc")), "")+
				tblCell(fint(gii(m, "compatibility_level")), "")+
				tblCell(rcsi, "")+
				tblCell(esc(gs(m, "log_reuse_wait_desc")), "")+
				tblCell(fnum(gfi(m, "data_mb"), 0), "")+
				tblCell(fnum(gfi(m, "log_mb"), 0), "")+
				tblCell(logPct, "")+
				tblCell(vlf, ""))
	}
	var frows []string
	for i, f := range r.d.DBs.Files {
		if i >= 120 {
			break
		}
		growth := "—"
		if gii(f, "is_percent_growth") == 1 {
			growth = fint(gfi(f, "growth")) + "%"
		} else {
			growth = fnum(gfi(f, "growth")*8, 0) + " МБ"
		}
		frows = append(frows,
			tblCell(esc(gs(f, "db")), "")+
				tblCell(esc(gs(f, "logical_name")), "")+
				tblCell(esc(gs(f, "type_desc")), "")+
				tblCell(fnum(gfi(f, "size_mb"), 0), "")+
				tblCell(growth, "")+
				tblCell("<span class='path'>"+esc(gs(f, "physical_name"))+"</span>", ""))
	}
	return fmt.Sprintf(`
<section class='card' id='databases'><h2>Базы данных и файлы</h2>
%s
<h3>Файлы и настройки роста</h3>
%s
</section>`,
		tbl([]string{"База", "Состояние", "Модель восст.", "Compat", "RCSI",
			"log_reuse_wait", "Данные, МБ", "Журнал, МБ", "Журнал занят", "VLF"}, rows, ""),
		tbl([]string{"База", "Файл", "Тип", "Размер, МБ", "Автоприращение", "Путь"}, frows, ""))
}

func (r *Report) disks() string {
	var rows []string
	for _, m := range r.d.IOFiles {
		rd := gfi(m, "num_of_reads")
		wr := gfi(m, "num_of_writes")
		rl, rlOK := 0.0, false
		wl, wlOK := 0.0, false
		if rd > 100 {
			rl, rlOK = gfi(m, "io_stall_read_ms")/rd, true
		}
		if wr > 100 {
			wl, wlOK = gfi(m, "io_stall_write_ms")/wr, true
		}
		cls := func(v float64, ok bool, lim float64) string {
			if !ok {
				return ""
			}
			if v >= lim*2 {
				return "t-crit"
			}
			if v >= lim {
				return "t-warn"
			}
			return "t-ok"
		}
		fmtRL, fmtWL := "—", "—"
		if rlOK {
			fmtRL = "<span class='" + cls(rl, true, 20) + "'>" + fnum(rl, 1) + "</span>"
		}
		if wlOK {
			fmtWL = "<span class='" + cls(wl, true, 20) + "'>" + fnum(wl, 1) + "</span>"
		}
		rows = append(rows,
			tblCell(esc(gs(m, "db")), "")+
				tblCell(esc(gs(m, "logical_name")), "")+
				tblCell(fint(rd), "")+
				tblCell(fmtRL, "")+
				tblCell(fint(wr), "")+
				tblCell(fmtWL, "")+
				tblCell(fnum(gfi(m, "size_on_disk_bytes")/1073741824.0, 1), ""))
	}
	var vrows []string
	for _, v := range r.d.Volumes {
		tot := gfi(v, "total_gb")
		fr := gfi(v, "free_gb")
		pct := 0.0
		if tot > 0 {
			pct = 100 * fr / tot
		}
		cls := "t-ok"
		if pct < 10 {
			cls = "t-crit"
		} else if pct < 20 {
			cls = "t-warn"
		}
		vrows = append(vrows,
			tblCell(esc(gs(v, "volume_mount_point")), "")+
				tblCell(esc(gs(v, "file_system_type")), "")+
				tblCell(fnum(tot, 1), "")+
				tblCell(fnum(fr, 1), "")+
				tblCell("<span class='"+cls+"'>"+fnum(pct, 1)+"%</span>", ""))
	}
	return fmt.Sprintf(`
<section class='card' id='disks'><h2>Диски и ввод-вывод</h2>
<h3>Задержки по файлам (с момента запуска сервера)</h3>
<p class='muted'>Зелёный — норма, жёлтый — повышенные (&gt;20 мс), красный — высокие (&gt;40 мс).</p>
%s
<h3>Тома (свободное место)</h3>
%s
</section>`,
		tbl([]string{"База", "Файл", "Чтений", "Ср. чтение, мс", "Записей",
			"Ср. запись, мс", "Размер, ГБ"}, rows, ""),
		tbl([]string{"Том", "ФС", "Всего, ГБ", "Свободно, ГБ", "Свободно %"}, vrows, ""))
}

func (r *Report) tempdbSection() string {
	if r.d.Tempdb == nil {
		return ""
	}
	td := r.d.Tempdb
	dataFiles := 0
	for _, f := range td.Files {
		if gs(f, "type_desc") == "ROWS" {
			dataFiles++
		}
	}
	var frows []string
	for _, f := range td.Files {
		frows = append(frows,
			tblCell(esc(gs(f, "name")), "")+
				tblCell(fnum(gfi(f, "size_mb"), 0), "")+
				tblCell("<span class='path'>"+esc(gs(f, "physical_name"))+"</span>", ""))
	}
	var trows []string
	for _, t := range td.SnapshotTR {
		trows = append(trows,
			tblCell(fint(gii(t, "session_id")), "")+
				tblCell(fhms(gfi(t, "elapsed_time_seconds")), "")+
				tblCell(esc(gs(t, "db")), ""))
	}
	sp := td.Space
	if sp == nil {
		sp = map[string]any{}
	}
	return fmt.Sprintf(`
<section class='card' id='tempdb'><h2>tempdb</h2>
<div class='stats'>
 <div class='stat'><div class='stat-v'>%d</div><div class='stat-k'>файлов данных</div></div>
 <div class='stat'><div class='stat-v'>%s МБ</div><div class='stat-k'>пользовательские объекты</div></div>
 <div class='stat'><div class='stat-v'>%s МБ</div><div class='stat-k'>внутренние объекты</div></div>
 <div class='stat'><div class='stat-v'>%s МБ</div><div class='stat-k'>version store</div></div>
 <div class='stat'><div class='stat-v'>%s МБ</div><div class='stat-k'>свободно</div></div>
</div>
%s
<h3>Длинные транзакции снимков (держат version store)</h3>
%s
</section>`, dataFiles, fnum(gfi(sp, "user_mb"), 0), fnum(gfi(sp, "internal_mb"), 0),
		fnum(gfi(sp, "version_mb"), 0), fnum(gfi(sp, "free_mb"), 0),
		tbl([]string{"Файл", "Размер, МБ", "Путь"}, frows, ""),
		tbl([]string{"Сессия", "Длится", "База"}, trows, "Нет активных транзакций снимков"))
}

// ---------- настройки ----------

var configRecs = [][2]string{
	{"max server memory (MB)", "Лимит с резервом ОС, служб и других инстансов; по измерениям"},
	{"max degree of parallelism", "По профилю нагрузки, online CPU и NUMA; универсального значения нет"},
	{"cost threshold for parallelism", "По планам и дельтам waits; 5 — повод проверить, не диагноз"},
	{"optimize for ad hoc workloads", "1 (включить)"},
	{"max worker threads", "0 (авто) — менять только при THREADPOOL-проблемах"},
	{"clr enabled", "0 (без необходимости не включать)"},
	{"xp_cmdshell", "0 (безопасность)"},
	{"priority boost", "0 (всегда!)"},
	{"lightweight pooling", "0 (всегда!)"},
	{"remote query timeout (s)", "600 по умолчанию"},
	{"blocked process threshold (s)", "5 — чтобы ловить блокировки"},
}

func (r *Report) config() string {
	cfg := map[string]map[string]any{}
	for _, m := range r.d.Configs {
		cfg[strings.ToLower(gs(m, "name"))] = m
	}
	var rows []string
	for _, cr := range configRecs {
		m := cfg[strings.ToLower(cr[0])]
		if m == nil {
			continue
		}
		rows = append(rows,
			tblCell(esc(cr[0]), "")+
				tblCell("<b>"+esc(gs(m, "value_in_use"))+"</b>", "")+
				tblCell(esc(cr[1]), "")+
				tblCell("<span class='muted'>—</span>", ""))
	}
	si := r.d.SysInfo
	if si == nil {
		si = map[string]any{}
	}
	uptime := ""
	if st, ok := gtime(si, "sqlserver_start_time"); ok {
		sec := time.Since(st).Seconds()
		uptime = fmt.Sprintf("%d дн %d ч", int(sec)/86400, (int(sec)%86400)/3600)
	}
	srv := r.d.Server
	if srv == nil {
		srv = map[string]any{}
	}
	phys := "—"
	if v, ok := gf(si, "phys_kb"); ok {
		phys = fnum(v/1048576.0, 1) + " ГБ"
	}
	inUse := "—"
	if v, ok := gf(si, "in_use_kb"); ok {
		inUse = fnum(v/1048576.0, 1) + " ГБ"
	}
	started := "—"
	if st, ok := gtime(si, "sqlserver_start_time"); ok {
		started = st.Format("02.01.2006 15:04")
		if uptime != "" {
			started += " (аптайм " + uptime + ")"
		}
	}
	ag := "нет"
	if gii(srv, "ag_count") > 0 {
		ag = "да"
	}
	infoRows := [][2]string{
		{"Продукт", orDash(gs(srv, "product_line"))},
		{"Выпуск", orDash(gs(srv, "edition"))},
		{"Версия / уровень", fmt.Sprintf("%s (%s %s)", orDash3(gs(srv, "ver")),
			gs(srv, "level"), gs(srv, "cu"))},
		{"Имя сервера", orDash(r.meta.ServerFull)},
		{"Потенциально в AlwaysOn", ag},
		{"Логических процессоров", fint(gii(si, "cpu_count"))},
		{"NUMA-узлов", fint(gii(si, "nodes"))},
		{"Физическая память", phys},
		{"SQL Server использует", inUse},
		{"Запущен", started},
		{"Сопоставление (collation)", orDash(gs(srv, "collation"))},
		{"ОС", orDash(gs(srv, "os_line"))},
	}
	var info strings.Builder
	for _, ir := range infoRows {
		fmt.Fprintf(&info, "<tr><td>%s</td><td>%s</td></tr>", esc(ir[0]), esc(ir[1]))
	}
	var svcRows []string
	for _, s := range servicesOf(si) {
		svcRows = append(svcRows,
			tblCell(esc(gs(s, "servicename")), "")+
				tblCell(esc(gs(s, "startup_type_desc")), "")+
				tblCell(esc(gs(s, "status_desc")), "")+
				tblCell(esc(gs(s, "service_account")), ""))
	}
	tfStr := "нет"
	if len(r.d.TraceFlags) > 0 {
		var tfs []string
		for _, t := range r.d.TraceFlags {
			tfs = append(tfs, "TF "+fint(gfi(t, "TraceFlag")))
		}
		tfStr = strings.Join(tfs, ", ")
	}
	return fmt.Sprintf(`
<section class='card' id='config'><h2>Настройки сервера и окружение</h2>
<div class='grid2'>
<div><h3>Сервер</h3><table class='kv'>%s</table>
<h3>Службы</h3>%s</div>
<div><h3>Ключевые параметры конфигурации</h3>%s
<h3>Глобальные трассировочные флаги</h3><p>%s</p></div>
</div></section>`,
		info.String(),
		tbl([]string{"Служба", "Запуск", "Состояние", "Аккаунт"}, svcRows,
			"Нет доступа к списку служб"),
		tbl([]string{"Параметр", "Текущее", "Рекомендуемое", "Статус"}, rows, ""),
		esc(tfStr))
}

func servicesOf(si map[string]any) []map[string]any {
	if v, ok := si["services"].([]map[string]any); ok {
		return v
	}
	if v, ok := si["services"].([]any); ok {
		var out []map[string]any
		for _, x := range v {
			if m, ok := x.(map[string]any); ok {
				out = append(out, m)
			}
		}
		return out
	}
	return nil
}

func orDash3(s string) string {
	if s == "" {
		return "?"
	}
	return s
}

// ---------- бэкапы/задания ----------

func (r *Report) ops() string {
	var brows []string
	for _, b := range r.d.Backups {
		ago := func(k string) string {
			h, hasH := b[k+"_ago_h"].(float64)
			if _, hasT := gtime(b, k); !hasT {
				return "<span class='t-crit'>нет!</span>"
			}
			if !hasH {
				if t, ok := gtime(b, k); ok {
					return esc(t.Format("02.01.2006 15:04"))
				}
				return "<span class='t-crit'>нет!</span>"
			}
			lim := 168.0
			if k != "last_full" {
				lim = 24.0
			}
			cls := "t-ok"
			if h > lim {
				cls = "t-crit"
			}
			t, _ := gtime(b, k)
			return "<span class='" + cls + "'>" + esc(t.Format("02.01.2006 15:04")) +
				" (" + fint(h) + " ч назад)</span>"
		}
		brows = append(brows,
			tblCell("<b>"+esc(gs(b, "db"))+"</b>", "")+
				tblCell(esc(gs(b, "recovery_model_desc")), "")+
				tblCell(ago("last_full"), "")+
				tblCell(ago("last_diff"), "")+
				tblCell(ago("last_log"), ""))
	}
	var jrows []string
	for i, j := range r.d.JobsFailed {
		if i >= 25 {
			break
		}
		runDT := "—"
		if t, ok := gtime(j, "run_dt"); ok {
			runDT = t.Format("02.01.2006 15:04")
		}
		jrows = append(jrows,
			tblCell(esc(runDT), "")+
				tblCell("<b>"+esc(gs(j, "job"))+"</b>", "")+
				tblCell("<code class='small'>"+esc(gs(j, "message_head"))+"</code>", ""))
	}
	return fmt.Sprintf(`
<section class='card' id='ops'><h2>Резервное копирование и задания SQL Agent</h2>
<h3>Последние резервные копии</h3>
%s
<h3>Неудачные задания (последние 2 дня)</h3>
%s
</section>`,
		tbl([]string{"База", "Модель", "Полная", "Разностная", "Журнала"}, brows, ""),
		tbl([]string{"Когда", "Задание", "Сообщение"}, jrows, "Сбоев заданий не зафиксировано"))
}

func (r *Report) errorLog() string {
	el := r.d.ErrorLog
	if el == nil {
		return ""
	}
	if len(el) == 0 {
		return "<section class='card' id='el'><h2>Журнал ошибок SQL Server</h2>" +
			"<div class='okbox'>Подозрительных записей не найдено.</div></section>"
	}
	var rows []string
	for i, e := range el {
		if i >= 60 {
			break
		}
		dt := "—"
		if t, ok := gtime(e, "date"); ok {
			dt = t.Format("02.01.2006 15:04")
		}
		rows = append(rows,
			tblCell(esc(dt), "")+
				tblCell("<code class='small'>"+esc(gs(e, "text"))+"</code>", ""))
	}
	return fmt.Sprintf("<section class='card' id='el'><h2>Журнал ошибок SQL Server — "+
		"подозрительные записи (%d)</h2>%s<p class='muted'>Полный журнал: "+
		"SSMS → Management → SQL Server Logs.</p></section>", len(el),
		tbl([]string{"Время", "Сообщение"}, rows, ""))
}

// ---------- рекомендации ----------

func (r *Report) recs() string {
	if len(r.findings) == 0 {
		return "<section class='card' id='recs'><h2>Рекомендации</h2>" +
			"<div class='okbox'>Автоматических замечаний не найдено. Сервер выглядит " +
			"настроенным хорошо.</div></section>"
	}
	var cards strings.Builder
	for _, f := range r.findings {
		var ev strings.Builder
		for _, e := range f.Evidence {
			fmt.Fprintf(&ev, "<li>%s</li>", esc(e))
		}
		evHTML := ""
		if ev.Len() > 0 {
			evHTML = "<ul class='ev'>" + ev.String() + "</ul>"
		}
		sql := ""
		if f.SQLT != "" {
			sql = codeBlock(f.SQLT)
		}
		fmt.Fprintf(&cards, `
<div class='rec r-%s' data-sev='%s'>
 <div class='rec-h'><span class='badge b-%s'>%s</span>
   <b>%d. %s</b> <span class='cat'>· %s</span></div>
 %s
 <div class='rec-why'><b>Что это значит:</b> %s</div>
 <div class='rec-act'><b>Как исправить:</b> %s</div>
 %s
</div>`, f.Severity, f.Severity, f.Severity, sevTitles[f.Severity],
			f.Num, esc(f.Title), esc(f.Cat), evHTML, esc(f.Why), esc(f.Action), sql)
	}
	return fmt.Sprintf(`
<section class='card' id='recs'><h2>Рекомендации (%d)</h2>
<div class='filters no-print'>
 <button onclick="filt('all')" class='on'>Все (%d)</button>
 <button onclick="filt('critical')">⛔ Критично (%d)</button>
 <button onclick="filt('warning')">⚠️ Важно (%d)</button>
 <button onclick="filt('info')">💡 Рекомендации (%d)</button>
</div>
<div id='reclist'>%s</div>
<p class='muted'>Все рекомендации вырабатываются автоматически на основе данных DMV.
Перед применением T-SQL проверяйте его на тестовой среде.</p>
</section>`, len(r.findings), len(r.findings), r.health.Crit, r.health.Warn,
		r.health.Info, cards.String())
}

func (r *Report) footer() string {
	return `
<section class='card' id='app' ><h2>Приложение: методика и права</h2>
<div class='grid2'>
<div>
<h3>Источники данных (только чтение)</h3>
<p class='muted'>Инструмент выполняет только SELECT к системным представлениям:</p>
<ul class='muted small'>
 <li>sys.dm_os_wait_stats, sys.dm_os_performance_counters, sys.dm_os_sys_info, sys.dm_os_ring_buffers</li>
 <li>sys.dm_exec_requests / sessions / query_stats, sys.dm_exec_sql_text, sys.dm_exec_query_plan</li>
 <li>sys.dm_db_missing_index_*, sys.dm_db_index_physical_stats, sys.dm_db_stats_properties</li>
 <li>sys.databases, sys.master_files, sys.dm_io_virtual_file_stats, sys.dm_os_volume_stats</li>
 <li>msdb.dbo.backupset, msdb.dbo.sysjobhistory, system_health (deadlocks), sp_readerrorlog</li>
</ul>
</div>
<div>
<h3>Требуемые права</h3>
<ul class='muted small'>
 <li><code>VIEW SERVER STATE</code> — основной объём данных;</li>
 <li><code>VIEW ANY DATABASE</code> + чтение пользовательских баз — индексы/статистика;</li>
 <li>чтение <code>msdb</code> — бэкапы и задания;</li>
 <li><code>securityadmin</code> или права на <code>sp_readerrorlog</code> — журнал ошибок.</li>
</ul>
<p class='muted small'>Недостающие права не ломают отчёт — недоступные проверки отмечаются
в сводке. Инструмент ничего не изменяет на сервере.</p>
</div>
</div>
</section>
<footer>Сформировано MS SQL Monitor (Go) · ` + esc(r.meta.GeneratedStr) +
		" · Данные: DMV Microsoft SQL Server</footer>"
}

func (r *Report) body() string {
	return "<body>\n" + r.header() + "\n<main>\n" +
		r.summary() + r.timeline() + r.peaksSection() + r.waits() + r.queries() +
		r.sessions() + r.longQueries() + r.deadlocks() + r.indexes() +
		r.databases() + r.disks() + r.tempdbSection() + r.config() + r.audit() + r.ops() +
		r.errorLog() + r.recs() + r.footer() +
		"\n</main>\n<script>\n" + reportJS + "\n</script>\n</body></html>"
}

// BuildReport — точка входа (сохраняет отчёт в файл и возвращает HTML).
func BuildReport(d *DeepData, samples []Sample, blocking []BlockingEvent,
	longq []LongQuery, meta ReportMeta) (string, Health, []Finding, []PeakWindow) {
	peaks := findSlowdownWindows(samples, blocking, longq)
	findings := BuildFindings(d, samples, blocking, longq)
	health := HealthScore(findings)
	rep := NewReport(d, findings, health, peaks, samples, blocking, longq, meta)
	return rep.Build(), health, findings, peaks
}
