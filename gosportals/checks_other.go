//go:build !windows

package main

import (
	"os/exec"
	"runtime"
	"strings"
)

// runEnvironmentChecks — на не-Windows системах полная диагностика недоступна:
// криптосредства (AVEST CSP и др.) распространяются под Windows.
// Возвращаем справочные проверки: браузеры, ОС, сетевой стек.
func runEnvironmentChecks() []Check {
	checks := []Check{{
		ID:     "os",
		Title:  "Операционная система",
		Status: "info",
		Detail: runtime.GOOS + "/" + runtime.GOARCH + ". Полноценная работа с ЭЦП (AVEST CSP, сертификаты НУЦ/ГосСУОК) поддерживается под Windows.",
	}}

	var browsers []string
	for _, b := range []struct{ bin, name string }{
		{"google-chrome", "Google Chrome"}, {"chromium", "Chromium"},
		{"firefox", "Mozilla Firefox"}, {"microsoft-edge", "Microsoft Edge"},
		{"yandex-browser", "Яндекс Браузер"},
	} {
		if _, err := exec.LookPath(b.bin); err == nil {
			browsers = append(browsers, b.name)
		}
	}
	c := Check{ID: "browser", Title: "Доступные браузеры"}
	if len(browsers) > 0 {
		c.Status = "ok"
		c.Detail = "Найдены: " + strings.Join(browsers, ", ")
	} else {
		c.Status = "warn"
		c.Detail = "Браузер не найден в PATH — порталы придётся открывать вручную."
	}
	checks = append(checks, c, Check{
		ID:       "avest",
		Title:    "Криптопровайдер AVEST CSP",
		Status:   "warn",
		Detail:   "Проверка доступна только в Windows-версии приложения. Нужен для входа по ЭЦП (МНС, ЭСЧФ, ФСЗН, Минфин, Белгосстрах, ЕГР).",
		FixLabel: "avest.by",
		FixURL:   "https://www.avest.by/crypto/csp.htm",
	}, Check{
		ID:       "ntclient",
		Title:    "NTClientSoftware (Е-ПАСЛУГА / ЕПЭУ)",
		Status:   "warn",
		Detail:   "Проверка доступна только в Windows-версии. Нужен для входа по ЭЦП/ID-карте на Единый портал электронных услуг и Е-ПАСЛУГА.",
		FixLabel: "nces.by",
		FixURL:   "https://nces.by/service/po/",
	}, Check{
		ID:       "certs",
		Title:    "Корневые сертификаты НУЦ / ГосСУОК",
		Status:   "warn",
		Detail:   "Проверка доступна только в Windows-версии. Сертификаты скачиваются с nca.by и gosuok.gov.by.",
		FixLabel: "nca.by",
		FixURL:   "https://nca.by",
	})
	return checks
}
