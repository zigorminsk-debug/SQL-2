// webui.go — встроенный веб-интерфейс (замена tkinter-GUI в Go-порте).
package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// App — общее состояние приложения (GUI и CLI).
type App struct {
	mu        sync.Mutex
	cfg       *Config
	log       *Logger
	db        Querier
	store     *Store
	monitor   *MonitorEngine
	monitorOn bool

	reportMu   sync.Mutex
	reportRun  bool
	reportDone time.Time
	reportPath string
	reportMsg  string
	reportErr  string

	exitCh chan struct{}
	once   sync.Once
}

func NewApp(cfg *Config, lg *Logger) *App {
	return &App{cfg: cfg, log: lg, exitCh: make(chan struct{})}
}

func (a *App) Store() *Store {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.store == nil {
		st, err := NewStore(DataDir())
		if err != nil {
			return nil
		}
		a.store = st
	}
	return a.store
}

// Connect — установить соединение (или демо).
func (a *App) Connect(db Querier) {
	a.mu.Lock()
	old := a.db
	a.db = db
	a.mu.Unlock()
	if old != nil {
		old.Close()
	}
}

func (a *App) Connected() Querier {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.db
}

// ===================== API =====================

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, map[string]any{"ok": false, "error": msg})
}

func (a *App) handleState(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	out := map[string]any{
		"version":    Version,
		"connected":  a.db != nil,
		"demo":       isDemo(a.db),
		"server":     "",
		"driver":     "",
		"monitor_on": a.monitor != nil && a.monitor.Alive(),
		"monitor_n":  0,
		"config":     a.cfg,
	}
	if a.db != nil {
		out["server"] = a.db.ServerName()
		out["driver"] = a.db.DriverName()
	}
	if a.monitor != nil {
		a.monitor.mu.Lock()
		out["monitor_n"] = a.monitor.Samples
		out["monitor_errors"] = a.monitor.Errors
		a.monitor.mu.Unlock()
	}
	a.reportMu.Lock()
	out["report_running"] = a.reportRun
	out["report_path"] = a.reportPath
	out["report_msg"] = a.reportMsg
	out["report_error"] = a.reportErr
	a.reportMu.Unlock()
	a.mu.Unlock()
	if store := a.Store(); store != nil {
		n, mn, mx := store.SampleCount()
		if n > 0 {
			out["samples_total"] = n
			out["samples_from"] = unixFmt(mn, "02.01.2006 15:04")
			out["samples_to"] = unixFmt(mx, "02.01.2006 15:04")
		}
	}
	writeJSON(w, out)
}

func isDemo(q Querier) bool {
	_, ok := q.(*DemoDatabase)
	return ok
}

type connectRequest struct {
	Server   string `json:"server"`
	Port     int    `json:"port"`
	Database string `json:"database"`
	Auth     string `json:"auth"`
	User     string `json:"user"`
	Password string `json:"password"`
	Encrypt  string `json:"encrypt"`
	Demo     bool   `json:"demo"`
}

func (a *App) handleConnect(w http.ResponseWriter, r *http.Request) {
	var req connectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "некорректный запрос")
		return
	}
	if req.Demo {
		db := NewDemoDatabase()
		a.Connect(db)
		a.log.Infof("Подключён демо-режим: %s", db.ServerName())
		writeJSON(w, map[string]any{"ok": true, "server": db.ServerName(), "demo": true})
		return
	}
	if strings.TrimSpace(req.Server) == "" {
		writeErr(w, 400, "укажите сервер")
		return
	}
	cfg := *a.cfg
	cfg.Server = strings.TrimSpace(req.Server)
	cfg.Port = req.Port
	if cfg.Port == 0 {
		cfg.Port = 1433
	}
	cfg.Database = strings.TrimSpace(req.Database)
	cfg.WindowsAuth = req.Auth != "sql"
	if req.Auth == "" && req.User == "" {
		cfg.WindowsAuth = true
	}
	cfg.User = strings.TrimSpace(req.User)
	if req.Password != "" {
		cfg.PasswordB64 = EncodePwd(req.Password)
	}
	encrypt := req.Encrypt
	if encrypt == "" {
		encrypt = "mandatory"
	}
	db := NewDatabase(cfg.Server, cfg.Database, cfg.WindowsAuth, cfg.User,
		req.Password, cfg.Port, encrypt != "disable", true)
	if err := db.Connect(); err != nil {
		a.log.Infof("Ошибка подключения к %s: %v", cfg.Server, err)
		writeErr(w, 502, "Не удалось подключиться: "+err.Error())
		return
	}
	if _, err := db.Test(); err != nil {
		db.Close()
		a.log.Infof("Тестовый запрос к %s не прошёл: %v", cfg.Server, err)
		writeErr(w, 502, "Подключение установлено, но тестовый запрос не прошёл: "+
			err.Error())
		return
	}
	*a.cfg = cfg
	a.cfg.Save()
	a.Connect(db)
	a.log.Infof("Подключено: %s (база: %s)", db.ServerName(), cfg.Database)
	writeJSON(w, map[string]any{"ok": true, "server": db.ServerName(), "demo": false})
}

