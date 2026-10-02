//go:build !windows

package main

import "errors"

// runGUI — полноценный GUI собирается только под Windows (нативные окна Win32).
// На Linux/macOS работает веб-панель (режим по умолчанию и единственный).
var errNoGUI = errors.New("полноценный GUI собирается только для Windows (используйте сборку GosPortalyRB.exe); здесь доступна веб-панель")

func runGUI() error { return errNoGUI }
