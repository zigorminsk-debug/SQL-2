//go:build windows

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// runEnvironmentChecks — диагностика "настроек для входа" на Windows:
// криптопровайдер AVEST CSP, AvTuneProxy, корневые сертификаты НУЦ/ГосСУОК,
// браузер по умолчанию, наличие Edge (для режима IE), версия ОС.
// Вызывается при старте приложения.
func runEnvironmentChecks() []Check {
	checks := []Check{
		checkOS(),
		checkDefaultBrowser(),
		checkEdge(),
		checkAvest(),
		checkAvTune(),
		checkCerts(),
	}
	return checks
}

func runCmd(timeout time.Duration, name string, args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return ""
	}
	return string(out)
}

func regQuery(path, value string) string {
	if value == "" {
		return runCmd(8*time.Second, "reg", "query", path)
	}
	return runCmd(8*time.Second, "reg", "query", path, "/v", value)
}

type installedApp struct{ name, version string }

var reVer = regexp.MustCompile(`\d+(?:\.\d+)+`)

// Поиск программы в ветках Uninstall (x64, x86, HKCU): название и версия.
func installedLike(keyword string) (installedApp, bool) {
	kw := strings.ToLower(keyword)
	roots := []string{
		`HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall`,
		`HKLM\SOFTWARE\WOW6432Node\Microsoft\Windows\CurrentVersion\Uninstall`,
		`HKCU\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall`,
	}
	for _, root := range roots {
		out := runCmd(12*time.Second, "reg", "query", root, "/s")
		if out == "" {
			continue
		}
		// Разбиваем на блоки по ключам реестра.
		for _, chunk := range strings.Split(out, "HKEY_") {
			var app installedApp
			for _, line := range strings.Split(chunk, "\n") {
				line = strings.TrimSpace(line)
				if strings.HasPrefix(line, "DisplayName") {
					app.name = regSzValue(line)
				}
				if strings.HasPrefix(line, "DisplayVersion") {
					app.version = regSzValue(line)
				}
			}
			if app.name == "" || !strings.Contains(strings.ToLower(app.name), kw) {
				continue
			}
			if app.version == "" {
				app.version = reVer.FindString(app.name)
			}
			return app, true
		}
	}
	return installedApp{}, false
}

func regSzValue(line string) string {
	if parts := strings.Split(line, "REG_SZ"); len(parts) == 2 {
		return strings.TrimSpace(parts[1])
	}
	return ""
}

func checkAvest() Check {
	c := Check{
		ID: "avest", Title: "Криптопровайдер AVEST CSP",
		FixLabel: "Скачать актуальный с avest.by", FixURL: "https://avest.by",
	}
	if app, ok := installedLike("avest"); ok {
		c.Status = "ok"
		v := ""
		if app.version != "" {
			v = ", версия " + app.version
		}
		c.Detail = "Установлен: " + app.name + v + ". Нужен для входа по ЭЦП (МНС, ФСЗН, Минфин, Белгосстрах). Обновления — на avest.by."
	} else {
		c.Status = "fail"
		c.Detail = "Не обнаружен в реестре установленных программ. Без AVEST CSP вход по ЭЦП на большинство госпорталов невозможен — скачайте и установите."
	}
	return c
}

func checkAvTune() Check {
	c := Check{
		ID: "avtun", Title: "AvTuneProxy (вход «без ЭЦП» в ФСЗН)",
		FixLabel: "Скачать с avtunproxy.by", FixURL: "https://avtunproxy.by/windows/",
	}
	if app, ok := installedLike("avtunproxy"); ok {
		c.Status = "ok"
		v := ""
		if app.version != "" {
			v = ", версия " + app.version
		}
		c.Detail = "Установлен: " + app.name + v + ". Нужен для входа на Корпоративный портал ФСЗН по МСИ (без ЭЦП)."
	} else {
		c.Status = "warn"
		c.Detail = "Не найден. Требуется только если вы входите в ФСЗН «без ЭЦП» (по логину МСИ)."
	}
	return c
}

// reNotAfter ловит дату NotAfter из вывода certutil (dd.mm.yyyy / mm/dd/yyyy / dd/mm/yyyy).
var reNotAfter = regexp.MustCompile(`NotAfter\s*:?\s*([0-9]{1,2})[./]([0-9]{1,2})[./]([0-9]{4})`)

