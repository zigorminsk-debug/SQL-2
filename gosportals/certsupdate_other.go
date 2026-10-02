//go:build !windows

package main

import (
	"encoding/json"
	"net/http"
)

// handleCertUpdate — автообновление сертификатов доступно только в Windows
// (certutil + хранилище Windows). На других ОС отдаём объяснение.
func handleCertUpdate(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(map[string]any{
		"failedn": 1,
		"steps": []map[string]any{{
			"name":   "автообновление сертификатов",
			"ok":     false,
			"detail": "доступно только в Windows-версии приложения; вручную — nces.by → «Сертификаты, руководства, инструкции и ПО»",
		}},
		"checks": runEnvironmentChecks(),
	})
}
