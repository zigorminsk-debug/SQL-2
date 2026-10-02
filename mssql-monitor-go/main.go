// main.go — точка входа MS SQL Monitor (Go-порт): CLI, демо и веб-GUI.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

func main() {
	fs := flag.NewFlagSet("MSSQLMonitor", flag.ContinueOnError)
	fCLI := fs.Bool("cli", false, "консольный режим (без GUI)")
	fServer := fs.String("server", "", "сервер SQL Server (host или host\\instance)")
	fPort := fs.Int("port", 1433, "порт TCP (по умолчанию 1433)")
	fDatabase := fs.String("database", "", "база данных по умолчанию")
	fAuth := fs.String("auth", "sql", "аутентификация: sql | windows")
	fUser := fs.String("user", "", "пользователь SQL (для -auth sql)")
	fPassword := fs.String("password", "", "пароль (для -auth sql)")
	fEncrypt := fs.String("encrypt", "mandatory",
		"шифрование: mandatory | strict | optional | disable")
	fHours := fs.Float64("hours", 24, "сколько часов мониторить (синоним -duration)")
	fInterval := fs.Int("interval", 60, "интервал опроса, секунд")
	fLongQuery := fs.Float64("long-query", 30, "порог долгих запросов, секунд")
	fDuration := fs.Float64("duration", 24, "длительность мониторинга, часов")
	fSkipMonitor := fs.Bool("skip-monitor", false, "не запускать мониторинг (только отчёт)")
	fNoFrag := fs.Bool("no-frag", false, "пропустить фрагментацию индексов")
	fNoErrorLog := fs.Bool("no-errorlog", false, "пропустить журнал ошибок")
	fNoPlanXML := fs.Bool("no-plan-xml", false, "пропустить разбор XML планов")
	fDemo := fs.Bool("demo", false, "GUI в демо-режиме (без сервера)")
	fDemoReport := fs.Bool("demo-report", false,
		"сформировать демо-отчёт и выйти (без сервера)")
	fHost := fs.String("host", "127.0.0.1", "адрес веб-GUI")
	fHTTP := fs.Int("http", 0, "порт веб-GUI (0 — выбрать свободный, 8790 по умолчанию)")
	fNoBrowser := fs.Bool("no-browser", false, "не открывать браузер автоматически")
	fVersion := fs.Bool("version", false, "версия программы")
	if err := fs.Parse(os.Args[1:]); err != nil {
		os.Exit(2)
	}

	if *fVersion {
		fmt.Printf("MS SQL Monitor %s (Go), %s/%s\n", Version, runtime.GOOS, runtime.GOARCH)
		return
	}

	// ---- режим: демо-отчёт и выход ----
	if *fDemoReport {
		cfg := defaultConfig()
		lg := NewLogger()
		app := NewApp(&cfg, lg)
		path, health, findings := app.buildDemoReport()
		fmt.Printf("Демо-отчёт: %s\nПроблем: %d (критично: %d, важно: %d, "+
			"рекомендации: %d), оценка: %d/100 (%s)\n",
			path, len(findings), health.Crit, health.Warn, health.Info,
			health.Score, health.Grade)
		return
	}

	// ---- консольный режим ----
	if *fCLI {
		runCLI(fs, fServer, fPort, fDatabase, fAuth, fUser, fPassword, fEncrypt,
			fInterval, fLongQuery, fDuration, fHours, fSkipMonitor, fNoFrag,
			fNoErrorLog, fNoPlanXML)
		return
	}

	// ---- GUI (веб) ----
	cfg := defaultConfig()
	lg := NewLogger()
	app := NewApp(&cfg, lg)
	if *fDemo {
		app.Connect(NewDemoDatabase())
		lg.Infof("Запущен демо-режим: данные синтетические, сервер не используется")
	}
	port := *fHTTP
	if port == 0 {
		port = 8790
	}
	actual, err := app.StartWebServer(bindHost(*fHost, *fDemo), port)
	if err != nil {
		// порт занят — пробуем случайный
		actual, err = app.StartWebServer(bindHost(*fHost, *fDemo), 0)
		if err != nil {
			fmt.Fprintln(os.Stderr, "Не удалось запустить веб-интерфейс:", err)
			os.Exit(1)
		}
	}
	url := fmt.Sprintf("http://%s:%d/", "127.0.0.1", actual)
	fmt.Printf("MS SQL Monitor %s — веб-интерфейс: %s\n", Version, url)
	fmt.Println("Окно можно закрыть: программа завершится через веб-интерфейс " +
		"или Ctrl+C.")
	if !*fNoBrowser {
		openBrowser(url)
	}
	app.Wait()
}

func bindHost(host string, demo bool) string {
	if demo && host == "127.0.0.1" {
		return "127.0.0.1"
	}
	return host
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", "", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	go func() {
		if cmd != nil {
			cmd.Run()
		}
	}()
}

