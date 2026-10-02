//go:build windows

package main

import (
	"os"
	"os/exec"
)

// openAppWindow открывает панель в отдельном «app-окне» браузера (без вкладок
// и адресной строки) — приложение выглядит как единое нативное окно, а все
// порталы открываются внутри него (каскадом: Edge → Chrome → браузер по умолчанию).
func openAppWindow(url string) error {
	pf, pfx86 := os.Getenv("ProgramFiles"), os.Getenv("ProgramFiles(x86)")

	edge := []string{
		pfx86 + `\Microsoft\Edge\Application\msedge.exe`,
		pf + `\Microsoft\Edge\Application\msedge.exe`,
	}
	if p := firstExisting(edge); p != "" {
		return startDetached(p, "--app="+url, "--window-size=1280,860")
	}
	if p, err := exec.LookPath("msedge.exe"); err == nil {
		return startDetached(p, "--app="+url, "--window-size=1280,860")
	}

	chrome := []string{
		pf + `\Google\Chrome\Application\chrome.exe`,
		pfx86 + `\Google\Chrome\Application\chrome.exe`,
	}
	if p := firstExisting(chrome); p != "" {
		return startDetached(p, "--app="+url, "--window-size=1280,860")
	}
	if p, err := exec.LookPath("chrome.exe"); err == nil {
		return startDetached(p, "--app="+url, "--window-size=1280,860")
	}

	// Нет ни Edge, ни Chrome с app-режимом — обычная вкладка браузера.
	return openBrowser(url)
}

func firstExisting(paths []string) string {
	for _, p := range paths {
		if p == "" {
			continue
		}
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p
		}
	}
	return ""
}

func startDetached(path string, args ...string) error {
	cmd := exec.Command(path, args...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Start()
}
