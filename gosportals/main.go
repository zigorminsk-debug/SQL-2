// GosPortals — панель быстрого доступа к государственным порталам Республики Беларусь.
//
// Однофайловое приложение без установки: поднимает локальную веб-панель,
// открывает её в браузере, показывает порталы, способы входа, чек-листы
// настроек и диагностику окружения (криптопровайдер, сертификаты, браузер).
//
// По мотивам идеи belportal.by. Программа НЕ хранит и НЕ запрашивает
// логины/пароли: аутентификация происходит только на официальных сайтах.
package main

import (
	"context"
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	neturl "net/url"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	appName = "ГосПорталы РБ"
	appVer  = "1.0.0"
)

//go:embed web portals.json
var content embed.FS

// Portal — карточка госпортала.
type Portal struct {
	ID    string   `json:"id"`
	Name  string   `json:"name"`
	Cat   string   `json:"cat"`
	Desc  string   `json:"desc"`
	Site  string   `json:"site"`
	Login string   `json:"login"`
	Auth  []string `json:"auth"`
	Note  string   `json:"note"`
	IE    bool     `json:"ie,omitempty"`
}

// Status — результат проверки доступности портала.
type Status struct {
	OK     bool  `json:"ok"`
	MS     int64 `json:"ms"`
	Code   int   `json:"code,omitempty"`
	WhenMS int64 `json:"when"`
}

// Check — результат диагностики окружения.
type Check struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Status   string `json:"status"` // ok | warn | fail | info
	Detail   string `json:"detail"`
	FixLabel string `json:"fixLabel,omitempty"`
	FixURL   string `json:"fixUrl,omitempty"`
}

var (
	portals   []Portal
	portalIdx = map[string]int{}

	stMu   sync.Mutex
	stData map[string]Status
	stTime time.Time

	httpClient = &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:        32,
			MaxIdleConnsPerHost: 4,
			TLSHandshakeTimeout: 4 * time.Second,
		},
	}
)

// toolsWhitelist — официальные утилиты/страницы настроек, которые приложение
// может открыть в браузере (только фиксированный белый список).
var toolsWhitelist = map[string]string{
	"avest":       "https://avest.by",
	"avtunproxy":  "https://avtunproxy.by/windows/",
	"nca":         "https://nca.by",
	"gosuok":      "https://gosuok.gov.by",
	"msi-reg":     "https://ioauth.raschet.by/client/",
	"esa":         "https://auth.gia.gov.by",
	"portal-help": "https://www.portal.nalog.gov.by",
}

func mustLoadData() {
	raw, err := content.ReadFile("portals.json")
	if err != nil {
		log.Fatalf("portals.json: %v", err)
	}
	if err := json.Unmarshal(raw, &portals); err != nil {
		log.Fatalf("portals.json: %v", err)
	}
	for i := range portals {
		if portals[i].Login == "" {
			portals[i].Login = portals[i].Site
		}
		portalIdx[portals[i].ID] = i
	}
}

func main() {
	addr := flag.String("addr", "127.0.0.1", "адрес прослушивания веб-панели (127.0.0.1 — только этот компьютер)")
	port := flag.Int("port", 0, "порт веб-панели (0 — выбрать свободный автоматически)")
	noOpen := flag.Bool("no-open", false, "не открывать окно/браузер автоматически")
	web := flag.Bool("web", false, "(устарело) то же, что режим по умолчанию")
	appMode := flag.Bool("app", false, "окно без интерфейса браузера (app-режим Edge/Chrome), порталы — во встроенных вкладках")
	native := flag.Bool("native", false, "нативный Win32-GUI (таблица, меню, трей) вместо окна приложения")
	flag.Parse()
	_ = web

	mustLoadData()

	switch {
	case *native:
		// Нативный GUI на walk; при ошибке — падать в режим браузера.
		if err := runGUI(); err != nil {
			fmt.Fprintf(os.Stderr, "нативный GUI недоступен (%v)\nЗапускаю окно браузера…\n", err)
			runServer(*addr, *port, *noOpen, openBrowser, "", true)
		}
	case *appMode:
		// Окно приложения без браузерного интерфейса (Edge/Chrome --app),
		// порталы открываются во встроенных вкладках этого окна.
		runServer(*addr, *port, *noOpen, openAppWindow, "?embed=1", true)
	default:
		// Режим по умолчанию (все ОС): отдельное окно браузера со стартовой
		// страницей; все порталы открываются ВКЛАДКАМИ в этом же окне.
		// Программа завершается, когда окно закрыто (heartbeat).
		runServer(*addr, *port, *noOpen, openBrowser, "", true)
	}
}

