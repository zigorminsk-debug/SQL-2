# -*- coding: utf-8 -*-
"""
report.py — построение самодостаточного HTML-отчёта (inline CSS + SVG, без CDN).
"""
import datetime
import html
import math

import analysis

PALETTE = ["#1a5fb4", "#e8710a", "#188038", "#c62828", "#7b1fa2",
           "#00838f", "#5d4037", "#546e7a", "#f9a825", "#455a64"]

CAT_COLORS = {"cpu": "#1a5fb4", "lock": "#c62828", "latch": "#7b1fa2",
              "io": "#e8710a", "network": "#00838f", "memory": "#f9a825",
              "log": "#5d4037", "hadr": "#188038", "other": "#90a4ae"}


def esc(s):
    return html.escape("" if s is None else str(s), quote=True)


def fnum(v, nd=1):
    try:
        if v is None:
            return "—"
        f = float(v)
        if abs(f) >= 1000:
            s = ("{:,.%df}" % nd).format(f)
            return s.replace(",", " ").replace(".", ",")
        return ("%.*f" % (nd, f)).replace(".", ",")
    except (TypeError, ValueError):
        return str(v)


def fint(v):
    try:
        if v is None:
            return "—"
        return "{:,}".format(int(v)).replace(",", " ")
    except (TypeError, ValueError):
        return str(v)


def fdt(v):
    try:
        if isinstance(v, datetime.datetime):
            return v.strftime("%d.%m.%Y %H:%M")
        return str(v or "—")
    except Exception:
        return str(v)


