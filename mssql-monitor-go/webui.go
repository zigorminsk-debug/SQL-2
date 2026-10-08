// webui.go — встроенный веб-интерфейс (замена tkinter-GUI в Go-порте).
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
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

	lifeMu     sync.Mutex
	closing    bool
	work       sync.WaitGroup
	ctx        context.Context
	cancel     context.CancelFunc
	stopOnce   sync.Once
	httpServer *http.Server
	exitCh     chan struct{}
	once       sync.Once
}

func NewApp(cfg *Config, lg *Logger) *App {
	ctx, cancel := context.WithCancel(context.Background())
	return &App{cfg: cfg, log: lg, exitCh: make(chan struct{}), ctx: ctx, cancel: cancel}
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
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
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
		"config":     *a.cfg,
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
	a.mu.Lock()
	cfg := *a.cfg
	a.mu.Unlock()
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
	if err := db.ConnectContext(a.ctx); err != nil {
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
	a.mu.Lock()
	*a.cfg = cfg
	a.cfg.Save()
	a.mu.Unlock()
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
			Workload     string  `json:"workload"`
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
		if in.Workload != "" && !validWorkload(in.Workload) {
			writeErr(w, 400, "неизвестный профиль нагрузки")
			return
		}
		a.mu.Lock()
		if in.Workload != "" {
			a.cfg.Workload = in.Workload
		}
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
		cfg := *a.cfg
		a.mu.Unlock()
		cfg.Save()
		a.log.Infof("Настройки сохранены: интервал %d с, длительность %d ч", cfg.Interval, cfg.DurationHours)
	}
	a.mu.Lock()
	cfg := *a.cfg
	a.mu.Unlock()
	writeJSON(w, cfg)
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
	a.mu.Lock()
	cfg := *a.cfg
	a.mu.Unlock()
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
		if auto && !e.Stopped() {
			a.runReport("Автоотчёт по завершении мониторинга")
		}
	}
	a.mu.Lock()
	if a.monitor != nil && a.monitor.Alive() {
		a.mu.Unlock()
		eng.Stop()
		writeErr(w, 409, "мониторинг уже запущен")
		return
	}
	a.monitor = eng
	a.monitorOn = true
	eng.Start()
	a.mu.Unlock()
	writeJSON(w, map[string]any{"ok": true})
}

func (a *App) handleMonitorStop(w http.ResponseWriter, r *http.Request) {
	a.StopMonitor(true)
	writeJSON(w, map[string]any{"ok": true})
}

func (a *App) StopMonitor(logIt bool) {
	a.mu.Lock()
	eng := a.monitor
	a.mu.Unlock()
	if eng != nil && eng.Alive() {
		eng.Stop()
		eng.Wait()
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
	if !a.beginWork() {
		return
	}
	a.reportMu.Lock()
	if a.reportRun {
		a.reportMu.Unlock()
		a.work.Done()
		return
	}
	a.reportRun = true
	a.reportErr = ""
	a.reportMsg = reason
	a.reportMu.Unlock()

	go func() {
		defer a.work.Done()
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
		interval := a.cfg.Interval
		opts := CheckOptions{Workload: a.cfg.Workload, Fragmentation: a.cfg.FragCheck,
			ErrorLog: a.cfg.ErrorLogCheck, PlanXML: a.cfg.PlanXMLCheck}
		a.mu.Unlock()
		db = scopedQuerier{db, a.ctx}
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
		if a.ctx.Err() != nil {
			return
		}
		if stx != nil {
			samples, _ = stx.LoadSamples(0, 0)
			blocking, _ = stx.LoadBlocking(0, 0)
			longq, _ = stx.LoadLong(0, 0)
		}
		meta := ReportMeta{
			ServerDisplay: db.ServerName(),
			ServerFull:    db.ServerName(),
			GeneratedStr:  time.Now().Format("02.01.2006 15:04:05"),
			Interval:      interval,
			Driver:        db.DriverName(),
		}
		html, health, findings, _ := BuildReport(deep, samples, blocking, longq, meta)
		if a.ctx.Err() != nil {
			return
		}
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
	a.runReport("по запросу пользователя")
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
	if !a.beginWork() {
		writeErr(w, 503, "программа завершается")
		return
	}
	go func() {
		defer a.work.Done()
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
		if a.ctx.Err() != nil {
			return
		}
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
	a.mu.Lock()
	workload := a.cfg.Workload
	a.mu.Unlock()
	opts := CheckOptions{Workload: workload, Fragmentation: true, ErrorLog: true, PlanXML: true}
	deep := collectDeep(scopedQuerier{db, a.ctx}, opts, nil, nil, nil)
	if a.ctx.Err() != nil {
		return "", Health{}, nil
	}
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
	if a.ctx.Err() != nil {
		return "", Health{}, nil
	}
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
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeErr(w, 405, "требуется POST")
		return
	}
	if r.Header.Get("Content-Type") != "application/json" {
		writeErr(w, 415, "требуется application/json")
		return
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		if err != nil || u.Host != r.Host {
			writeErr(w, 403, "чужой Origin")
			return
		}
	}
	a.stopWork()
	writeJSON(w, map[string]any{"ok": true})
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	go a.Shutdown()
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
	mux.HandleFunc("/api/state", a.apiHandler(a.handleState, false))
	mux.HandleFunc("/api/connect", a.apiHandler(a.handleConnect, true))
	mux.HandleFunc("/api/disconnect", a.apiHandler(a.handleDisconnect, true))
	mux.HandleFunc("/api/settings", func(w http.ResponseWriter, r *http.Request) { a.apiHandler(a.handleSettings, r.Method != "GET")(w, r) })
	mux.HandleFunc("/api/monitor/start", a.apiHandler(a.handleMonitorStart, true))
	mux.HandleFunc("/api/monitor/stop", a.apiHandler(a.handleMonitorStop, true))
	mux.HandleFunc("/api/log", a.apiHandler(a.handleLog, false))
	mux.HandleFunc("/api/samples", a.apiHandler(a.handleSamples, false))
	mux.HandleFunc("/api/report/start", a.apiHandler(a.handleReportStart, true))
	mux.HandleFunc("/api/report/status", a.apiHandler(a.handleReportStatus, false))
	mux.HandleFunc("/api/demo-report", a.apiHandler(a.handleDemoReport, true))
	mux.HandleFunc("/api/reports", a.apiHandler(a.handleReportsList, false))
	mux.HandleFunc("/api/exit", a.handleExit)
	fs := http.FileServer(http.Dir(reportsDir()))
	mux.Handle("/reports/", http.StripPrefix("/reports/", neuterDir(fs)))

	addr := fmt.Sprintf("%s:%d", host, port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return 0, err
	}
	a.httpServer = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go a.httpServer.Serve(ln)
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