// certBlock — один сертификат из certutil -store Root.
func certExpiry(block string) (time.Time, bool) {
	m := reNotAfter.FindStringSubmatch(block)
	if m == nil {
		return time.Time{}, false
	}
	n1, _ := strconv.Atoi(m[1])
	n2, _ := strconv.Atoi(m[2])
	year, _ := strconv.Atoi(m[3])
	// Формат ru — dd.mm.yyyy. Если «месяц» оказался >12 — это en-формат mm/dd/yyyy, меняем местами.
	day, month := n1, n2
	if month > 12 && day <= 12 {
		day, month = month, day
	}
	return time.Date(year, time.Month(clampInt(month, 1, 12)), clampInt(day, 1, 31), 12, 0, 0, 0, time.Local), true
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

type rootCert struct {
	label  string
	found  bool
	exp    time.Time
	hasExp bool
}

func checkCerts() Check {
	c := Check{
		ID: "certs", Title: "Корневые сертификаты НУЦ / ГосСУОК (обновление)",
		FixLabel: "Обновить: nca.by / gosuok.gov.by", FixURL: "https://nca.by",
	}
	out := runCmd(15*time.Second, "certutil", "-store", "Root")
	if out == "" {
		out = runCmd(15*time.Second, "certutil", "-store", "-enterprise", "Root")
	}
	if out == "" {
		c.Status = "warn"
		c.Detail = "certutil недоступен или хранилище не прочитано — проверьте сертификаты вручную (certmgr.msc → «Доверенные корневые»)."
		return c
	}

	certs := []rootCert{{label: "НУЦ РБ"}, {label: "ГосСУОК"}}
	for _, block := range strings.Split(out, "================") {
		low := strings.ToLower(block)
		var which *rootCert
		switch {
		case strings.Contains(low, "национальн") || strings.Contains(low, "nca"):
			which = &certs[0]
		case strings.Contains(low, "gosuok") || strings.Contains(block, "ГосСУОК") || strings.Contains(block, "ГОССУОК"):
			which = &certs[1]
		default:
			continue
		}
		which.found = true
		if !which.hasExp {
			if exp, ok := certExpiry(block); ok {
				which.exp, which.hasExp = exp, true
			}
		}
	}

	var problems, parts []string
	worst := "ok"
	bump := func(s string) {
		if s == "fail" || (s == "warn" && worst == "ok") {
			worst = s
		}
	}
	for i := range certs {
		rc := certs[i]
		switch {
		case !rc.found:
			problems = append(problems, rc.label)
			parts = append(parts, rc.label+": не найден")
			bump("fail")
		case rc.hasExp:
			days := int(time.Until(rc.exp).Hours() / 24)
			switch {
			case days < 0:
				parts = append(parts, fmt.Sprintf("%s: просрочен %s", rc.label, rc.exp.Format("02.01.2006")))
				problems = append(problems, rc.label)
				bump("fail")
			case days < 60:
				parts = append(parts, fmt.Sprintf("%s: истекает %s (осталось %d дн.) — пора обновить", rc.label, rc.exp.Format("02.01.2006"), days))
				bump("warn")
			default:
				parts = append(parts, fmt.Sprintf("%s: действует до %s", rc.label, rc.exp.Format("02.01.2006")))
			}
		default:
			parts = append(parts, rc.label+": найден (срок не прочитан)")
		}
	}

	c.Status = worst
	c.Detail = strings.Join(parts, "; ") + "."
	if len(problems) > 0 {
		c.Detail += " Без актуальных корневых сертификатов порталы выдают «сертификат не доверенный»."
	}
	if worst == "ok" {
		c.Detail += " Обновляйте при смене выпусков на nca.by и gosuok.gov.by."
	}
	return c
}

func checkDefaultBrowser() Check {
	c := Check{ID: "browser", Title: "Браузер по умолчанию"}
	out := regQuery(`HKCU\SOFTWARE\Microsoft\Windows\Shell\Associations\UrlAssociations\http\UserChoice`, "ProgId")
	prog := ""
	if idx := strings.Index(out, "ProgId"); idx >= 0 {
		line := strings.TrimSpace(out[idx:])
		parts := strings.Fields(line)
		if len(parts) > 0 {
			prog = parts[len(parts)-1]
		}
	}
	names := map[string]string{
		"ChromeHTML": "Google Chrome", "MSEdgeHTM": "Microsoft Edge",
		"FirefoxURL": "Mozilla Firefox", "IE.HTTP": "Internet Explorer",
	}
	friendly := prog
	for k, v := range names {
		if strings.Contains(prog, k) {
			friendly = v
			break
		}
	}
	if prog == "" {
		c.Status = "warn"
		c.Detail = "Не удалось определить браузер по умолчанию."
	} else if strings.Contains(prog, "IE.") {
		c.Status = "warn"
		c.Detail = "По умолчанию стоит Internet Explorer — современные порталы лучше открывать в Edge (режим IE можно включить в настройках Edge)."
	} else {
		c.Status = "ok"
		c.Detail = "Браузер по умолчанию: " + friendly + ". Порталы из панели будут открываться в нём."
	}
	return c
}

func checkEdge() Check {
	c := Check{ID: "edge", Title: "Microsoft Edge (окно приложения и режим IE)"}
	paths := []string{
		`C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe`,
		`C:\Program Files\Microsoft\Edge\Application\msedge.exe`,
	}
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			c.Status = "ok"
			c.Detail = "Edge установлен. Панель запускается отдельным окном приложения; для порталов МНС и Белстата включите режим IE: Настройки → «Браузер по умолчанию» → «Страницы в режиме Internet Explorer»."
			return c
		}
	}
	c.Status = "warn"
	c.Detail = "Edge не найден в стандартных путях. Режим IE нужен для сервисов с физической ЭЦП (МНС) и «Электронного респондента» Белстата."
	return c
}

func checkOS() Check {
	out := regQuery(`HKLM\SOFTWARE\Microsoft\Windows NT\CurrentVersion`, "ProductName")
	name := "Windows"
	if idx := strings.Index(out, "ProductName"); idx >= 0 {
		line := strings.TrimSpace(out[idx:])
		if parts := strings.Split(line, "REG_SZ"); len(parts) == 2 {
			name = strings.TrimSpace(parts[1])
		}
	}
	return Check{ID: "os", Title: "Операционная система", Status: "info", Detail: name}
}
