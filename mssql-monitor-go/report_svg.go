// report_svg.go — SVG-графики и форматирование чисел (порт report.py, 1:1).
package main

import (
	"fmt"
	"math"
	"strings"
	"time"
)

var paletteOK = []string{"#1a5fb4", "#e8710a", "#188038", "#c62828", "#7b1fa2",
	"#00838f", "#5d4037", "#546e7a", "#f9a825", "#455a64"}

var catColors = map[string]string{
	"cpu": "#1a5fb4", "lock": "#c62828", "latch": "#7b1fa2",
	"io": "#e8710a", "network": "#00838f", "memory": "#f9a825",
	"log": "#5d4037", "hadr": "#188038", "other": "#90a4ae",
}

func esc(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&#39;")
	// html.escape заменяет кавычку на &#x27;; используем близкий аналог
	return r.Replace(s)
}

func escQ(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&#x27;", "'", "&#x27;")
	return r.Replace(s)
}

// fnum — русское форматирование: 1 234,5
func fnum(v any, nd int) string {
	f, ok := toFloat(v)
	if !ok {
		return "—"
	}
	neg := f < 0
	if neg {
		f = -f
	}
	s := fmt.Sprintf("%.*f", nd, f)
	intPart, frac := s, ""
	if i := strings.IndexByte(s, '.'); i >= 0 {
		intPart, frac = s[:i], s[i+1:]
	}
	var parts []string
	for len(intPart) > 3 {
		parts = append([]string{intPart[len(intPart)-3:]}, parts...)
		intPart = intPart[:len(intPart)-3]
	}
	parts = append([]string{intPart}, parts...)
	out := strings.Join(parts, " ")
	if frac != "" {
		out += "," + frac
	}
	if neg {
		out = "-" + out
	}
	return out
}

func fint(v any) string {
	f, ok := toFloat(v)
	if !ok {
		return "—"
	}
	neg := f < 0
	if neg {
		f = -f
	}
	s := fmt.Sprintf("%.0f", f)
	var parts []string
	for len(s) > 3 {
		parts = append([]string{s[len(s)-3:]}, parts...)
		s = s[:len(s)-3]
	}
	parts = append([]string{s}, parts...)
	out := strings.Join(parts, " ")
	if neg {
		out = "-" + out
	}
	return out
}

func fdt(v any) string {
	if t, ok := v.(time.Time); ok {
		return t.Format("02.01.2006 15:04")
	}
	if v == nil {
		return "—"
	}
	return gs(map[string]any{"v": v}, "v")
}

func fhms(sec float64) string {
	if sec != sec || sec == 0 { // NaN
		return "—"
	}
	if sec >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", int(sec)/3600, (int(sec)%3600)/60, int(sec)%60)
	}
	if sec >= 60 {
		return fmt.Sprintf("%d мин %02d с", int(sec)/60, int(sec)%60)
	}
	return fmt.Sprintf("%.1f с", sec)
}

func fmtMSru(v any) string {
	f, ok := toFloat(v)
	if !ok {
		return "—"
	}
	switch {
	case f >= 3600000:
		return fnum(f/3600000.0, 1) + " ч"
	case f >= 60000:
		return fnum(f/60000.0, 1) + " мин"
	default:
		return fnum(f/1000.0, 0) + " с"
	}
}

func toFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case float32:
		return float64(x), true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case uint64:
		return float64(x), true
	case *float64:
		if x == nil {
			return 0, false
		}
		return *x, true
	case *int:
		if x == nil {
			return 0, false
		}
		return float64(*x), true
	case string:
		return parseFloatRu(x)
	case []byte:
		return parseFloatRu(string(x))
	case nil:
		return 0, false
	}
	return 0, false
}

func parseFloatRu(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	var f float64
	if _, err := fmt.Sscanf(strings.ReplaceAll(s, ",", "."), "%g", &f); err != nil {
		return 0, false
	}
	return f, true
}

func niceStep(raw float64) float64 {
	if raw <= 0 {
		return 1
	}
	mag := math.Pow(10, math.Floor(math.Log10(raw)))
	for _, m := range []float64{1, 2, 2.5, 5, 10} {
		if raw <= m*mag {
			return m * mag
		}
	}
	return 10 * mag
}

// ---------- svg_line ----------

type linePoint struct {
	TS  int64
	Val *float64
}

type lineSeries struct {
	Name   string
	Color  string
	Points []linePoint
	Dash   bool
}

