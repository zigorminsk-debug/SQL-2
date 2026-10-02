//go:build !windows

package main

import "os/exec"

// openAppWindow — app-режим окна доступен в Chromium-совместимых браузерах.
func openAppWindow(url string) error {
	for _, b := range []string{"google-chrome", "chromium", "chromium-browser", "microsoft-edge"} {
		if p, err := exec.LookPath(b); err == nil {
			cmd := exec.Command(p, "--app="+url, "--window-size=1280,860")
			if err := cmd.Start(); err == nil {
				return nil
			}
		}
	}
	return openBrowser(url)
}