// runServer поднимает локальный сервер панели. opener=nil (или noOpen) —
// стандартный браузер; urlSuffix дописывается к URL панели (?embed=1 для
// отдельного окна). killOnClose=true: программа завершается, когда пропадает
// heartbeat главного окна (то есть пользователь его закрыл). Блокируется навсегда.
func runServer(addr string, port int, noOpen bool, opener func(string) error, urlSuffix string, killOnClose bool) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", handleIndex)
	mux.HandleFunc("GET /api/meta", handleMeta)
	mux.HandleFunc("GET /api/portals", handlePortals)
	mux.HandleFunc("GET /api/status", handleStatus)
	mux.HandleFunc("GET /api/checks", handleChecks)
	mux.HandleFunc("/api/heartbeat", handleHeartbeat)
	mux.HandleFunc("GET /api/icon", handleIcon)
	mux.HandleFunc("GET /api/framing", handleFraming)
	mux.HandleFunc("POST /api/open", handleOpen)

	ln, err := net.Listen("tcp", fmt.Sprintf("%s:%d", addr, port))
	if err != nil {
		log.Fatalf("не удалось занять порт: %v", err)
	}

	showURL := ln.Addr().String()
	if strings.HasPrefix(showURL, "0.0.0.0:") || strings.HasPrefix(showURL, ":") {
		showURL = "127.0.0.1" + showURL[strings.LastIndex(showURL, ":"):]
	}
	url := "http://" + showURL + urlSuffix

	fmt.Println("========================================================")
	fmt.Printf("  %s  v%s\n", appName, appVer)
	fmt.Println("  Панель госпорталов Республики Беларусь")
	fmt.Println("========================================================")
	fmt.Printf("  Панель открыта:     %s\n", strings.TrimSuffix(url, "?embed=1"))
	fmt.Println("  Это окно (терминал) НЕ закрывайте, пока работаете.")
	fmt.Println("  Для выхода нажмите  Ctrl+C")
	fmt.Println("========================================================")

	if !noOpen {
		open := opener
		if open == nil {
			open = openBrowser
		}
		go func() {
			time.Sleep(350 * time.Millisecond)
			if err := open(url); err != nil {
				log.Printf("окно не открылось (%v) — откройте вручную: %s", err, url)
				return
			}
			if killOnClose {
				// Окно открылось — консоль больше не нужна.
				time.Sleep(2200 * time.Millisecond)
				hideConsoleWindow()
			}
		}()
	}

	if killOnClose {
		go heartbeatSupervisor(30 * time.Second)
	}

	log.Fatal(http.Serve(ln, mux))
}

func handleIndex(w http.ResponseWriter, r *http.Request) {
	page, err := content.ReadFile("web/index.html")
	if err != nil {
		http.Error(w, "ui not found", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(page)
}

func handleMeta(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{
		"name": appName, "version": appVer, "os": runtime.GOOS, "arch": runtime.GOARCH,
	})
}

func handlePortals(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, portals)
}

// handleStatus — проверка доступности порталов (кэш 90 секунд).
func handleStatus(w http.ResponseWriter, r *http.Request) {
	stMu.Lock()
	if time.Since(stTime) < 90*time.Second && stData != nil {
		out := stData
		stMu.Unlock()
		writeJSON(w, out)
		return
	}
	stMu.Unlock()

	res := map[string]Status{}
	var wg sync.WaitGroup
	var mu sync.Mutex
	for _, p := range portals {
		p := p
		wg.Add(1)
		go func() {
			defer wg.Done()
			s := probe(p.Site)
			mu.Lock()
			res[p.ID] = s
			mu.Unlock()
		}()
	}
	wg.Wait()

	stMu.Lock()
	stData, stTime = res, time.Now()
	stMu.Unlock()
	writeJSON(w, res)
}

func probe(rawurl string) Status {
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawurl, nil)
	if err != nil {
		return Status{OK: false, MS: ms(start), WhenMS: time.Now().UnixMilli()}
	}
	req.Header.Set("User-Agent", "GosPortals/"+appVer)
	resp, err := httpClient.Do(req)
	if err != nil {
		return Status{OK: false, MS: ms(start), WhenMS: time.Now().UnixMilli()}
	}
	defer resp.Body.Close()
	// Доступен — любой осмысленный HTTP-ответ ниже 500 (включая 401/403/redirect-и).
	return Status{OK: resp.StatusCode < 500, MS: ms(start), Code: resp.StatusCode, WhenMS: time.Now().UnixMilli()}
}

func ms(start time.Time) int64 { return time.Since(start).Milliseconds() }

func handleChecks(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, runEnvironmentChecks())
}