// runCLI — консольный режим: подключение -> (мониторинг) -> отчёт.
func runCLI(fs *flag.FlagSet, fServer *string, fPort *int, fDatabase *string,
	fAuth *string, fUser *string, fPassword *string, fEncrypt *string,
	fInterval *int, fLongQuery *float64, fDuration *float64, fHours *float64,
	fSkipMonitor *bool, fNoFrag *bool, fNoErrorLog *bool, fNoPlanXML *bool) {
	lg := NewLogger()
	cfg := defaultConfig()
	if *fHours != 24 && *fDuration == 24 {
		cfg.DurationHours = int(*fHours)
	} else if *fDuration != 24 {
		cfg.DurationHours = int(*fDuration)
	}
	cfg.Interval = *fInterval
	cfg.LongQueryS = *fLongQuery
	var db Querier
	if *fServer == "" {
		fmt.Fprintln(os.Stderr,
			"Консольный режим: укажите -server host[\\instance] (или используйте -demo-report)")
		os.Exit(2)
	}
	app := NewApp(&cfg, lg)
	cfg.Server = *fServer
	cfg.Port = *fPort
	cfg.Database = *fDatabase
	cfg.WindowsAuth = *fAuth == "windows"
	if *fAuth == "" && *fUser == "" {
		cfg.WindowsAuth = true
	}
	cfg.User = strings.TrimSpace(*fUser)
	if *fPassword != "" {
		cfg.PasswordB64 = EncodePwd(*fPassword)
	}
	real := NewDatabase(cfg.Server, cfg.Database, cfg.WindowsAuth, cfg.User,
		*fPassword, cfg.Port, *fEncrypt != "disable", true)
	fmt.Printf("Подключение к %s", cfg.Server)
	if cfg.Port != 1433 && !containsByte(cfg.Server, '\\') {
		fmt.Printf(":%d", cfg.Port)
	}
	fmt.Println(" …")
	if err := real.Connect(); err != nil {
		fmt.Fprintln(os.Stderr, "Ошибка подключения:", err)
		os.Exit(1)
	}
	if test, err := real.Test(); err != nil {
		fmt.Fprintln(os.Stderr, "Тестовый запрос не прошёл:", err)
		os.Exit(1)
	} else {
		fmt.Println("Подключено:", gs(test, "srv"))
	}
	db = real
	app.Connect(db)
	cfg.Save()

	store := app.Store()
	if store == nil {
		fmt.Fprintln(os.Stderr, "Не удалось открыть хранилище данных:", dataRoot())
		os.Exit(1)
	}

	if !*fSkipMonitor {
		dur := time.Duration(cfg.DurationHours) * time.Hour
		fmt.Printf("Мониторинг: %v (интервал %d с)…\n",
			dur.Round(time.Minute), cfg.Interval)
		done := make(chan struct{})
		eng := NewMonitorEngine(db, store,
			time.Duration(cfg.Interval)*time.Second, dur, cfg.LongQueryS)
		eng.OnLog = func(msg string) { fmt.Println("  ", msg) }
		eng.OnDone = func(e *MonitorEngine) { close(done) }
		eng.Start()
		lastPct := -10
		tick := time.NewTicker(30 * time.Second)
		defer tick.Stop()
	loop:
		for {
			select {
			case <-done:
				break loop
			case <-tick.C:
				pct := int(float64(time.Since(eng.Started)) / float64(dur) * 100)
				samples, _ := eng.Stats()
				if pct >= lastPct+10 && pct < 100 {
					fmt.Printf("  … %d%% (тиков: %d)\n", pct, samples)
					lastPct = pct
				}
			}
		}
		samples, errors := eng.Stats()
		fmt.Printf("Мониторинг завершён: тиков %d, ошибок %d\n", samples, errors)
	}

	fmt.Println("Глубокая диагностика и построение отчёта…")
	opts := CheckOptions{Fragmentation: !*fNoFrag, ErrorLog: !*fNoErrorLog,
		PlanXML: !*fNoPlanXML}
	t0 := time.Now()
	samples, _ := store.LoadSamples(0, 0)
	blocking, _ := store.LoadBlocking(0, 0)
	longq, _ := store.LoadLong(0, 0)
	deep := collectDeep(db, opts, func(stage string) {
		fmt.Println("  -", stage)
	}, blocking, longq)
	meta := ReportMeta{ServerDisplay: db.ServerName(), ServerFull: db.ServerName(),
		GeneratedStr: time.Now().Format("02.01.2006 15:04:05"),
		Interval:     cfg.Interval, Driver: db.DriverName()}
	html, health, findings, _ := BuildReport(deep, samples, blocking, longq, meta)
	path := saveReport(html)
	fmt.Println()
	fmt.Printf("Отчёт сохранён: %s\n", path)
	fmt.Printf("Проблем: %d (критично: %d, важно: %d, рекомендации: %d)\n",
		len(findings), health.Crit, health.Warn, health.Info)
	fmt.Printf("Оценка состояния: %d/100 — %s\n", health.Score, health.Grade)
	if len(deep.Errors) > 0 {
		fmt.Println("Недоступные проверки (права):")
		for k, v := range deep.Errors {
			fmt.Printf("  - %s: %s\n", k, v)
		}
	}
	fmt.Printf("Время диагностики: %.1f с\n", time.Since(t0).Seconds())
}

func containsByte(s string, c byte) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return true
		}
	}
	return false
}