func svgLine(series []lineSeries, t0, t1 int64, h int, yMax *float64,
	yMin float64, unit string, regions [][2]int64) string {
	const W = 1040
	L, R, T, B := 62, 14, 16, 30
	H := h
	if H == 0 {
		H = 250
	}
	iw, ih := W-L-R, H-T-B
	if t1 <= t0 {
		t1 = t0 + 1
	}
	var vals []float64
	for _, s := range series {
		for _, p := range s.Points {
			if p.Val != nil {
				vals = append(vals, *p.Val)
			}
		}
	}
	if len(vals) == 0 {
		return "<div class='nochart'>Недостаточно данных для графика</div>"
	}
	vmax := maxOf(vals)
	vmin := yMin
	if m := minOf(vals); m < vmin {
		vmin = m
	}
	if yMax != nil {
		if *yMax > vmax {
			vmax = *yMax
		}
	}
	if vmax == vmin {
		vmax = vmin + 1
	}
	vmax *= 1.08
	step := niceStep((vmax - vmin) / 5)

	ft0, ft1 := float64(t0), float64(t1)
	X := func(ts float64) float64 { return float64(L) + (ts-ft0)/(ft1-ft0)*float64(iw) }
	Y := func(v float64) float64 { return float64(T) + float64(ih) - (v-vmin)/(vmax-vmin)*float64(ih) }

	var p strings.Builder
	fmt.Fprintf(&p, "<svg viewBox='0 0 %d %d' class='chart' preserveAspectRatio='xMidYMid meet'>", W, H)
	for v := vmin; v <= vmax+1e-9; v += step {
		y := Y(v)
		fmt.Fprintf(&p, "<line x1='%d' y1='%.1f' x2='%d' y2='%.1f' class='grid'/>",
			L, y, W-R, y)
		nd := 1
		if step >= 1 {
			nd = 0
		}
		fmt.Fprintf(&p, "<text x='%d' y='%.1f' class='tick'>%s</text>", L-6, y+4, fnum(v, nd))
	}
	for _, rg := range regions {
		x1 := X(math.Max(float64(rg[0]), ft0))
		x2 := X(math.Min(float64(rg[1]), ft1))
		if x2-x1 > 1 {
			fmt.Fprintf(&p, "<rect x='%.1f' y='%d' width='%.1f' height='%d' "+
				"fill='#e53935' fill-opacity='0.09'/>", x1, T, x2-x1, ih)
		}
	}
	fmt.Fprintf(&p, "<line x1='%d' y1='%d' x2='%d' y2='%d' class='axis'/>", L, T, L, T+ih)
	fmt.Fprintf(&p, "<line x1='%d' y1='%.1f' x2='%d' y2='%.1f' class='axis'/>",
		L, float64(T+ih), W-R, float64(T+ih))
	nTicks := min(9, max(4, iw/120))
	span := ft1 - ft0
	for i := 0; i <= nTicks; i++ {
		ts := t0 + int64(span*float64(i)/float64(nTicks))
		lbl := unixFmt(ts, "15:04")
		if span > 86400*1.5 {
			lbl = unixFmt(ts, "02.01 15:04")
		}
		fmt.Fprintf(&p, "<text x='%.1f' y='%d' class='tick' text-anchor='middle'>%s</text>",
			X(float64(ts)), T+ih+18, lbl)
	}
	if unit != "" {
		fmt.Fprintf(&p, "<text x='%d' y='%d' class='unit'>%s</text>", L-6, T-5, esc(unit))
	}
	for _, s := range series {
		dAttr := ""
		if s.Dash {
			dAttr = " stroke-dasharray='6 4'"
		}
		fmt.Fprintf(&p, "<path fill='none' stroke='%s' stroke-width='2'%s d='", s.Color, dAttr)
		started := false
		for _, pt := range s.Points {
			if pt.Val == nil {
				started = false
				continue
			}
			cmd := "L"
			if !started {
				cmd = "M"
			}
			fmt.Fprintf(&p, "%s%.1f %.1f ", cmd, X(float64(pt.TS)), Y(*pt.Val))
			started = true
		}
		p.WriteString("'/>")
	}
	p.WriteString("</svg>")
	return p.String()
}

func maxOf(xs []float64) float64 {
	m := math.Inf(-1)
	for _, x := range xs {
		if x > m {
			m = x
		}
	}
	return m
}

func minOf(xs []float64) float64 {
	m := math.Inf(1)
	for _, x := range xs {
		if x < m {
			m = x
		}
	}
	return m
}

// ---------- svg_donut ----------

type donutPart struct {
	Label string
	Val   float64
	Color string
}