def fhms(sec):
    try:
        sec = float(sec or 0)
        if sec >= 3600:
            return "%d:%02d:%02d" % (sec // 3600, (sec % 3600) // 60, sec % 60)
        if sec >= 60:
            return "%d мин %02d с" % (sec // 60, sec % 60)
        return "%.1f с" % sec
    except Exception:
        return "—"


def fmt_ms_ru(ms):
    if ms is None:
        return "—"
    try:
        ms = float(ms)
    except (TypeError, ValueError):
        return "—"
    if ms >= 3600000:
        return fnum(ms / 3600000, 1) + " ч"
    if ms >= 60000:
        return fnum(ms / 60000, 1) + " мин"
    return fnum(ms / 1000, 0) + " с"


# =====================================================================
# SVG-ГРАФИКИ
# =====================================================================
def _nice_step(raw):
    if raw <= 0:
        return 1
    mag = 10 ** math.floor(math.log10(raw))
    for m in (1, 2, 2.5, 5, 10):
        if raw <= m * mag:
            return m * mag
    return 10 * mag


def svg_line(series, t0, t1, h=250, y_max=None, y_min=0, unit="", regions=None,
             y_start=None):
    """series: [{name,color,points:[(ts,val|None)],dash}] по общей шкале времени."""
    regions = regions or []
    W, H = 1040, h
    L, R, T, B = 62, 14, 16, 30
    iw, ih = W - L - R, H - T - B
    t0, t1 = float(t0), float(t1)
    if t1 <= t0:
        t1 = t0 + 1
    vals = []
    for s in series:
        vals += [p[1] for p in s["points"] if p[1] is not None]
    if not vals:
        return "<div class='nochart'>Недостаточно данных для графика</div>"
    vmax = y_max if y_max is not None else max(vals)
    vmin = min(y_min, min(vals))
    if y_start is not None:
        vmax = max(vmax, y_start)
    if vmax == vmin:
        vmax = vmin + 1
    vmax *= 1.08
    step = _nice_step((vmax - vmin) / 5)

    def X(ts):
        return L + (ts - t0) / (t1 - t0) * iw

    def Y(v):
        return T + ih - (v - vmin) / (vmax - vmin) * ih

    parts = ["<svg viewBox='0 0 %d %d' class='chart' preserveAspectRatio='xMidYMid meet'>"
             % (W, H)]
    # сетка
    v = vmin
    grid = []
    while v <= vmax + 1e-9:
        y = Y(v)
        grid.append(v)
        parts.append("<line x1='%d' y1='%.1f' x2='%d' y2='%.1f' class='grid'/>"
                     % (L, y, W - R, y))
        parts.append("<text x='%d' y='%.1f' class='tick'>%s</text>"
                     % (L - 6, y + 4, fnum(v, 0 if step >= 1 else 1)))
        v += step
    # области замедления
    for (ra, rb) in regions:
        x1, x2 = X(max(ra, t0)), X(min(rb, t1))
        if x2 - x1 > 1:
            parts.append("<rect x='%.1f' y='%d' width='%.1f' height='%d' "
                         "fill='#e53935' fill-opacity='0.09'/>" % (x1, T, x2 - x1, ih))
    # оси
    parts.append("<line x1='%d' y1='%d' x2='%d' y2='%d' class='axis'/>"
                 % (L, T, L, T + ih))
    parts.append("<line x1='%d' y1='%.1f' x2='%d' y2='%.1f' class='axis'/>"
                 % (L, T + ih, W - R, T + ih))
    # подписи X
    n_ticks = min(9, max(4, int(iw // 120)))
    span = t1 - t0
    for i in range(n_ticks + 1):
        ts = t0 + span * i / n_ticks
        d = datetime.datetime.fromtimestamp(ts)
        lbl = d.strftime("%H:%M") if span <= 86400 * 1.5 else d.strftime("%d.%m %H:%M")
        parts.append("<text x='%.1f' y='%d' class='tick' text-anchor='middle'>%s</text>"
                     % (X(ts), T + ih + 18, lbl))
    if unit:
        parts.append("<text x='%d' y='%d' class='unit'>%s</text>" % (L - 6, T - 5, unit))
    # серии
    for s in series:
        d_attr = " stroke-dasharray='6 4'" if s.get("dash") else ""
        parts.append("<path fill='none' stroke='%s' stroke-width='2'%s "
                     "d='" % (s["color"], d_attr))
        started = False
        for (ts, v) in s["points"]:
            if v is None:
                started = False
                continue
            cmd = "M" if not started else "L"
            parts.append("%s%.1f %.1f " % (cmd, X(ts), Y(v)))
            started = True
        parts.append("'/>")
    parts.append("</svg>")
    return "".join(parts)


def svg_donut(parts_data, total=None, center_label=""):
    """parts_data: [(label, value, color)]"""
    total = total or sum(v for _l, v, _c in parts_data) or 1
    cx, cy, r = 90, 90, 62
    sw = 30
    a0 = -90.0
    arcs = []
    for (label, v, color) in parts_data:
        frac = v / total if total else 0
        if frac <= 0:
            continue
        a1 = a0 + frac * 360
        large = 1 if (a1 - a0) > 180 else 0
        x0 = cx + r * math.cos(math.radians(a0))
        y0 = cy + r * math.sin(math.radians(a0))
        x1 = cx + r * math.cos(math.radians(a1))
        y1 = cy + r * math.sin(math.radians(a1))
        arcs.append(("<path d='M %.2f %.2f A %.1f %.1f 0 %d 1 %.2f %.2f' "
                     "fill='none' stroke='%s' stroke-width='%d'><title>%s: %s (%.1f%%)"
                     "</title></path>")
                    % (x0, y0, r, r, large, x1, y1, color, sw,
                       esc(label), fmt_ms_ru(v), frac * 100))
        a0 = a1
    return ("<svg viewBox='0 0 180 180' class='donut'>%s"
            "<text x='90' y='88' text-anchor='middle' class='donut-c'>%s</text>"
            "<text x='90' y='106' text-anchor='middle' class='donut-s'>%s</text></svg>"
            % ("".join(arcs), esc(center_label), "итого: " + fmt_ms_ru(total)))


def svg_gauge(score):
    frac = max(0, min(1, score / 100.0))
    cx, cy, r = 100, 96, 74
    sw = 16
    color = "#2e7d32" if score >= 75 else ("#f9a825" if score >= 55 else "#c62828")
    a0 = 180
    a1 = 180 + frac * 180
    large = 0
    x0 = cx + r * math.cos(math.radians(a0))
    y0 = cy + r * math.sin(math.radians(a0))
    x1 = cx + r * math.cos(math.radians(a1))
    y1 = cy + r * math.sin(math.radians(a1))
    return ("<svg viewBox='0 0 200 118' class='gauge'>"
            "<path d='M %.1f %.1f A %.1f %.1f 0 0 1 %.1f %.1f' fill='none' "
            "stroke='#e0e6ee' stroke-width='%d'/>"
            "<path d='M %.1f %.1f A %.1f %.1f 0 %d 1 %.1f %.1f' fill='none' "
            "stroke='%s' stroke-width='%d' stroke-linecap='round'/>"
            "<text x='100' y='88' text-anchor='middle' class='gauge-v' fill='%s'>%d</text>"
            "<text x='100' y='106' text-anchor='middle' class='gauge-s'>из 100</text></svg>"
            % (cx - r, cy, r, r, cx + r, cy, sw,
               x0, y0, r, r, large, x1, y1, color, sw, color, score))


def html_hbars(items, unit="", fmt=None, color_fn=None):
    """items: [(label, value, title?)] -> HTML-бары."""
    if not items:
        return "<div class='nodata'>Нет данных</div>"
    mx = max(v for _l, v, *_ in items) or 1
    fmt = fmt or (lambda v: fnum(v, 1))
    rows = []
    for i, item in enumerate(items):
        label, v = item[0], item[1]
        tip = item[2] if len(item) > 2 else None
        pct = max(1.5, 100.0 * (v or 0) / mx)
        color = color_fn(i, item) if color_fn else PALETTE[i % len(PALETTE)]
        rows.append(
            "<div class='hbar-row'><div class='hbar-l' title='%s'>%s</div>"
            "<div class='hbar-t'><div class='hbar' style='width:%.1f%%;background:%s'>"
            "</div></div><div class='hbar-v'>%s</div></div>"
            % (esc(tip or label), esc(label), pct, color, fmt(v)))
    return ("<div class='hbars'>%s<div class='hbar-legend'>%s</div></div>"
            % ("".join(rows), ("<div class='muted'>Единицы: %s</div>" % esc(unit)) if unit else ""))


# =====================================================================
# ОТЧЁТ
# =====================================================================
class Report(object):
    def __init__(self, data, findings, health, peaks, samples,
                 blocking_events, long_queries, meta):
        self.d = data
        self.findings = findings or []
        self.health = health or {"score": 0, "grade": "—", "crit": 0, "warn": 0, "info": 0}
        self.peaks = peaks or []
        self.samples = samples or []
        self.blocking = blocking_events or []
        self.longq = long_queries or []
        self.meta = meta or {}

    # ------------------------------------------------------------ utils
    def table(self, headers, rows, cls="", empty="Нет данных"):
        if not rows:
            return "<div class='nodata'>%s</div>" % esc(empty)
        h = "".join("<th>%s</th>" % esc(x) for x in headers)
        b = "".join("<tr>%s</tr>" % r for r in rows)
        return ("<table class='tbl %s'><thead><tr>%s</tr></thead><tbody>%s</tbody></table>"
                % (cls, h, b))

    def c(self, text, cls=""):
        return "<td class='%s'>%s</td>" % (cls, text)

    @staticmethod
    def code(text):
        return "<pre class='sql'>%s</pre>" % esc(text)

    def tspan(self, ts):
        return datetime.datetime.fromtimestamp(ts).strftime("%H:%M:%S")

    # ------------------------------------------------------------ сборка
    def build(self):
        title = self.meta.get("server_display") or "MS SQL Server"
        return _HEAD_CSS.replace("%TITLE%", esc(title)) + self._body()

    # ------------------------------------------------------------ шапка
    def _header(self):
        m = self.meta
        per = ""
        if self.samples:
            per = "%s — %s" % (
                datetime.datetime.fromtimestamp(self.samples[0]["ts"]).strftime("%d.%m.%Y %H:%M"),
                datetime.datetime.fromtimestamp(self.samples[-1]["ts"]).strftime("%d.%m %H:%M"))
        else:
            per = "нет накопленных данных (режим снимка)"
        h = self.health
        return """
<div class='hero card'>
  <div class='hero-grid'>
    <div>
      <div class='brand'>MS SQL Monitor — отчёт о диагностике</div>
      <div class='h1'>%(server)s</div>
      <table class='kv'>
        <tr><td>Версия:</td><td>%(product)s</td></tr>
        <tr><td>Сервер / экземпляр:</td><td>%(srvname)s</td></tr>
        <tr><td>Отчёт сформирован:</td><td>%(gen)s</td></tr>
        <tr><td>Период мониторинга:</td><td>%(period)s (%(cnt)s точек, интервал %(interval)s с)</td></tr>
        <tr><td>Драйвер:</td><td>%(driver)s</td></tr>
      </table>
    </div>
    <div class='hero-score'>
      %(gauge)s
      <div class='grade'>%(grade)s</div>
      <div class='score-cards'>
        <span class='sc crit'>⛔ Критично: <b>%(crit)d</b></span>
        <span class='sc warn'>⚠️ Важно: <b>%(warn)d</b></span>
        <span class='sc info'>💡 Рекомендаций: <b>%(info)d</b></span>
      </div>
    </div>
  </div>
</div>
<nav class='nav no-print' id='top'>
 %(nav)s
</nav>""" % {
            "server": esc(m.get("server_display") or "?"),
            "product": esc((self.d.get("server") or {}).get("product_line") or "—"),
            "srvname": esc(m.get("server_full") or "?"),
            "gen": esc(m.get("generated_str") or "—"),
            "period": esc(per),
            "cnt": fint(len(self.samples)),
            "interval": fint(m.get("interval") or 0),
            "driver": esc(m.get("driver") or "—"),
            "gauge": svg_gauge(h.get("score", 0)),
            "grade": esc(h.get("grade", "—")),
            "crit": h.get("crit", 0), "warn": h.get("warn", 0), "info": h.get("info", 0),
            "nav": "".join("<a href='#%s'>%s</a>" % (a, esc(t)) for a, t in _NAV),
        }

    # ------------------------------------------------------------ сводка
    def _summary(self):
        top = self.findings[:6]
        items = "".join(
            "<li><a href='#recs' class='sevlink s-%s'>%s %s</a></li>"
            % (f["severity"], analysis.SEV_ICONS[f["severity"]], esc(f["title"]))
            for f in top)
        return """
<section class='card' id='summary'><h2>Сводка</h2>
<p class='lead'>Оценка состояния сервера — <b>%(score)d/100</b> (%(grade)s).
Обнаружено проблем: <b class='t-crit'>%(crit)d критичных</b>, <b class='t-warn'>%(warn)d важных</b>,
%(info)d рекомендаций. Ниже — самые значимые; полный список в разделе «Рекомендации».</p>
<ul class='toplist'>%(items)s</ul>
%(errs)s
</section>""" % {
            "score": self.health.get("score", 0), "grade": esc(self.health.get("grade", "")),
            "crit": self.health.get("crit", 0), "warn": self.health.get("warn", 0),
            "info": self.health.get("info", 0), "items": items or "<li>Замечаний не найдено</li>",
            "errs": self._check_errors(),
        }

    def _check_errors(self):
        errs = self.d.get("errors") or {}
        if not errs:
            return ""
        rows = "".join("<li>%s — <span class='muted'>%s</span></li>" % (esc(k), esc(v))
                       for k, v in errs.items())
        return ("<div class='warnbox'><b>Часть проверок недоступна</b> (обычно из-за "
                "недостатка прав; полный отчёт требует VIEW SERVER STATE):<ul>%s</ul></div>" % rows)

    # ------------------------------------------------------------ графики
    def _timeline(self):
        if len(self.samples) < 2:
            return ("<section class='card' id='timeline'><h2>Графики нагрузки "
                    "(суточный мониторинг)</h2><div class='nodata'>Данных мониторинга пока "
                    "недостаточно. Запустите мониторинг во вкладке «Мониторинг» — и на "
                    "следующем отчёте здесь появятся графики загрузки за сутки.</div>"
                    "</section>")
        S = self.samples
        t0, t1 = S[0]["ts"], S[-1]["ts"]
        regions = [(w["t0"], w["t1"]) for w in self.peaks]

        def pts(key):
            return [(s["ts"], s.get(key)) for s in S]

        cpu = svg_line(
            [{"name": "SQL CPU, %", "color": PALETTE[0], "points": pts("sql_cpu")},
             {"name": "Общий CPU, %", "color": PALETTE[1], "points": pts("sys_cpu"),
              "dash": True}],
            t0, t1, y_max=100, unit="%", regions=regions)
        batch = svg_line([{"name": "Batch req/sec", "color": PALETTE[2],
                           "points": pts("batch_req")}], t0, t1, unit="запр/с",
                         regions=regions)
        ple = svg_line([{"name": "PLE", "color": PALETTE[3], "points": pts("ple")}],
                       t0, t1, y_min=0, unit="с", regions=regions)
        mem = svg_line(
            [{"name": "Занято SQL, МБ", "color": PALETTE[0], "points": pts("mem_total_mb")},
             {"name": "Цель SQL, МБ", "color": PALETTE[4], "points": pts("mem_target_mb"),
              "dash": True}],
            t0, t1, y_min=0, unit="МБ", regions=regions)
        io = svg_line(
            [{"name": "Чтение, мс", "color": PALETTE[1], "points": pts("io_rd_lat")},
             {"name": "Запись, мс", "color": PALETTE[5], "points": pts("io_wr_lat")}],
            t0, t1, y_min=0, unit="мс", regions=regions)
        tmp = svg_line([{"name": "tempdb занято, МБ", "color": PALETTE[6],
                         "points": pts("tempdb_used_mb")}], t0, t1, y_min=0, unit="МБ",
                       regions=regions)
        blocked_items = [(self.tspan(s["ts"]) + (" ⛔" if (s.get("blocked") or 0) > 0 else ""),
                          s.get("blocked") or 0) for s in S if (s.get("blocked") or 0) > 0]
        blocked_chart = (html_hbars(blocked_items, "эпизодов блокировки в минуту", fmt=fint)
                         if blocked_items else "<div class='nodata'>Блокировок за период не зафиксировано</div>")

        def legend_html():
            return ("<div class='legend no-print'>%(regs)s</div>" % {
                "regs": ("<span class='lg'><span class='sw region'></span> окно замедления</span>"
                         if regions else "")})

        stats = self._timeline_stats()
        return """
<section class='card' id='timeline'><h2>Графики нагрузки (суточный мониторинг)</h2>
<p class='muted'>Период: %(t0)s — %(t1)s, точек: %(n)s. %(legend)s</p>
%(stats)s
<div class='chart-block'><h3>Загрузка процессора</h3>%(cpu)s
 <div class='legend'><span class='lg'><span class='sw' style='background:#1a5fb4'></span>SQL Server, %%</span>
 <span class='lg'><span class='sw' style='background:#e8710a'></span>система в целом, %%</span></div></div>
<div class='chart-block'><h3>Скорость обработки — запросов в секунду (Batch Requests/sec)</h3>%(batch)s</div>
<div class='chart-block'><h3>Время жизни страниц в памяти (Page Life Expectory)</h3>%(ple)s
 <p class='muted'>300 с — эвристика, не норматив; сравните с эталоном системы и физическими чтениями.</p></div>
<div class='chart-block'><h3>Память SQL Server</h3>%(mem)s</div>
<div class='chart-block'><h3>Задержки ввода-вывода (мс на операцию)</h3>%(io)s
 <p class='muted'>Норма: до 10–20 мс на чтение и запись. Выше 30 мс — диск не справляется.</p></div>
<div class='chart-block'><h3>tempdb — занято, МБ</h3>%(tmp)s</div>
<div class='chart-block'><h3>Блокировки по времени</h3>%(blocked)s</div>
</section>""" % {
            "t0": esc(datetime.datetime.fromtimestamp(t0).strftime("%d.%m.%Y %H:%M")),
            "t1": esc(datetime.datetime.fromtimestamp(t1).strftime("%d.%m %H:%M")),
            "n": fint(len(S)), "legend": legend_html(), "stats": stats,
            "cpu": cpu, "batch": batch, "ple": ple, "mem": mem, "io": io,
            "tmp": tmp, "blocked": blocked_chart}

    def _timeline_stats(self):
        S = self.samples
        def agg(key, fn):
            vals = [s.get(key) for s in S if s.get(key) is not None]
            if not vals:
                return "—"
            return fn(vals)
        cells = [
            ("CPU SQL (средн./макс.)", "%s%% / %s%%" % (fnum(agg("sql_cpu", lambda v: sum(v) / len(v)), 0),
                                                        fnum(agg("sql_cpu", max), 0))),
            ("Запросов/с (средн./макс.)", "%s / %s" % (fnum(agg("batch_req", lambda v: sum(v) / len(v)), 1),
                                                       fnum(agg("batch_req", max), 1))),
            ("PLE (мин.)", fnum(agg("ple", min), 0)),
            ("Буферный кэш hit, %", fnum(agg("bchr", lambda v: sum(v) / len(v)), 1)),
            ("Задержка чтения, мс (макс.)", fnum(agg("io_rd_lat", max), 1)),
            ("Задержка записи, мс (макс.)", fnum(agg("io_wr_lat", max), 1)),
            ("Тиков с блокировками", fint(len([s for s in S if (s.get("blocked") or 0) > 0]))),
            ("Макс. ожидание блокировки", fhms(agg("blocked_max_wait", max))),
        ]
        rows = "".join("<div class='stat'><div class='stat-v'>%s</div><div class='stat-k'>%s</div></div>"
                       % (v, esc(k)) for k, v in cells)
        return "<div class='stats'>%s</div>" % rows

    # ------------------------------------------------------------ замедления
    def _peaks(self):
        if not self.peaks:
            return ("<section class='card' id='peaks'><h2>Анализ замедлений</h2>"
                    "<div class='nodata'>Явных окон замедления на таймлайне не выделено "
                    "(или данных мониторинга недостаточно).</div></section>")
        blocks = []
        for i, w in enumerate(self.peaks, 1):
            wait_items = [(wt, ms * 1000.0, "%s: %s" % (wt, fmt_ms_ru(ms * 1000.0)))
                          for wt, ms in w["waits"]]
            bl_rows = []
            for e in w["blocked"][:12]:
                bl_rows.append("<tr>%s%s%s%s%s%s</tr>" % (
                    self.c(self.tspan(e["ts"])), self.c(fint(e.get("session_id"))),
                    self.c("← " + str(e.get("blocking_session_id"))),
                    self.c(esc(e.get("wait_type") or "")),
                    self.c(fnum(e.get("wait_time_s"), 0) + " с"),
                    self.c("<code class='small'>%s</code>" % esc((e.get("text_head") or "")[:120]))))
            lq_rows = []
            for q in sorted(w["long"], key=lambda x: -(x.get("elapsed_s") or 0))[:8]:
                lq_rows.append("<tr>%s%s%s%s%s%s</tr>" % (
                    self.c(self.tspan(q["ts"])), self.c(fint(q.get("session_id"))),
                    self.c(fhms(q.get("elapsed_s"))), self.c(fhms(q.get("cpu_s"))),
                    self.c(fint(q.get("logical_reads"))),
                    self.c("<code class='small'>%s</code>" % esc((q.get("text_head") or "")[:120]))))
            reasons = "".join("<li>%s</li>" % r for r in w["reasons"])
            blocks.append("""
<div class='peak'>
 <h3>Окно №%(i)d: %(t0)s — %(t1)s (%(dur)d мин, %(n)d точек)</h3>
 <div class='peak-stats'>CPU: <b>%(cpu)s%%</b> (макс. %(cpum)s%%) · запросов/с: <b>%(batch)s</b> ·
   блокировок: <b class='t-warn'>%(bl)d</b> · PLE мин.: <b>%(ple)s</b> с</div>
 <div class='reasons'><b>Причины замедления:</b><ul>%(reasons)s</ul></div>
 %(waits)s
 %(bl_tbl)s
 %(lq_tbl)s
</div>""" % {
                "i": i,
                "t0": esc(datetime.datetime.fromtimestamp(w["t0"]).strftime("%d.%m %H:%M")),
                "t1": esc(datetime.datetime.fromtimestamp(w["t1"]).strftime("%H:%M")),
                "dur": int((w["t1"] - w["t0"]) // 60), "n": w["n"],
                "cpu": fnum(w.get("cpu_avg"), 0), "cpum": fnum(w.get("cpu_max"), 0),
                "batch": fnum(w.get("batch_avg"), 1), "bl": len(w["blocked"]),
                "ple": fnum(w.get("ple_min") if w.get("ple_min") != 10 ** 9 else None, 0),
                "reasons": reasons,
                "waits": ("<h4>Ожидания в этом окне</h4>" +
                          html_hbars(wait_items, "время ожидания", fmt=fmt_ms_ru)
                          if wait_items else ""),
                "bl_tbl": ("<h4>Блокировки в этом окне</h4>" +
                           self.table(["Время", "Сессия", "Блокирует", "Тип ожидания",
                                       "Ожидание", "Запрос"], bl_rows) if bl_rows else ""),
                "lq_tbl": ("<h4>Долгие запросы в этом окне</h4>" +
                           self.table(["Время", "Сессия", "Длительность", "CPU", "Чтений",
                                       "Запрос"], lq_rows) if lq_rows else ""),
            })
        return ("<section class='card' id='peaks'><h2>Анализ замедлений — разбор причин</h2>"
                "<p class='muted'>Автоматически выделены интервалы повышенной нагрузки и "
                "проблем. Для каждого окна приведены доминирующие ожидания, блокировки и "
                "долгие запросы именно этого периода.</p>%s</section>"
                % "".join(blocks))

    # ------------------------------------------------------------ ожидания
    def _waits(self):
        ws = self.d.get("wait_stats") or {}
        rows = ws.get("rows") or []
        if not rows:
            return ("<section class='card' id='waits'><h2>Ожидания (wait stats)</h2>"
                    "<div class='nodata'>Данные ожиданий недоступны.</div></section>")
        total = ws.get("total_ms") or 1
        signal = ws.get("signal_ms") or 0
        cats = analysis.wait_category_totals(rows)
        donut = [(analysis.WAIT_CATEGORIES[c]["title"], v, CAT_COLORS.get(c, "#999"))
                 for c, v in sorted(cats.items(), key=lambda kv: -kv[1]) if v > 0]
        trows = []
        for r in rows[:25]:
            cat, title, desc, _acts = analysis.wait_info(r["wait_type"])
            share = 100.0 * r["wait_ms"] / total
            trows.append("<tr>%s%s%s%s%s%s</tr>" % (
                self.c("<span class='sev-dot' style='background:%s'></span>%s"
                       % (CAT_COLORS.get(cat, "#999"), esc(r["wait_type"]))),
                self.c(esc(title), "nowrap"),
                self.c(fnum(share, 1) + "%"),
                self.c(fmt_ms_ru(r["wait_ms"])),
                self.c(fmt_ms_ru(r.get("max_ms"))),
                self.c(fint(r.get("tasks")))))
        cat_rows = "".join(
            "<div class='cat-card'><div class='cat-h'><span class='sev-dot' "
            "style='background:%s'></span><b>%s</b> — %s</div><div class='muted'>%s</div>"
            "<ul>%s</ul></div>" % (
                CAT_COLORS.get(c, "#999"), esc(analysis.WAIT_CATEGORIES[c]["title"]),
                fmt_ms_ru(v), esc(analysis.WAIT_CATEGORIES[c]["desc"]),
                "".join("<li>%s</li>" % esc(a) for a in
                        analysis.WAIT_CATEGORIES[c]["actions"]))
            for c, v in sorted(cats.items(), key=lambda kv: -kv[1]) if v > total * 0.005)
        return """
<section class='card' id='waits'><h2>Ожидания (wait stats) — на что тратится время</h2>
<div class='waits-grid'>
  <div class='waits-left'>%(donut)s
    <div class='legend'>%(legend)s</div>
    <p class='muted'>Signal wait: <b>%(sig)s%%</b> (доля ожидания процессора; &gt;20%% — нехватка CPU)</p>
  </div>
  <div>%(tbl)s</div>
</div>
<h3>Категории ожиданий и что с ними делать</h3>
<div class='cats'>%(cats)s</div>
</section>""" % {
            "donut": svg_donut(donut, center_label="ожидания"),
            "legend": "".join("<span class='lg'><span class='sw' style='background:%s'>"
                              "</span>%s</span>" % (c, esc(l)) for l, _v, c in donut),
            "sig": fnum(100.0 * signal / total, 1),
            "tbl": self.table(["Тип", "Что означает", "Доля", "Всего", "Макс.",
                               "Ожиданий (раз)"], trows),
            "cats": cat_rows}

    # ------------------------------------------------------------ запросы
    def _queries(self):
        qs = self.d.get("qstats") or {}
        out = ["<section class='card' id='queries'><h2>Топ запросов "
               "(скорость обработки)</h2>"]
        if not qs:
            out.append("<div class='nodata'>Статистика запросов недоступна.</div></section>")
            return "".join(out)

        def qtable(lst, title, note=""):
            rows = []
            for r in lst[:15]:
                rows.append("<tr>%s%s%s%s%s%s%s</tr>" % (
                    self.c(fint(r.get("execution_count"))),
                    self.c(fnum(r.get("avg_elapsed_ms"), 0) + " мс"),
                    self.c(fnum(r.get("avg_worker_ms"), 0) + " мс"),
                    self.c(fint(r.get("total_logical_reads"))),
                    self.c(esc(r.get("db_name") or "")),
                    self.c(esc(r.get("object_name") or "—")),
                    self.c("<details><summary>показать</summary><code class='small'>%s</code>"
                           "</details>" % esc((r.get("sql_text") or "")[:1500]))))
            return ("<h3>%s</h3><p class='muted'>%s</p>%s" % (
                esc(title), note,
                self.table(["Выполнений", "Ср. длит.", "Ср. CPU", "Чтений (всего)",
                            "База", "Объект", "Текст"], rows)))

        out.append(qtable(qs.get("by_cpu") or [], "Самые дорогие по CPU",
                          "Суммарное время процессора с момента запуска сервера."))
        out.append(qtable(qs.get("by_avg") or [], "Самые долгие в среднем",
                          "Средняя длительность выполнения (не менее 10 выполнений)."))
        out.append(qtable(qs.get("by_reads") or [], "Самые «читающие»",
                          "Суммарные логические чтения страниц."))
        dup = qs.get("dup_plans") or []
        if dup:
            drows = "".join("<tr>%s%s%s</tr>" % (
                self.c(fint(d.get("plan_count"))), self.c(fint(d.get("execs"))),
                self.c("<code class='small'>%s</code>" % esc((d.get("sql_text") or "")[:300])))
                for d in dup)
            out.append("<h3>Один запрос — много планов (возможен parameter sniffing)</h3>%s"
                       % self.table(["Планов", "Выполнений", "Текст"], [drows]))
        out.append("</section>")
        return "".join(out)

    def _requests_live(self):
        lr = self.d.get("requests_live") or {}
        reqs = (lr.get("requests") or [])
        blockers = (lr.get("blockers") or [])
        rows = []
        for r in sorted(reqs, key=lambda x: -(x.get("elapsed_s") or 0))[:20]:
            bl = r.get("blocking_session_id")
            rows.append("<tr>%s%s%s%s%s%s%s%s</tr>" % (
                self.c(fint(r.get("session_id"))),
                self.c(esc(r.get("status") or "")),
                self.c(fhms(r.get("elapsed_s"))),
                self.c(fhms(r.get("cpu_s"))),
                self.c("<span class='t-warn'>← %s</span>" % fint(bl) if bl else "—"),
                self.c(esc(r.get("wait_type") or "")),
                self.c(esc(r.get("login_name") or "")),
                self.c("<details><summary>%s</summary><code class='small'>%s</code></details>"
                       % (esc(r.get("command") or ""),
                          esc((r.get("sql_text") or "")[:1200])))))
        btbl = []
        for b in blockers:
            btbl.append("<tr>%s%s%s%s%s%s</tr>" % (
                self.c(fint(b.get("session_id"))),
                self.c(esc(b.get("status") or "")),
                self.c(fint(b.get("tran_count"))),
                self.c(esc(b.get("login_name") or "")),
                self.c(esc(b.get("program_name") or "")),
                self.c("<code class='small'>%s</code>" % esc((b.get("sql_text") or "")[:300]))))
        blocking_section = ""
        if self.blocking:
            brows = []
            seen = set()
            for e in self.blocking:
                key = (e.get("session_id"), e.get("blocking_session_id"),
                       e.get("text_head"))
                if key in seen:
                    continue
                seen.add(key)
                brows.append("<tr>%s%s%s%s%s%s</tr>" % (
                    self.c(datetime.datetime.fromtimestamp(e["ts"]).strftime("%d.%m %H:%M")),
                    self.c(fint(e.get("session_id"))),
                    self.c("<span class='t-warn'>← %s</span>" % fint(e.get("blocking_session_id"))),
                    self.c(esc(e.get("wait_type") or "")),
                    self.c(fnum(e.get("wait_time_s"), 0) + " с"),
                    self.c("<details><summary>%s</summary><code class='small'>%s</code>"
                           "</details>" % (esc(e.get("command") or ""),
                                           esc((e.get("text_head") or "")[:1000])))))
            blocking_section = ("<h3>История блокировок за период мониторинга "
                                "(%d эпизодов)</h3>%s" % (
                                    len(self.blocking),
                                    self.table(["Время", "Сессия", "Блокирует", "Тип",
                                                "Ожидание", "Запрос"], brows)))
        return """
<section class='card' id='sessions'><h2>Активные сессии и блокировки</h2>
<h3>Запросы на момент диагностики</h3>
%(req)s
%(bl)s
%(hist)s
</section>""" % {
            "req": self.table(["Сессия", "Статус", "Длится", "CPU", "Блокир.", "Ожидание",
                               "Логин", "Запрос"], rows,
                              empty="Активных пользовательских запросов нет"),
            "bl": ("<h3>Сессии-«виновники» блокировок (head blockers)</h3>" +
                   self.table(["Сессия", "Статус", "Транзакций", "Логин", "Приложение",
                               "Последний запрос"], btbl,
                               empty="Сейчас активных блокировщиков нет") if blockers
                   else "<p class='muted'>Активных блокировщиков на момент снимка нет.</p>"),
            "hist": blocking_section}

    def _long_queries(self):
        if not self.longq:
            return ""
        rows = []
        for q in sorted(self.longq, key=lambda x: -x.get("elapsed_s", 0))[:40]:
            rows.append("<tr>%s%s%s%s%s%s%s</tr>" % (
                self.c(datetime.datetime.fromtimestamp(q["ts"]).strftime("%d.%m %H:%M:%S")),
                self.c(fint(q.get("session_id"))),
                self.c(fhms(q.get("elapsed_s"))),
                self.c(fhms(q.get("cpu_s"))),
                self.c(fint(q.get("logical_reads"))),
                self.c(esc(q.get("db") or "")),
                self.c("<details><summary>%s</summary><code class='small'>%s</code></details>"
                       % (esc(q.get("command") or ""), esc((q.get("text_head") or "")[:1200])))))
        return ("<section class='card' id='longq'><h2>Долгие запросы за период "
                "мониторинга</h2>%s</section>"
                % self.table(["Время", "Сессия", "Длительность", "CPU", "Чтений", "База",
                              "Запрос"], rows))

    def _deadlocks(self):
        dls = self.d.get("deadlocks") or []
        if not dls:
            return ("<section class='card' id='deadlocks'><h2>Взаимоблокировки "
                    "(deadlock)</h2><div class='nodata'>За последние 48 ч "
                    "взаимоблокировок не зафиксировано.</div></section>")
        blocks = []
        for i, d in enumerate(dls, 1):
            prows = "".join(
                "<tr>%s%s%s%s%s%s%s</tr>" % (
                    self.c("⛔ жертва" if p.get("id") == d.get("victim") else ""),
                    self.c(fint(p.get("spid"))),
                    self.c(esc(p.get("hostname") or "")),
                    self.c(esc(p.get("loginname") or "")),
                    self.c(esc(p.get("isolationlevel") or "")),
                    self.c(esc(p.get("waitresource") or "")),
                    self.c("<code class='small'>%s</code>" % esc((p.get("inputbuf") or "")[:500])))
                for p in d.get("processes") or [])
            blocks.append("""
<div class='peak'><h3>Взаимоблокировка №%(i)d — %(time)s</h3>
<p class='muted'>Объекты: %(objs)s</p>
%(tbl)s</div>""" % {"i": i, "time": esc(fdt(d.get("time"))),
                    "objs": esc(", ".join(d.get("objects") or []) or "—"),
                    "tbl": self.table(["", "SPID", "Хост", "Логин", "Изоляция",
                                       "Ресурс", "Запрос"], [prows])})
        return ("<section class='card' id='deadlocks'><h2>Взаимоблокировки (deadlock) — "
                "%d за последние 48 ч</h2>%s</section>" % (len(dls), "".join(blocks)))

    # ------------------------------------------------------------ индексы
    def _indexes(self):
        out = ["<section class='card' id='indexes'><h2>Индексы и статистика</h2>"]
        mi = self.d.get("missing_indexes") or []
        if mi:
            rows = []
            for r in mi[:20]:
                rows.append("<tr>%s%s%s%s%s%s%s</tr>" % (
                    self.c(esc(r.get("db") or "")),
                    self.c("<code>%s</code>" % esc(r.get("obj") or "")),
                    self.c(esc(r.get("equality_columns") or "—")),
                    self.c(esc(r.get("inequality_columns") or "—")),
                    self.c(esc(r.get("included_columns") or "—")),
                    self.c(fint(r.get("user_seeks"))),
                    self.c(fnum(r.get("avg_user_impact"), 0) + "%")))
            out.append("<h3>Отсутствующие индексы (%d рекомендаций)</h3>"
                       "<p class='muted'>Оценка потенциального эффекта от оптимизатора. "
                       "Проверяйте на тестовой среде — индексы замедляют изменение данных.</p>%s"
                       % (len(mi),
                          self.table(["База", "Таблица", "Колонки (равенство)",
                                      "Колонки (диапазон)", "INCLUDE", "Seeks",
                                      "Эффект"], rows)))
            ddl = "".join("<details class='ddl'><summary>%s</summary>%s</details>"
                          % (esc((r.get("obj") or "?").split(".")[-1]),
                             self.code(r.get("ddl") or ""))
                          for r in mi[:15] if r.get("ddl"))
            out.append("<h4>Готовые DDL</h4>%s" % ddl)
        frag = self.d.get("fragmentation") or []
        if frag:
            rows = []
            for r in frag[:30]:
                f = float(r.get("frag") or 0)
                act = "REBUILD" if f >= 30 else "REORGANIZE"
                rows.append("<tr>%s%s%s%s%s%s</tr>" % (
                    self.c(esc(r.get("db") or "")),
                    self.c("%s.%s" % (esc(r.get("sch") or ""), esc(r.get("tbl") or ""))),
                    self.c(esc(r.get("ix_name") or "")),
                    self.c("<span class='t-%s'>%s%%</span>" %
                           ("crit" if f >= 30 else "warn", fnum(f, 1))),
                    self.c(fint(r.get("page_count"))),
                    self.c("<code>%s</code>" % act)))
            out.append("<h3>Фрагментация индексов (топ-30)</h3>%s" % self.table(
                ["База", "Таблица", "Индекс", "Фрагментация", "Страниц (8КБ)",
                 "Действие"], rows))
        un = self.d.get("unused_indexes") or []
        if un:
            rows = "".join("<tr>%s%s%s%s%s%s</tr>" % (
                self.c(esc(r.get("db") or "")),
                self.c("%s.%s" % (esc(r.get("sch") or ""), esc(r.get("tbl") or ""))),
                self.c(esc(r.get("ix_name") or "")),
                self.c(fint(r.get("updates"))),
                self.c(fnum(r.get("size_mb"), 0) + " МБ"),
                self.c(fnum((r.get("reads") or 0), 0))) for r in un[:25])
            out.append("<h3>Неиспользуемые индексы (кандидаты на удаление)</h3>"
                       "<p class='muted'>Счётчики использования сбрасываются при "
                       "перезапуске SQL Server — учитывайте аптайм.</p>%s"
                       % self.table(["База", "Таблица", "Индекс", "Изменений",
                                     "Размер", "Чтений"], [rows]))
        st = self.d.get("stats") or []
        if st:
            def stale(r):
                try:
                    rows_ = float(r.get("rows") or 0)
                    mods = float(r.get("modification_counter") or 0)
                    if rows_ > 10000 and mods > 0.25 * rows_:
                        return True
                    lu = r.get("last_updated")
                    if isinstance(lu, datetime.datetime):
                        h = (datetime.datetime.now() - lu).total_seconds() / 3600
                        return h > 336 and mods > 0.05 * rows_
                except Exception:
                    pass
                return False
            bad = [r for r in st if stale(r)]
            bad.sort(key=lambda r: -(float(r.get("modification_counter") or 0)))
            rows = "".join("<tr>%s%s%s%s%s%s</tr>" % (
                self.c(esc(r.get("db") or "")),
                self.c("%s.%s" % (esc(r.get("sch") or ""), esc(r.get("tbl") or ""))),
                self.c(esc(r.get("stat_name") or "")),
                self.c(fint(r.get("rows"))),
                self.c(fint(r.get("modification_counter"))),
                self.c(esc(fdt(r.get("last_updated"))))) for r in bad[:25])
            out.append("<h3>Устаревшая статистика (%d)</h3>%s"
                       % (len(bad), self.table(
                           ["База", "Таблица", "Статистика", "Строк", "Изменено",
                            "Обновлялась"], [rows] if rows else [],
                           empty="Устаревшей статистики не найдено") if rows else
                          "<div class='okbox'>Устаревшей статистики не найдено.</div>"))
        if not (mi or frag or un or st):
            out.append("<div class='nodata'>Данные по индексам недоступны.</div>")
        out.append("</section>")
        return "".join(out)

    # ------------------------------------------------------------ базы/файлы
    def _databases(self):
        d = (self.d.get("databases") or {})
        dbs = d.get("databases") or []
        rows = []
        for r in dbs:
            rows.append("<tr>%s%s%s%s%s%s%s%s%s%s</tr>" % (
                self.c("<b>%s</b>" % esc(r.get("name"))),
                self.c(esc(r.get("state_desc") or "")),
                self.c(esc(r.get("recovery_model_desc") or "")),
                self.c(fint(r.get("compatibility_level"))),
                self.c("✓" if int(r.get("is_read_committed_snapshot_on") or 0) else "—"),
                self.c(esc(r.get("log_reuse_wait_desc") or "")),
                self.c(fnum(r.get("data_mb"), 0)),
                self.c(fnum(r.get("log_mb"), 0)),
                self.c((fnum(r.get("log_used_pct"), 0) + "%") if r.get("log_used_pct") is not None else "—"),
                self.c(fint(r.get("vlf")) if r.get("vlf") is not None else "—")))
        files = d.get("files") or []
        frows = []
        for f in files[:120]:
            growth = ("%s%%" % f.get("growth")) if int(f.get("is_percent_growth") or 0) \
                else ("%s МБ" % fnum((float(f.get("growth") or 0) * 8), 0))
            frows.append("<tr>%s%s%s%s%s%s</tr>" % (
                self.c(esc(f.get("db") or "")),
                self.c(esc(f.get("logical_name") or "")),
                self.c(esc((f.get("type_desc") or ""))),
                self.c(fnum(f.get("size_mb"), 0)),
                self.c(growth),
                self.c("<span class='path'>%s</span>" % esc(f.get("physical_name") or ""))))
        return """
<section class='card' id='databases'><h2>Базы данных и файлы</h2>
%(db)s
<h3>Файлы и настройки роста</h3>
%(files)s
</section>""" % {
            "db": self.table(["База", "Состояние", "Модель восст.", "Compat",
                              "RCSI", "log_reuse_wait", "Данные, МБ", "Журнал, МБ",
                              "Журнал занят", "VLF"], rows),
            "files": self.table(["База", "Файл", "Тип", "Размер, МБ", "Автоприращение",
                                 "Путь"], frows)}

    def _disks(self):
        io = self.d.get("io_files") or []
        rows = []
        for r in io:
            rd = float(r.get("num_of_reads") or 0)
            wr = float(r.get("num_of_writes") or 0)
            rl = float(r.get("io_stall_read_ms") or 0) / rd if rd > 100 else None
            wl = float(r.get("io_stall_write_ms") or 0) / wr if wr > 100 else None
            def cls(v, lim):
                return "" if v is None else ("t-crit" if v >= lim * 2 else
                                             ("t-warn" if v >= lim else "t-ok"))
            rows.append("<tr>%s%s%s%s%s%s%s</tr>" % (
                self.c(esc(r.get("db") or "")),
                self.c(esc(r.get("logical_name") or "")),
                self.c(fint(rd)),
                self.c("<span class='%s'>%s</span>" % (cls(rl, 20),
                                                       fnum(rl, 1) if rl is not None else "—")),
                self.c(fint(wr)),
                self.c("<span class='%s'>%s</span>" % (cls(wl, 20),
                                                       fnum(wl, 1) if wl is not None else "—")),
                self.c(fnum(float(r.get("size_on_disk_bytes") or 0) / 1073741824.0, 1))))
        vols = self.d.get("volumes") or []
        vrows = []
        for v in vols:
            tot = float(v.get("total_gb") or 0)
            fr = float(v.get("free_gb") or 0)
            pct = (100 * fr / tot) if tot else 0
            vrows.append("<tr>%s%s%s%s%s</tr>" % (
                self.c(esc(v.get("volume_mount_point") or "")),
                self.c(esc(v.get("file_system_type") or "")),
                self.c(fnum(tot, 1)),
                self.c(fnum(fr, 1)),
                self.c("<span class='%s'>%s%%</span>" %
                       ("t-crit" if pct < 10 else ("t-warn" if pct < 20 else "t-ok"),
                        fnum(pct, 1)))))
        return """
<section class='card' id='disks'><h2>Диски и ввод-вывод</h2>
<h3>Задержки по файлам (с момента запуска сервера)</h3>
<p class='muted'>Зелёный — норма, жёлтый — повышенные (>20 мс), красный — высокие (>40 мс).</p>
%(io)s
<h3>Тома (свободное место)</h3>
%(vols)s
</section>""" % {"io": self.table(["База", "Файл", "Чтений", "Ср. чтение, мс", "Записей",
                                   "Ср. запись, мс", "Размер, ГБ"], rows),
                 "vols": self.table(["Том", "ФС", "Всего, ГБ", "Свободно, ГБ",
                                     "Свободно %"], vrows)}

    def _tempdb(self):
        td = self.d.get("tempdb") or {}
        if not td:
            return ""
        files = td.get("files") or []
        sp = td.get("space") or {}
        tr = td.get("snapshot_tr") or []
        frows = "".join("<tr>%s%s%s</tr>" % (
            self.c(esc(f.get("name") or "")),
            self.c(fnum(f.get("size_mb"), 0)),
            self.c("<span class='path'>%s</span>" % esc(f.get("physical_name") or "")))
            for f in files)
        trows = "".join("<tr>%s%s%s</tr>" % (self.c(fint(t.get("session_id"))),
                                             self.c(fhms(t.get("elapsed_time_seconds"))),
                                             self.c(esc(t.get("db") or ""))) for t in tr)
        return """
<section class='card' id='tempdb'><h2>tempdb</h2>
<div class='stats'>
 <div class='stat'><div class='stat-v'>%(files)d</div><div class='stat-k'>файлов данных</div></div>
 <div class='stat'><div class='stat-v'>%(user)s МБ</div><div class='stat-k'>пользовательские объекты</div></div>
 <div class='stat'><div class='stat-v'>%(internal)s МБ</div><div class='stat-k'>внутренние объекты</div></div>
 <div class='stat'><div class='stat-v'>%(ver)s МБ</div><div class='stat-k'>version store</div></div>
 <div class='stat'><div class='stat-v'>%(free)s МБ</div><div class='stat-k'>свободно</div></div>
</div>
%(ftbl)s
<h3>Длинные транзакции снимков (держат version store)</h3>
%(ttbl)s
</section>""" % {
            "files": len([f for f in files if (f.get("type_desc") or "") == "ROWS"]),
            "user": fnum(sp.get("user_mb"), 0), "internal": fnum(sp.get("internal_mb"), 0),
            "ver": fnum(sp.get("version_mb"), 0), "free": fnum(sp.get("free_mb"), 0),
            "ftbl": self.table(["Файл", "Размер, МБ", "Путь"], [frows] if frows else []),
            "ttbl": self.table(["Сессия", "Длится", "База"], [trows] if trows else [],
                               empty="Нет активных транзакций снимков")}

    # ------------------------------------------------------------ настройки
    def _config(self):
        cfg = {r.get("name"): r for r in (self.d.get("configs") or [])}
        recs = {
            "max server memory (MB)": ("Лимит с резервом ОС/служб по измерениям",),
            "max degree of parallelism": ("По профилю нагрузки, online CPU и NUMA",),
            "cost threshold for parallelism": ("По планам и дельтам waits, не универсальное значение",),
            "optimize for ad hoc workloads": ("1 (включить)",),
            "max worker threads": ("0 (авто) — менять только при THREADPOOL-проблемах",),
            "clr enabled": ("0 (без необходимости не включать)",),
            "xp_cmdshell": ("0 (безопасность)",),
            "priority boost": ("0 (всегда!)",),
            "lightweight pooling": ("0 (всегда!)",),
            "remote query timeout (s)": ("600 по умолчанию",),
            "blocked process threshold (s)": ("5 — чтобы ловить блокировки",),
        }
        rows = []
        for name, rec in recs.items():
            r = cfg.get(name)
            if not r:
                continue
            rows.append("<tr>%s%s%s%s</tr>" % (
                self.c(esc(name)),
                self.c("<b>%s</b>" % esc(r.get("value_in_use"))),
                self.c(esc(rec[0])),
                self.c("<span class='muted'>—</span>")))
        si = self.d.get("sys_info") or {}
        st = si.get("sqlserver_start_time")
        uptime = ""
        if isinstance(st, datetime.datetime):
            sec = (datetime.datetime.now() - st).total_seconds()
            uptime = "%d дн %d ч" % (sec // 86400, (sec % 86400) // 3600)
        phys = si.get("phys_kb")
        srv = self.d.get("server") or {}
        info_rows = "".join("<tr><td>%s</td><td>%s</td></tr>" % (esc(k), esc(v)) for k, v in [
            ("Продукт", srv.get("product_line") or "—"),
            ("Выпуск", srv.get("edition") or "—"),
            ("Версия / уровень", "%s (%s %s)" % (srv.get("ver") or "?",
                                                 srv.get("level") or "",
                                                 srv.get("cu") or "")),
            ("Имя сервера", self.meta.get("server_full") or "—"),
            ("Потенциально в AlwaysOn", "да" if srv.get("ag_count") else "нет"),
            ("Логических процессоров", fint(si.get("cpu_count"))),
            ("NUMA-узлов", fint(si.get("nodes"))),
            ("Физическая память", fnum(float(phys) / 1048576.0, 1) + " ГБ"
             if phys else "—"),
            ("SQL Server использует", fnum(float(si.get("in_use_kb") or 0) / 1048576.0, 1)
             + " ГБ" if si.get("in_use_kb") else "—"),
            ("Запущен", fdt(st) + (" (аптайм %s)" % uptime if uptime else "")),
            ("Сопоставление (collation)", srv.get("collation") or "—"),
            ("ОС", srv.get("os_line") or "—"),
        ])
        svc = si.get("services") or []
        svc_rows = "".join("<tr>%s%s%s%s</tr>" % (
            self.c(esc(s.get("servicename") or "")),
            self.c(esc(s.get("startup_type_desc") or "")),
            self.c(esc(s.get("status_desc") or "")),
            self.c(esc(s.get("service_account") or ""))) for s in svc)
        tf = self.d.get("trace_flags") or []
        tf_html = ", ".join("TF %s" % fint(t.get("TraceFlag")) for t in tf) or "нет"
        return """
<section class='card' id='config'><h2>Настройки сервера и окружение</h2>
<div class='grid2'>
<div><h3>Сервер</h3><table class='kv'>%(info)s</table>
<h3>Службы</h3>%(svc)s</div>
<div><h3>Ключевые параметры конфигурации</h3>%(cfg)s
<h3>Глобальные трассировочные флаги</h3><p>%(tf)s</p></div>
</div></section>""" % {"info": info_rows,
                       "svc": self.table(["Служба", "Запуск", "Состояние", "Аккаунт"],
                                         [svc_rows] if svc_rows else [],
                                         empty="Нет доступа к списку служб"),
                       "cfg": self.table(["Параметр", "Текущее", "Рекомендуемое", "Статус"],
                                         rows),
                       "tf": esc(tf_html)}

    # ------------------------------------------------------------ бэкапы/задания
    def _ops(self):
        bks = self.d.get("backups") or []
        brows = []
        for b in bks:
            def ago(k):
                h = b.get(k + "_ago_h")
                if h is None:
                    return esc(fdt(b.get(k)))
                if not b.get(k):
                    return "<span class='t-crit'>нет!</span>"
                bad = h > 168 if k == "last_full" else h > 24
                return "<span class='%s'>%s (%s ч назад)</span>" % (
                    "t-crit" if bad else "t-ok", esc(fdt(b.get(k))), fint(h))
            brows.append("<tr>%s%s%s%s%s</tr>" % (
                self.c("<b>%s</b>" % esc(b.get("db"))),
                self.c(esc(b.get("recovery_model_desc") or "")),
                self.c(ago("last_full")), self.c(ago("last_diff")), self.c(ago("last_log"))))
        jobs = self.d.get("jobs_failed") or []
        jrows = "".join("<tr>%s%s%s</tr>" % (
            self.c(esc(fdt(j.get("run_dt")))),
            self.c("<b>%s</b>" % esc(j.get("job"))),
            self.c("<code class='small'>%s</code>" % esc(j.get("message_head") or "")))
            for j in jobs[:25])
        return """
<section class='card' id='ops'><h2>Резервное копирование и задания SQL Agent</h2>
<h3>Последние резервные копии</h3>
%(bk)s
<h3>Неудачные задания (последние 2 дня)</h3>
%(jobs)s
</section>""" % {"bk": self.table(["База", "Модель", "Полная", "Разностная",
                                   "Журнала"], brows),
                 "jobs": self.table(["Когда", "Задание", "Сообщение"],
                                    [jrows] if jrows else [],
                                    empty="Сбоев заданий не зафиксировано")}

    def _errorlog(self):
        el = self.d.get("errorlog")
        if el is None:
            return ""
        if not el:
            return ("<section class='card' id='el'><h2>Журнал ошибок SQL Server</h2>"
                    "<div class='okbox'>Подозрительных записей не найдено.</div></section>")
        rows = "".join("<tr>%s%s</tr>" % (
            self.c(esc(fdt(e.get("date")))),
            self.c("<code class='small'>%s</code>" % esc(e.get("text") or "")))
            for e in el[:60])
        return ("<section class='card' id='el'><h2>Журнал ошибок SQL Server — "
                "подозрительные записи (%d)</h2>%s<p class='muted'>Полный журнал: "
                "SSMS → Management → SQL Server Logs.</p></section>"
                % (len(el), self.table(["Время", "Сообщение"], [rows])))

    # ------------------------------------------------------------ рекомендации
    def _recs(self):
        if not self.findings:
            return ("<section class='card' id='recs'><h2>Рекомендации</h2>"
                    "<div class='okbox'>Автоматических замечаний не найдено. Сервер "
                    "выглядит настроенным хорошо.</div></section>")
        cards = []
        for f in self.findings:
            ev = "".join("<li>%s</li>" % esc(e) for e in (f.get("evidence") or []))
            sql = self.code(f["sql"]) if f.get("sql") else ""
            cards.append("""
<div class='rec r-%(sev)s' data-sev='%(sev)s'>
 <div class='rec-h'><span class='badge b-%(sev)s'>%(sevtitle)s</span>
   <b>%(num)d. %(title)s</b> <span class='cat'>· %(cat)s</span></div>
 %(ev)s
 <div class='rec-why'><b>Что это значит:</b> %(why)s</div>
 <div class='rec-act'><b>Как исправить:</b> %(act)s</div>
 %(sql)s
</div>""" % {"sev": f["severity"], "sevtitle": analysis.SEV_TITLES[f["severity"]],
             "num": f.get("num", 0), "title": esc(f["title"]), "cat": esc(f["cat"]),
             "ev": ("<ul class='ev'>%s</ul>" % ev) if ev else "",
             "why": esc(f["why"]), "act": esc(f["action"]), "sql": sql})
        return """
<section class='card' id='recs'><h2>Рекомендации (%(total)d)</h2>
<div class='filters no-print'>
 <button onclick="filt('all')" class='on'>Все (%(total)d)</button>
 <button onclick="filt('critical')">⛔ Критично (%(crit)d)</button>
 <button onclick="filt('warning')">⚠️ Важно (%(warn)d)</button>
 <button onclick="filt('info')">💡 Рекомендации (%(info)d)</button>
</div>
<div id='reclist'>%(cards)s</div>
<p class='muted'>Все рекомендации вырабатываются автоматически на основе данных DMV.
Перед применением T-SQL проверяйте его на тестовой среде.</p>
</section>""" % {"total": len(self.findings), "crit": self.health.get("crit", 0),
                 "warn": self.health.get("warn", 0), "info": self.health.get("info", 0),
                 "cards": "".join(cards)}

    def _footer(self):
        return """
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
<footer>Сформировано MS SQL Monitor · %s · Данные: DMV Microsoft SQL Server</footer>
""" % esc(self.meta.get("generated_str") or "")

    # ------------------------------------------------------------ body
    def _body(self):
        return """<body>
%(header)s
<main>
%(summary)s
%(timeline)s
%(peaks)s
%(waits)s
%(queries)s
%(sessions)s
%(longq)s
%(deadlocks)s
%(indexes)s
%(databases)s
%(disks)s
%(tempdb)s
%(config)s
%(ops)s
%(el)s
%(recs)s
%(footer)s
</main>
<script>
function filt(sev){
 document.querySelectorAll('#reclist .rec').forEach(function(c){
   c.style.display=(sev==='all'||c.dataset.sev===sev)?'':'none';});
 document.querySelectorAll('.filters button').forEach(function(b){b.classList.remove('on');});
 event.target.classList.add('on');
}
</script>
</body></html>""" % {
            "header": self._header(),
            "summary": self._summary(),
            "timeline": self._timeline(),
            "peaks": self._peaks(),
            "waits": self._waits(),
            "queries": self._queries(),
            "sessions": self._requests_live(),
            "longq": self._long_queries(),
            "deadlocks": self._deadlocks(),
            "indexes": self._indexes(),
            "databases": self._databases(),
            "disks": self._disks(),
            "tempdb": self._tempdb(),
            "config": self._config(),
            "ops": self._ops(),
            "el": self._errorlog(),
            "recs": self._recs(),
            "footer": self._footer(),
        }


_NAV = [
    ("summary", "Сводка"), ("timeline", "Графики"), ("peaks", "Замедления"),
    ("waits", "Ожидания"), ("queries", "Запросы"), ("sessions", "Сессии"),
    ("deadlocks", "Блокировки"), ("indexes", "Индексы"), ("databases", "Базы"),
    ("disks", "Диски"), ("tempdb", "tempdb"), ("config", "Настройки"),
    ("ops", "Бэкапы"), ("recs", "Рекомендации"),
]

_HEAD_CSS = """<!DOCTYPE html>
<html lang="ru"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Отчёт MS SQL Monitor — %TITLE%</title>
<style>
:root{--bg:#eef1f6;--card:#ffffff;--tx:#17202a;--mut:#5b6b7c;--acc:#1a5fb4;
 --crit:#c62828;--warn:#e65100;--info:#0277bd;--ok:#2e7d32;--bd:#dde4ec}
*{box-sizing:border-box}
body{margin:0;font:14px/1.5 "Segoe UI",system-ui,-apple-system,Roboto,Arial,sans-serif;
 background:var(--bg);color:var(--tx)}
main{max-width:1180px;margin:0 auto;padding:14px}
.card{background:var(--card);border:1px solid var(--bd);border-radius:12px;
 padding:18px 20px;margin:14px 0;box-shadow:0 1px 3px rgba(20,40,80,.06)}
.hero{background:linear-gradient(135deg,#123a75,#1a5fb4);color:#fff;border:none}
.hero .h1{font-size:22px;font-weight:700;margin:2px 0 10px}
.hero .brand{opacity:.85;font-size:12px;text-transform:uppercase;letter-spacing:.08em}
.brand{opacity:.85}
.hero-grid{display:flex;gap:26px;flex-wrap:wrap;align-items:center;justify-content:space-between}
.hero .kv td{padding:2px 10px 2px 0;font-size:13px;opacity:.95}
.hero-score{text-align:center;min-width:230px}
.hero .grade{font-size:16px;font-weight:600;margin-top:-6px}
.score-cards{display:flex;gap:8px;justify-content:center;margin-top:10px;flex-wrap:wrap}
.sc{background:rgba(255,255,255,.14);border-radius:8px;padding:2px 8px;font-size:12px}
nav.nav{position:sticky;top:0;z-index:50;background:#ffffffee;backdrop-filter:blur(6px);
 border-bottom:1px solid var(--bd);padding:8px 14px;display:flex;gap:4px;flex-wrap:wrap;
 max-width:1180px;margin:0 auto;border-radius:0 0 10px 10px}
nav.nav a{text-decoration:none;color:var(--acc);padding:3px 9px;border-radius:7px;font-size:13px}
nav.nav a:hover{background:#e8f0fb}
h2{margin:4px 0 12px;font-size:19px;border-left:4px solid var(--acc);padding-left:10px}
h3{font-size:15px;margin:18px 0 8px}
h4{font-size:13px;margin:12px 0 6px;color:var(--mut)}
.lead{font-size:15px}
.muted{color:var(--mut)}
.small{font-size:12px}
table.kv td{padding:2px 12px 2px 0;vertical-align:top}
table.tbl{border-collapse:collapse;width:100%;margin:8px 0;font-size:13px}
table.tbl th{background:#f0f4f9;text-align:left;padding:7px 9px;border:1px solid var(--bd);
 font-weight:600;white-space:nowrap}
table.tbl td{padding:6px 9px;border:1px solid var(--bd);vertical-align:top}
table.tbl tr:nth-child(even) td{background:#fafbfd}
.nowrap{white-space:nowrap}
.tbl code.small, code.small{font:11.5px/1.45 Consolas,"Cascadia Mono",monospace;
 white-space:pre-wrap;word-break:break-word}
.path{font:11.5px Consolas,monospace;color:var(--mut)}
details summary{cursor:pointer;color:var(--acc);white-space:nowrap}
.nodata{color:var(--mut);font-style:italic;padding:10px 0}
.okbox{background:#e8f5e9;border:1px solid #a5d6a7;color:#1b5e20;border-radius:8px;padding:10px 14px}
.warnbox{background:#fff8e1;border:1px solid #ffe082;border-radius:8px;padding:10px 14px;
 margin-top:12px;font-size:13px}
.warnbox ul{margin:6px 0 2px 18px}
.toplist{list-style:none;padding:0;margin:10px 0}
.toplist li{padding:5px 0;border-bottom:1px dashed var(--bd)}
a.sevlink{text-decoration:none;color:var(--tx)}
a.s-critical{color:var(--crit)} a.s-warning{color:var(--warn)} a.s-info{color:var(--info)}
.t-crit{color:var(--crit);font-weight:600}.t-warn{color:var(--warn);font-weight:600}
.t-ok{color:var(--ok)}.t-info{color:var(--info)}
.stats{display:flex;gap:12px;flex-wrap:wrap;margin:10px 0}
.stat{background:#f4f7fb;border:1px solid var(--bd);border-radius:10px;padding:8px 14px;
 min-width:120px;flex:1}
.stat-v{font-size:17px;font-weight:700}
.stat-k{font-size:11.5px;color:var(--mut)}
.chart{width:100%;height:auto;background:#fbfcfe;border:1px solid var(--bd);border-radius:8px}
.chart-block{margin:16px 0}
.grid{stroke:#e3e9f1;stroke-width:1}
.axis{stroke:#9fb0c3;stroke-width:1.2}
.tick{font:11px "Segoe UI",sans-serif;fill:#61707f;text-anchor:end}
text.text-anchor{font:11px "Segoe UI",sans-serif;fill:#61707f}
.unit{font:11px "Segoe UI",sans-serif;fill:#61707f;text-anchor:start}
.donut{width:230px;height:230px}
.donut-c{font:700 16px "Segoe UI",sans-serif;fill:#17202a}
.donut-s{font:11px "Segoe UI",sans-serif;fill:#5b6b7c}
.gauge{width:210px;height:124px}
.gauge-v{font:700 34px "Segoe UI",sans-serif}
.gauge-s{font:12px "Segoe UI",sans-serif;fill:#5b6b7c}
.legend{display:flex;gap:14px;flex-wrap:wrap;margin:6px 0;font-size:12.5px;color:var(--mut)}
.lg{display:inline-flex;align-items:center;gap:5px}
.sw{width:12px;height:12px;border-radius:3px;display:inline-block}
.sw.region{background:repeating-linear-gradient(45deg,#e5393522,#e5393522 4px,#e5393544 4px,#e5393544 8px)}
.hbars{margin:8px 0}
.hbar-row{display:flex;align-items:center;gap:8px;margin:3px 0}
.hbar-l{width:34%;text-align:right;font-size:12px;color:var(--tx);white-space:nowrap;
 overflow:hidden;text-overflow:ellipsis}
.hbar-t{flex:1;background:#eef2f7;border-radius:4px;height:16px;overflow:hidden}
.hbar{height:100%;border-radius:4px}
.hbar-v{width:90px;font-size:12px;color:var(--mut)}
.peak{border:1px solid var(--bd);border-left:5px solid var(--crit);border-radius:10px;
 padding:12px 16px;margin:12px 0;background:#fffafa}
.peak h3{margin:2px 0 6px}
.peak-stats{font-size:13px;color:var(--mut);margin-bottom:6px}
.reasons ul{margin:6px 0 4px 20px}
.reasons li{margin:3px 0}
.waits-grid{display:grid;grid-template-columns:290px 1fr;gap:18px;align-items:start}
@media(max-width:900px){.waits-grid{grid-template-columns:1fr}}
.cats{display:grid;grid-template-columns:repeat(auto-fill,minmax(330px,1fr));gap:12px}
.cat-card{border:1px solid var(--bd);border-radius:10px;padding:10px 14px;background:#fbfcfe}
.cat-h{margin-bottom:4px}
.sev-dot{display:inline-block;width:10px;height:10px;border-radius:50%;margin-right:6px}
.rec{border:1px solid var(--bd);border-radius:10px;padding:12px 16px;margin:10px 0;
 background:#fff}
.r-critical{border-left:6px solid var(--crit)}
.r-warning{border-left:6px solid var(--warn)}
.r-info{border-left:6px solid var(--info)}
.rec-h{display:flex;gap:8px;align-items:baseline;flex-wrap:wrap;margin-bottom:6px}
.badge{color:#fff;border-radius:6px;padding:1px 8px;font-size:11.5px;font-weight:600}
.b-critical{background:var(--crit)}.b-warning{background:var(--warn)}.b-info{background:var(--info)}
.cat{color:var(--mut);font-size:12.5px}
ul.ev{margin:4px 0 8px 20px;color:#37474f;font-size:13px}
.rec-why,.rec-act{margin:6px 0;font-size:13.5px}
.rec-act{background:#f1f8f4;border-radius:8px;padding:8px 12px}
pre.sql{background:#10212f;color:#d7e4f0;border-radius:8px;padding:10px 14px;overflow:auto;
 font:12.5px/1.5 Consolas,"Cascadia Mono",monospace;white-space:pre-wrap}
.filters{display:flex;gap:6px;flex-wrap:wrap;margin:8px 0}
.filters button{border:1px solid var(--bd);background:#fff;border-radius:8px;padding:4px 12px;
 cursor:pointer;font-size:12.5px}
.filters button.on{background:var(--acc);color:#fff;border-color:var(--acc)}
footer{text-align:center;color:var(--mut);font-size:12px;margin:20px 0}
@media print{.no-print{display:none}body{background:#fff}.card{box-shadow:none;
 page-break-inside:avoid}nav.nav{display:none}}
</style></head>
"""


def build_report(data, findings, health, peaks, samples, blocking_events,
                 long_queries, meta):
    r = Report(data, findings, health, peaks, samples, blocking_events,
               long_queries, meta)
    return r.build()
