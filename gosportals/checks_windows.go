//go:build windows

package main

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"time"
)

// runEnvironmentChecks — диагностика "настроек для входа" на Windows:
// криптопровайдер AVEST CSP, AvTuneProxy, корневые сертификаты НУЦ/ГосСУОК,
// браузер по умолчанию, наличие Edge (для режима IE), версия ОС.
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

// Поиск программы в ветках Uninstall (x64, x86, HKCU).
func installedLike(keyword string) (string, bool) {
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
		for _, line := range strings.Split(out, "\n") {
			line = strings.TrimSpace(line)
			if strings.Contains(line, "DisplayName") && strings.Contains(strings.ToLower(line), kw) {
				// Формат: "    DisplayName    REG_SZ    AVEST CSP x64"
				parts := strings.Split(line, "REG_SZ")
				name := line
				if len(parts) == 2 {
					name = strings.TrimSpace(parts[1])
				} else if idx := strings.Index(line, "DisplayName"); idx >= 0 {
					name = strings.TrimSpace(line[idx+len("DisplayName"):])
					name = strings.TrimLeft(name, " REG_SZ")
				}
				return name, true
			}
		}
	}
	return "", false
}

func checkAvest() Check {
	c := Check{
		ID: "avest", Title: "Криптопровайдер AVEST CSP",
		FixLabel: "Скачать с avest.by", FixURL: "https://avest.by",
	}
	if name, ok := installedLike("avest"); ok {
		c.Status = "ok"
		c.Detail = "Установлен: " + name + ". Требуется для входа по ЭЦП в МНС, Минфин, ФСЗН, Белгосстрах и др."
	} else {
		c.Status = "fail"
		c.Detail = "Не обнаружен в реестре установленных программ. Без AVEST CSP вход по ЭЦП на большинство госпорталов невозможен."
	}
	return c
}

func checkAvTune() Check {
	c := Check{
		ID: "avtun", Title: "AvTuneProxy (вход «без ЭЦП»)",
		FixLabel: "Скачать с avtunproxy.by", FixURL: "https://avtunproxy.by/windows/",
	}
	if name, ok := installedLike("avtunproxy"); ok {
		c.Status = "ok"
		c.Detail = "Установлен: " + name + ". Нужен для входа на Корпоративный портал ФСЗН по МСИ (без ЭЦП)."
	} else {
		c.Status = "warn"
		c.Detail = "Не найден. Требуется только если вы входите в ФСЗН «без ЭЦП» (по логину МСИ)."
	}
	return c
}

func checkCerts() Check {
	c := Check{
		ID: "certs", Title: "Корневые сертификаты НУЦ / ГосСУОК",
		FixLabel: "Сертификаты: nca.by / gosuok.gov.by", FixURL: "https://nca.by",
	}
	// certutil читает хранилище доверенных корневых. Права администратора не нужны.
	out := runCmd(15*time.Second, "certutil", "-store", "-enterprise", "Root")
	if out == "" {
		out = runCmd(15*time.Second, "certutil", "-store", "Root")
	}
	low := strings.ToLower(out)
	var got []string
	if strings.Contains(low, "nca") || strings.Contains(strings.ToLower(out), "национальн") {
		got = append(got, "НУЦ РБ")
	}
	if strings.Contains(low, "gosuok") || strings.Contains(out, "ГосСУОК") || strings.Contains(out, "ГОССУОК") {
		got = append(got, "ГосСУОК")
	}
	if out == "" {
		c.Status = "warn"
		c.Detail = "certutil недоступен или хранилище не прочитано — проверьте сертификаты вручную (certmgr.msc → «Доверенные корневые»)."
	} else if len(got) > 0 {
		c.Status = "ok"
		c.Detail = "В доверенных корневых найдены: " + strings.Join(got, ", ") + "."
	} else {
		c.Status = "fail"
		c.Detail = "Корневые сертификаты НУЦ/ГосСУОК не найдены — госпорталы могут выдавать ошибку «сертификат не доверенный». Скачайте актуальные сертификаты с nca.by и gosuok.gov.by."
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
	c := Check{ID: "edge", Title: "Microsoft Edge (режим Internet Explorer)"}
	paths := []string{
		`C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe`,
		`C:\Program Files\Microsoft\Edge\Application\msedge.exe`,
	}
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			c.Status = "ok"
			c.Detail = "Edge установлен. Для порталов МНС и Белстата включите режим IE: Настройки → «Браузер по умолчанию» → «Страницы в режиме Internet Explorer» → добавьте адрес портала."
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
