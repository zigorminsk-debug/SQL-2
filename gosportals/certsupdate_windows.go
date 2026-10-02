//go:build windows

package main

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// Официальные сертификаты и СОС NCES (НУЦ/ГосСУОК) — источник:
// nces.by «О проведении планового издания нового СОК КУЦ и СОК РУЦ».
var certFixItems = []struct {
	url   string // откуда скачать
	store string // в какое хранилище ставить (Root/CA)
	name  string // подпись для журнала
}{
	{"https://nces.by/wp-content/uploads/certificates/pki/kuc2.cer", "Root", "СОК КУЦ ГосСУОК (от 13.06.2024)"},
	{"https://nces.by/wp-content/uploads/certificates/pki/ruc3.cer", "Root", "СОК РУЦ (от 13.06.2024)"},
	{"https://nces.by/wp-content/uploads/certificates/pki/kuc2.crl", "CA", "СОС КУЦ (актуальный)"},
	{"https://nces.by/wp-content/uploads/certificates/pki/ruc3.crl", "CA", "СОС РУЦ (актуальный)"},
}

type certStep struct {
	Name   string `json:"name"`
	Ok     bool   `json:"ok"`
	Detail string `json:"detail"`
}

// handleCertUpdate — «исправить в один клик»: скачивает официальные
// сертификаты/СОС и ставит в хранилище пользователя certutil-ом.
func handleCertUpdate(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	var steps []certStep

	for _, it := range certFixItems {
		b, err := downloadChecked(r.Context(), it.url, it.store)
		if err != nil {
			steps = append(steps, certStep{it.name, false, "скачивание: " + err.Error()})
			continue
		}
		if err := installCert(it.store, b); err != nil {
			steps = append(steps, certStep{it.name, false, "установка: " + err.Error()})
			continue
		}
		steps = append(steps, certStep{it.name, true, "установлено в хранилище «" + it.store + "»"})
		logEvent(fmt.Sprintf("cert-update: %s ← %s", it.name, it.url))
	}

	// Сразу видим итоговую картину проверок.
	checks := runEnvironmentChecks()
	json.NewEncoder(w).Encode(map[string]any{
		"steps":   steps,
		"checks":  checks,
		"failedn": countFailed(steps),
	})
}

func countFailed(s []certStep) int {
	n := 0
	for _, x := range s {
		if !x.Ok {
			n++
		}
	}
	return n
}

// downloadChecked скачивает файл и проверяет, что это реальный X.509/CRL.
func downloadChecked(ctx context.Context, url, kind string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "GosPortaly/"+appVer)
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, err
	}
	if kind == "Root" {
		if _, err := x509.ParseCertificate(b); err != nil {
			return nil, fmt.Errorf("файл не похож на X.509-сертификат")
		}
	} else {
		if _, err := x509.ParseDERCRL(b); err != nil {
			return nil, fmt.Errorf("файл не похож на CRL")
		}
	}
	return b, nil
}

// installCert ставит через certutil в хранилище текущего пользователя
// (не требует прав администратора).
func installCert(store string, der []byte) error {
	f, err := os.CreateTemp("", "gp-cert-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(der); err != nil {
		f.Close()
		return err
	}
	f.Close()

	cmd := exec.Command("certutil.exe", "-user", "-addstore", store, f.Name())
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("certutil: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}