func (a *App) handleDisconnect(w http.ResponseWriter, r *http.Request) {
	a.StopMonitor(false)
	a.Connect(nil)
	a.log.Infof("Отключено от сервера")
	writeJSON(w, map[string]any{"ok": true})
}

func (a *App) handleSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method == "POST" {
		var in struct {
			IntervalSec  int     `json:"interval_sec"`
			DurationH    float64 `json:"duration_h"`
			LongQuerySec float64 `json:"long_query_sec"`
			AutoReport   *bool   `json:"auto_report"`
			FragCheck    *bool   `json:"frag_check"`
			ErrorLog     *bool   `json:"errorlog_check"`
			PlanXML      *bool   `json:"plan_xml_check"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeErr(w, 400, "некорректный запрос")
			return
		}
		a.mu.Lock()
		if in.IntervalSec >= 5 {
			a.cfg.Interval = in.IntervalSec
		}
		if in.DurationH > 0 {
			a.cfg.DurationHours = int(in.DurationH)
		}
		if in.LongQuerySec >= 1 {
			a.cfg.LongQueryS = in.LongQuerySec
		}
		if in.AutoReport != nil {
			a.cfg.AutoReport = *in.AutoReport
		}
		if in.FragCheck != nil {
			a.cfg.FragCheck = *in.FragCheck
		}
		if in.ErrorLog != nil {
			a.cfg.ErrorLogCheck = *in.ErrorLog
		}
		if in.PlanXML != nil {
			a.cfg.PlanXMLCheck = *in.PlanXML
		}
		a.mu.Unlock()
		a.cfg.Save()
		a.log.Infof("Настройки сохранены: интервал %d с, длительность %d ч",
			a.cfg.Interval, a.cfg.DurationHours)
	}
	writeJSON(w, a.cfg)
}

func (a *App) handleMonitorStart(w http.ResponseWriter, r *http.Request) {
	db := a.Connected()
	if db == nil {
		writeErr(w, 409, "нет подключения к серверу")
		return
	}
	a.mu.Lock()
	if a.monitor != nil && a.monitor.Alive() {
		a.mu.Unlock()
		writeErr(w, 409, "мониторинг уже запущен")
		return
	}
	a.mu.Unlock()
	st := a.Store()
	if st == nil {
		writeErr(w, 500, "не удалось открыть хранилище данных")
		return
	}
	cfg := a.cfg
	eng := NewMonitorEngine(db, st, time.Duration(cfg.Interval)*time.Second,
		time.Duration(cfg.DurationHours)*time.Hour, cfg.LongQueryS)
	eng.OnLog = func(msg string) { a.log.Infof("%s", msg) }
	eng.OnSample = func(sm Sample) {}
	eng.OnDone = func(e *MonitorEngine) {
		a.mu.Lock()
		a.monitorOn = false
		auto := a.cfg.AutoReport
		a.mu.Unlock()
		a.log.Infof("Мониторинг завершён. Тиков: %d, ошибок опроса: %d",
			e.Samples, e.Errors)
		if auto {
			go a.runReport("Автоотчёт по завершении мониторинга")
		}
	}
	a.mu.Lock()
	a.monitor = eng
	a.monitorOn = true
	a.mu.Unlock()
	eng.Start()
	writeJSON(w, map[string]any{"ok": true})
}

func (a *App) handleMonitorStop(w http.ResponseWriter, r *http.Request) {
	a.StopMonitor(true)
	writeJSON(w, map[string]any{"ok": true})
}

func (a *App) StopMonitor(logIt bool) {
	a.mu.Lock()
	eng := a.monitor
	a.monitor = nil
	a.mu.Unlock()
	if eng != nil && eng.Alive() {
		eng.Stop()
		if logIt {
			a.log.Infof("Мониторинг остановлен пользователем")
		}
	}
}

func (a *App) handleLog(w http.ResponseWriter, r *http.Request) {
	var after int64
	fmt.Sscanf(r.URL.Query().Get("after"), "%d", &after)
	writeJSON(w, a.log.Since(after))
}

func (a *App) handleSamples(w http.ResponseWriter, r *http.Request) {
	st := a.Store()
	if st == nil {
		writeErr(w, 500, "хранилище недоступно")
		return
	}
	var t0, t1 int64
	fmt.Sscanf(r.URL.Query().Get("t0"), "%d", &t0)
	fmt.Sscanf(r.URL.Query().Get("t1"), "%d", &t1)
	samples, _ := st.LoadSamples(t0, t1)
	if len(samples) > 2000 {
		samples = samples[len(samples)-2000:]
	}
	writeJSON(w, map[string]any{"samples": samples})
}

// runReport — фоновое выполнение глубокой диагностики + генерация отчёта.
func (a *App) runReport(reason string) {
	a.reportMu.Lock()
	if a.reportRun {
		a.reportMu.Unlock()
		return
	}
	a.reportRun = true
	a.reportErr = ""
	a.reportMsg = reason
	a.reportMu.Unlock()

	go func() {
		defer func() {
			a.reportMu.Lock()
			a.reportRun = false
			a.reportDone = time.Now()
			a.reportMu.Unlock()
		}()
		db := a.Connected()
		if db == nil {
			a.reportMu.Lock()
			a.reportErr = "нет подключения к серверу"
			a.reportMu.Unlock()
			return
		}
		a.log.Infof("Начата глубокая диагностика (%s)…", reason)
		a.mu.Lock()
		opts := CheckOptions{Fragmentation: a.cfg.FragCheck,
			ErrorLog: a.cfg.ErrorLogCheck, PlanXML: a.cfg.PlanXMLCheck}
		a.mu.Unlock()
		t := time.Now()
		stx := a.Store()
		var preBlocking []BlockingEvent
		var preLong []LongQuery
		var samples []Sample
		var blocking []BlockingEvent
		var longq []LongQuery
		if stx != nil {
			preBlocking, _ = stx.LoadBlocking(0, 0)
			preLong, _ = stx.LoadLong(0, 0)
		}
		deep := collectDeep(db, opts, func(stage string) {
			a.reportMu.Lock()
			a.reportMsg = "Сбор данных: " + stage
			a.reportMu.Unlock()
		}, preBlocking, preLong)
		if stx != nil {
			samples, _ = stx.LoadSamples(0, 0)
			blocking, _ = stx.LoadBlocking(0, 0)
			longq, _ = stx.LoadLong(0, 0)
		}
		meta := ReportMeta{
			ServerDisplay: db.ServerName(),
			ServerFull:    db.ServerName(),
			GeneratedStr:  time.Now().Format("02.01.2006 15:04:05"),
			Interval:      a.cfg.Interval,
			Driver:        db.DriverName(),
		}
		html, health, findings, _ := BuildReport(deep, samples, blocking, longq, meta)
		path := saveReport(html)
		fname := filepath.Base(path)
		a.log.Infof("Отчёт готов: %s (находок: %d, оценка: %d/100, за %.1f с)",
			fname, len(findings), health.Score, time.Since(t).Seconds())
		a.reportMu.Lock()
		a.reportPath = "/reports/" + fname
		a.reportMsg = fmt.Sprintf("Готово: %d проблем (критично: %d, важно: %d, "+
			"рекомендации: %d), оценка %d/100", len(findings), health.Crit,
			health.Warn, health.Info, health.Score)
		a.reportMu.Unlock()
	}()
}

func (a *App) handleReportStart(w http.ResponseWriter, r *http.Request) {
	if a.Connected() == nil {
		writeErr(w, 409, "нет подключения к серверу")
		return
	}
	a.reportMu.Lock()
	running := a.reportRun
	a.reportMu.Unlock()
	if running {
		writeErr(w, 409, "отчёт уже формируется")
		return
	}
	go a.runReport("по запросу пользователя")
	writeJSON(w, map[string]any{"ok": true})
}

func (a *App) handleReportStatus(w http.ResponseWriter, r *http.Request) {
	a.reportMu.Lock()
	defer a.reportMu.Unlock()
	writeJSON(w, map[string]any{
		"running": a.reportRun, "message": a.reportMsg,
		"path": a.reportPath, "error": a.reportErr,
	})
}

// handleDemoReport — сформировать демо-отчёт (без сервера).
func (a *App) handleDemoReport(w http.ResponseWriter, r *http.Request) {
	go func() {
		a.reportMu.Lock()
		if a.reportRun {
			a.reportMu.Unlock()
			return
		}
		a.reportRun = true
		a.reportErr = ""
		a.reportMsg = "Формирование демо-отчёта…"
		a.reportMu.Unlock()
		defer func() {
			a.reportMu.Lock()
			a.reportRun = false
			a.reportMu.Unlock()
		}()
		path, health, findings := a.buildDemoReport()
		a.log.Infof("Демо-отчёт готов: %s (находок: %d, оценка: %d/100)",
			filepath.Base(path), len(findings), health.Score)
		a.reportMu.Lock()
		a.reportPath = "/reports/" + filepath.Base(path)
		a.reportMsg = fmt.Sprintf("Демо-отчёт готов: %d проблем, оценка %d/100",
			len(findings), health.Score)
		a.reportMu.Unlock()
	}()
	writeJSON(w, map[string]any{"ok": true})
}

func (a *App) buildDemoReport() (string, Health, []Finding) {
	db := NewDemoDatabase()
	opts := CheckOptions{Fragmentation: true, ErrorLog: true, PlanXML: true}
	deep := collectDeep(db, opts, nil, nil, nil)
	samples := generateDemoSamples(60, 24)
	blocking, longq := generateDemoEvents(samples)
	meta := ReportMeta{
		ServerDisplay: db.ServerName(),
		ServerFull:    db.ServerName(),
		GeneratedStr:  time.Now().Format("02.01.2006 15:04:05"),
		Interval:      60,
		Driver:        db.DriverName(),
	}
	html, health, findings, _ := BuildReport(deep, samples, blocking, longq, meta)
	os.MkdirAll(reportsDir(), 0o755)
	path := filepath.Join(reportsDir(), "demo_report.html")
	os.WriteFile(path, []byte(html), 0o644)
	return path, health, findings
}

func (a *App) handleReportsList(w http.ResponseWriter, r *http.Request) {
	dir := reportsDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		writeJSON(w, map[string]any{"reports": []any{}})
		return
	}
	type rep struct {
		Name string `json:"name"`
		URL  string `json:"url"`
		Size int64  `json:"size"`
	}
	var out []rep
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".html") {
			continue
		}
		info, _ := e.Info()
		out = append(out, rep{Name: e.Name(), URL: "/reports/" + e.Name(),
			Size: info.Size()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name > out[j].Name })
	writeJSON(w, map[string]any{"reports": out})
}

func (a *App) handleExit(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"ok": true})
	a.StopMonitor(false)
	go func() {
		time.Sleep(300 * time.Millisecond)
		a.once.Do(func() { close(a.exitCh) })
	}()
}

// StartWebServer — HTTP-сервер встроенного GUI. Возвращает порт.
func (a *App) StartWebServer(host string, port int) (int, error) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(indexHTML))
	})
	mux.HandleFunc("/api/state", a.handleState)
	mux.HandleFunc("/api/connect", a.handleConnect)
	mux.HandleFunc("/api/disconnect", a.handleDisconnect)
	mux.HandleFunc("/api/settings", a.handleSettings)
	mux.HandleFunc("/api/monitor/start", a.handleMonitorStart)
	mux.HandleFunc("/api/monitor/stop", a.handleMonitorStop)
	mux.HandleFunc("/api/log", a.handleLog)
	mux.HandleFunc("/api/samples", a.handleSamples)
	mux.HandleFunc("/api/report/start", a.handleReportStart)
	mux.HandleFunc("/api/report/status", a.handleReportStatus)
	mux.HandleFunc("/api/demo-report", a.handleDemoReport)
	mux.HandleFunc("/api/reports", a.handleReportsList)
	mux.HandleFunc("/api/exit", a.handleExit)
	fs := http.FileServer(http.Dir(reportsDir()))
	mux.Handle("/reports/", http.StripPrefix("/reports/", neuterDir(fs)))

	addr := fmt.Sprintf("%s:%d", host, port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return 0, err
	}
	go http.Serve(ln, mux)
	return ln.Addr().(*net.TCPAddr).Port, nil
}

// neuterDir — запрет листинга каталогов (отдаём только файлы).
func neuterDir(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/") || strings.Contains(r.URL.Path, "..") {
			http.NotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Wait — блокирующее ожидание завершения (через /api/exit).
func (a *App) Wait() {
	<-a.exitCh
}