func svgDonut(parts []donutPart, center string) string {
	total := 0.0
	for _, p := range parts {
		total += p.Val
	}
	if total == 0 {
		total = 1
	}
	cx, cy, r, sw := 90.0, 90.0, 62.0, 30
	a0 := -90.0
	var b strings.Builder
	b.WriteString("<svg viewBox='0 0 180 180' class='donut'>")
	for _, p := range parts {
		if p.Val <= 0 {
			continue
		}
		frac := p.Val / total
		a1 := a0 + frac*360
		large := 0
		if a1-a0 > 180 {
			large = 1
		}
		x0 := cx + r*math.Cos(a0*math.Pi/180)
		y0 := cy + r*math.Sin(a0*math.Pi/180)
		x1 := cx + r*math.Cos(a1*math.Pi/180)
		y1 := cy + r*math.Sin(a1*math.Pi/180)
		fmt.Fprintf(&b, "<path d='M %.2f %.2f A %.1f %.1f 0 %d 1 %.2f %.2f' "+
			"fill='none' stroke='%s' stroke-width='%d'><title>%s: %s (%.1f%%)</title></path>",
			x0, y0, r, r, large, x1, y1, p.Color, sw, esc(p.Label), fmtMSru(p.Val), frac*100)
		a0 = a1
	}
	fmt.Fprintf(&b, "<text x='90' y='88' text-anchor='middle' class='donut-c'>%s</text>"+
		"<text x='90' y='106' text-anchor='middle' class='donut-s'>%s</text></svg>",
		esc(center), "итого: "+fmtMSru(total))
	return b.String()
}

// ---------- svg_gauge ----------

func svgGauge(score int) string {
	frac := float64(score) / 100.0
	if frac < 0 {
		frac = 0
	}
	if frac > 1 {
		frac = 1
	}
	cx, cy, r, sw := 100.0, 96.0, 74.0, 16
	color := "#c62828"
	switch {
	case score >= 75:
		color = "#2e7d32"
	case score >= 55:
		color = "#f9a825"
	}
	a0 := 180.0
	a1 := 180.0 + frac*180
	x0 := cx + r*math.Cos(a0*math.Pi/180)
	y0 := cy + r*math.Sin(a0*math.Pi/180)
	x1 := cx + r*math.Cos(a1*math.Pi/180)
	y1 := cy + r*math.Sin(a1*math.Pi/180)
	return fmt.Sprintf("<svg viewBox='0 0 200 118' class='gauge'>"+
		"<path d='M %.1f %.1f A %.1f %.1f 0 0 1 %.1f %.1f' fill='none' "+
		"stroke='#e0e6ee' stroke-width='%d'/>"+
		"<path d='M %.1f %.1f A %.1f %.1f 0 0 1 %.1f %.1f' fill='none' "+
		"stroke='%s' stroke-width='%d' stroke-linecap='round'/>"+
		"<text x='100' y='88' text-anchor='middle' class='gauge-v' fill='%s'>%d</text>"+
		"<text x='100' y='106' text-anchor='middle' class='gauge-s'>из 100</text></svg>",
		cx-r, cy, r, r, cx+r, cy, sw,
		x0, y0, r, r, x1, y1, color, sw, color, score)
}

// ---------- hbars ----------

type hbarItem struct {
	Label string
	Val   float64
	Tip   string
}

func htmlHbars(items []hbarItem, unit string, fmtFn func(float64) string,
	colorFn func(i int, it hbarItem) string) string {
	if len(items) == 0 {
		return "<div class='nodata'>Нет данных</div>"
	}
	mx := maxOf(valsOf(items))
	if mx == 0 {
		mx = 1
	}
	if fmtFn == nil {
		fmtFn = func(v float64) string { return fnum(v, 1) }
	}
	var rows strings.Builder
	for i, it := range items {
		pct := 100.0 * it.Val / mx
		if pct < 1.5 {
			pct = 1.5
		}
		color := paletteOK[i%len(paletteOK)]
		if colorFn != nil {
			color = colorFn(i, it)
		}
		tip := it.Tip
		if tip == "" {
			tip = it.Label
		}
		fmt.Fprintf(&rows, "<div class='hbar-row'><div class='hbar-l' title='%s'>%s</div>"+
			"<div class='hbar-t'><div class='hbar' style='width:%.1f%%;background:%s'>"+
			"</div></div><div class='hbar-v'>%s</div></div>",
			escQ(tip), esc(it.Label), pct, color, fmtFn(it.Val))
	}
	legend := ""
	if unit != "" {
		legend = "<div class='muted'>Единицы: " + esc(unit) + "</div>"
	}
	return "<div class='hbars'>" + rows.String() + "</div><div class='hbar-legend'>" +
		legend + "</div>"
}

func valsOf(items []hbarItem) []float64 {
	out := make([]float64, len(items))
	for i, it := range items {
		out[i] = it.Val
	}
	return out
}
