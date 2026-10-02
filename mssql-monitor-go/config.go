// config.go — конфигурация и пути (портабельно: рядом с EXE, иначе в профиле).
package main

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// Version — версия программы «база.порядковыйНомерСборки» (например 2.0.0.7).
// Номер сборки присваивается при компиляции:
//
//	GitHub Actions — счётчик запусков workflow:
//	  -ldflags "-X main.Version=2.0.0.<номер запуска>"  (см. .github/workflows/build-windows-go.yml)
//	локальная сборка (build.bat) помечается 2.0.0-dev.
var Version = "2.0.0"

const AppTitle = "MS SQL Monitor — контроль и оптимизация SQL Server"

type Config struct {
	Server        string  `json:"server"`
	Port          int     `json:"port"`
	Database      string  `json:"database"`
	WindowsAuth   bool    `json:"windows_auth"`
	User          string  `json:"user"`
	PasswordB64   string  `json:"password_b64"`
	SavePassword  bool    `json:"save_password"`
	Encrypt       bool    `json:"encrypt"`
	TrustCert     bool    `json:"trust_cert"`
	Interval      int     `json:"interval"`
	DurationHours int     `json:"duration_hours"`
	AutoReport    bool    `json:"auto_report"`
	LongQueryS    float64 `json:"long_query_s"`
	FragCheck     bool    `json:"frag_check"`
	ErrorLogCheck bool    `json:"errorlog_check"`
	PlanXMLCheck  bool    `json:"plan_xml_check"`
	DemoMode      bool    `json:"demo_mode"`
}

func defaultConfig() Config {
	return Config{
		Server: "localhost", Port: 1433, Database: "master",
		WindowsAuth: true, User: "", PasswordB64: "",
		SavePassword: false, Encrypt: false, TrustCert: true,
		Interval: 60, DurationHours: 24, AutoReport: true,
		LongQueryS: 30, FragCheck: true, ErrorLogCheck: true,
		PlanXMLCheck: true, DemoMode: false,
	}
}

func baseDir() string {
	if exe, err := os.Executable(); err == nil {
		return filepath.Dir(exe)
	}
	wd, _ := os.Getwd()
	return wd
}

func writable(p string) bool {
	if err := os.MkdirAll(p, 0o755); err != nil {
		return false
	}
	probe := filepath.Join(p, ".probe")
	if err := os.WriteFile(probe, []byte("x"), 0o644); err != nil {
		return false
	}
	os.Remove(probe)
	return true
}

func dataRoot() string {
	b := baseDir()
	if writable(filepath.Join(b, "data")) {
		return b
	}
	home, _ := os.UserHomeDir()
	p := filepath.Join(home, "MSSQLMonitor")
	os.MkdirAll(p, 0o755)
	return p
}

func DataDir() string    { return filepath.Join(dataRoot(), "data") }
func ReportsDir() string { return filepath.Join(dataRoot(), "reports") }
func LogsDir() string    { return filepath.Join(dataRoot(), "logs") }
func ConfigPath() string { return filepath.Join(dataRoot(), "data", "config.json") }
func StorePath() string  { return filepath.Join(dataRoot(), "data") }

func LoadConfig() Config {
	cfg := defaultConfig()
	b, err := os.ReadFile(ConfigPath())
	if err == nil {
		json.Unmarshal(b, &cfg)
	}
	return cfg
}

func SaveConfig(cfg Config) error {
	os.MkdirAll(filepath.Dir(ConfigPath()), 0o755)
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(ConfigPath(), b, 0o644)
}

func EncodePwd(p string) string {
	return base64.StdEncoding.EncodeToString([]byte(p))
}

func DecodePwd(s string) string {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return ""
	}
	return string(b)
}

// Save — сохранить конфигурацию.
func (c *Config) Save() error { return SaveConfig(*c) }

// reportsDir — каталог отчётов (для webui/main).
func reportsDir() string { return ReportsDir() }

// saveReport — сохранить HTML-отчёт с меткой времени, вернуть путь.
func saveReport(html string) string {
	os.MkdirAll(ReportsDir(), 0o755)
	path := filepath.Join(ReportsDir(),
		"report_"+time.Now().Format("20060102_150405")+".html")
	os.WriteFile(path, []byte(html), 0o644)
	return path
}
