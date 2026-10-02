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
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
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
	addr := flag.String("addr", "127.0.0.1", "адрес прослушивания (127.0.0.1 — только этот компьютер)")
	port := flag.Int("port", 0, "порт (0 — выбрать свободный автоматически)")
	noOpen := flag.Bool("no-open", false, "не открывать браузер автоматически")
	flag.Parse()

	mustLoadData()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", handleIndex)
	mux.HandleFunc("GET /api/meta", handleMeta)
	mux.HandleFunc("GET /api/portals", handlePortals)
	mux.HandleFunc("GET /api/status", handleStatus)
	mux.HandleFunc("GET /api/checks", handleChecks)
	mux.HandleFunc("POST /api/open", handleOpen)

	ln, err := net.Listen("tcp", fmt.Sprintf("%s:%d", *addr, *port))
	if err != nil {
		log.Fatalf("не удалось занять порт: %v", err)
	}

	showURL := ln.Addr().String()
	if strings.HasPrefix(showURL, "0.0.0.0:") || strings.HasPrefix(showURL, ":") {
		showURL = "127.0.0.1" + showURL[strings.LastIndex(showURL, ":"):]
	}
	url := "http://" + showURL

	fmt.Println("========================================================")
	fmt.Printf("  %s  v%s\n", appName, appVer)
	fmt.Println("  Панель госпорталов Республики Беларусь")
	fmt.Println("========================================================")
	fmt.Printf("  Панель открыта:     %s\n", url)
	fmt.Println("  Это окно (терминал) НЕ закрывайте, пока работаете.")
	fmt.Println("  Для выхода нажмите  Ctrl+C")
	fmt.Println("========================================================")

	if !*noOpen && (*addr == "127.0.0.1" || *addr == "localhost") {
		go func() {
			time.Sleep(350 * time.Millisecond)
			if err := openBrowser(url); err != nil {
				log.Printf("браузер не открылся (%v) — откройте вручную: %s", err, url)
			}
		}()
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

func sortedKeys(m map[string]Status) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