// handleOpen — открытие ссылок из приложения в системном браузере.
// Принимает ТОЛЬКО идентификаторы (портала или утилиты), URL с клиента не принимается.
func handleOpen(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID   string `json:"id"`   // id портала
		Key  string `json:"key"`  // site | login
		Tool string `json:"tool"` // ключ из toolsWhitelist
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	var url string
	switch {
	case body.Tool != "":
		u, ok := toolsWhitelist[body.Tool]
		if !ok {
			http.Error(w, "unknown tool", http.StatusBadRequest)
			return
		}
		url = u
	case body.ID != "":
		i, ok := portalIdx[body.ID]
		if !ok {
			http.Error(w, "unknown portal", http.StatusBadRequest)
			return
		}
		p := portals[i]
		if body.Key == "login" {
			url = p.Login
		} else {
			url = p.Site
		}
	default:
		http.Error(w, "id or tool required", http.StatusBadRequest)
		return
	}
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		http.Error(w, "invalid url", http.StatusBadRequest)
		return
	}
	if err := openBrowser(url); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// openBrowser открывает URL в браузере по умолчанию.
// URL приходит только из вшитых данных (порталы/белый список), не от пользователя.
func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		if _, err := exec.LookPath("xdg-open"); err != nil {
			return fmt.Errorf("xdg-open не найден")
		}
		cmd = exec.Command("xdg-open", url)
	}
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Start()
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(v)
}

// ---------- heartbeat: программа живёт, пока открыто главное окно ----------

var (
	hbMu      sync.Mutex
	hbLast    time.Time
	hbStarted bool
)

func handleHeartbeat(w http.ResponseWriter, r *http.Request) {
	hbMu.Lock()
	hbLast, hbStarted = time.Now(), true
	hbMu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

// heartbeatSupervisor: если главное окно закрыли (перестали приходить
// heartbeat-и), приложение завершается само — висящих процессов нет.
func heartbeatSupervisor(timeout time.Duration) {
	t := time.NewTicker(3 * time.Second)
	defer t.Stop()
	for range t.C {
		hbMu.Lock()
		started, last := hbStarted, hbLast
		hbMu.Unlock()
		if started && time.Since(last) > timeout {
			fmt.Println("главное окно закрыто — завершаю работу")
			os.Exit(0)
		}
	}
}

// ---------- родные иконки порталов (favicon) ----------

type iconEntry struct {
	ct   string
	body []byte
	at   time.Time
	fail bool
}

var (
	iconMu    sync.Mutex
	iconCache = map[string]*iconEntry{}
)

// handleIcon отдаёт favicon портала: скачивает с самого портала (приватно,
// без сторонних сервисов) и кеширует на сутки.
func handleIcon(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	i, ok := portalIdx[id]
	if !ok {
		http.NotFound(w, r)
		return
	}
	iconMu.Lock()
	cached := iconCache[id]
	iconMu.Unlock()
	if cached != nil && time.Since(cached.at) < 24*time.Hour {
		if cached.fail {
			http.NotFound(w, r)
			return
		}
		serveIcon(w, cached)
		return
	}
	p := portals[i]
	ct, body := fetchPortalIcon(p.Site)
	e := &iconEntry{ct: ct, body: body, at: time.Now(), fail: body == nil}
	iconMu.Lock()
	iconCache[id] = e
	iconMu.Unlock()
	if e.fail {
		http.NotFound(w, r)
		return
	}
	serveIcon(w, e)
}

func serveIcon(w http.ResponseWriter, e *iconEntry) {
	w.Header().Set("Content-Type", e.ct)
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Write(e.body)
}

var reLinkIcon = regexp.MustCompile(`(?i)<link[^>]+href=["']([^"']+\.(?:ico|png|svg|gif|jpe?g|webp))[^"']*["'][^>]*>`)

// fetchPortalIcon пробует /favicon.ico, затем ищет rel=icon на главной.
func fetchPortalIcon(site string) (string, []byte) {
	u, err := neturl.Parse(site)
	if err != nil || u.Host == "" {
		return "", nil
	}
	if ct, b := tryFetchImage(u.Scheme + "://" + u.Host + "/favicon.ico"); b != nil {
		return ct, b
	}
	// Ищем <link rel="icon"> в HTML главной (только среди img-расширений).
	if _, html := tryFetchText(site, 400*1024); html != "" {
		for _, m := range reLinkIcon.FindAllStringSubmatch(html, 6) {
			href := absURL(site, strings.TrimSpace(m[1]))
			if ct2, b := tryFetchImage(href); b != nil {
				return ct2, b
			}
		}
	}
	return "", nil
}

func tryFetchImage(raw string) (string, []byte) {
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return "", nil
	}
	req.Header.Set("User-Agent", "GosPortals/"+appVer)
	resp, err := httpClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		return "", nil
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 400*1024))
	if err != nil || len(body) < 16 {
		return "", nil
	}
	ct := resp.Header.Get("Content-Type")
	semi := strings.Index(ct, ";")
	if semi > 0 {
		ct = ct[:semi]
	}
	ct = strings.ToLower(strings.TrimSpace(ct))
	// Нормализуем по магическим байтам.
	switch {
	case len(body) >= 4 && body[0] == 0 && body[1] == 0 && body[2] == 1 && body[3] == 0:
		return "image/x-icon", body
	case len(body) >= 8 && string(body[:8]) == "\x89PNG\r\n\x1a\n":
		return "image/png", body
	case len(body) >= 3 && string(body[:3]) == "GIF":
		return "image/gif", body
	case len(body) >= 3 && body[0] == 0xFF && body[1] == 0xD8:
		return "image/jpeg", body
	case strings.Contains(strings.ToLower(string(body[:min(300, len(body))])), "<svg"):
		return "image/svg+xml", body
	}
	if strings.HasPrefix(ct, "image/") {
		return ct, body
	}
	return "", nil
}

func tryFetchText(raw string, limit int64) (string, string) {
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return "", ""
	}
	req.Header.Set("User-Agent", "GosPortals/"+appVer)
	resp, err := httpClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		return "", ""
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	if err != nil {
		return "", ""
	}
	return resp.Header.Get("Content-Type"), string(b)
}

func absURL(base, href string) string {
	if strings.HasPrefix(href, "http://") || strings.HasPrefix(href, "https://") {
		return href
	}
	b, err := neturl.Parse(base)
	if err != nil {
		return href
	}
	r, err := neturl.Parse(href)
	if err != nil {
		return href
	}
	return b.ResolveReference(r).String()
}

// ---------- проверка разрешения на встраивание (iframe-вкладки) ----------

type frameEntry struct {
	blocked bool
	reason  string
	at      time.Time
}

var (
	frameMu    sync.Mutex
	frameCache = map[string]*frameEntry{}
)

// handleFraming сообщает, запрещает ли портал встраивание во вкладку-iframe
// (X-Frame-Options / CSP frame-ancestors). Кеш — 10 минут.
func handleFraming(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	i, ok := portalIdx[id]
	if !ok {
		http.Error(w, "unknown portal", http.StatusBadRequest)
		return
	}
	frameMu.Lock()
	if e, hit := frameCache[id]; hit && time.Since(e.at) < 10*time.Minute {
		frameMu.Unlock()
		writeJSON(w, map[string]any{"blocked": e.blocked, "reason": e.reason})
		return
	}
	frameMu.Unlock()

	p := portals[i]
	blocked, reason := checkFraming(p.Login)
	frameMu.Lock()
	frameCache[id] = &frameEntry{blocked: blocked, reason: reason, at: time.Now()}
	frameMu.Unlock()
	writeJSON(w, map[string]any{"blocked": blocked, "reason": reason})
}

func checkFraming(rawURL string) (bool, string) {
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return false, ""
	}
	req.Header.Set("User-Agent", "GosPortals/"+appVer)
	resp, err := httpClient.Do(req)
	if err != nil {
		return false, "" // сеть недоступна — не мешаем, во вкладке будет видно
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))

	xfo := strings.ToUpper(strings.TrimSpace(resp.Header.Get("X-Frame-Options")))
	if strings.Contains(xfo, "DENY") || strings.Contains(xfo, "SAMEORIGIN") {
		return true, "X-Frame-Options: " + xfo
	}
	csp := resp.Header.Get("Content-Security-Policy")
	if fa := cspDirective(csp, "frame-ancestors"); fa != "" {
		fal := strings.ToLower(fa)
		switch {
		case strings.Contains(fal, "'none'"):
			return true, "CSP frame-ancestors 'none'"
		case !strings.Contains(fal, "*") && !strings.Contains(fal, "http"):
			return true, "CSP frame-ancestors ограничивает встраивание"
		case strings.Contains(fal, "'self'") && !strings.Contains(fal, "localhost") && !strings.Contains(fal, "127.0.0.1"):
			return true, "CSP frame-ancestors 'self'"
		}
	}
	return false, ""
}

func cspDirective(csp, name string) string {
	for _, part := range strings.Split(csp, ";") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(strings.ToLower(part), name) {
			return strings.TrimSpace(strings.TrimPrefix(part, part[:len(name)]))
		}
	}
	return ""
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func sortedKeys(m map[string]Status) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
