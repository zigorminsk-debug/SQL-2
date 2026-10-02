//go:build !windows

package main

// hideConsoleWindow — на Linux/macOS окно терминала не прячем:
// там это единственный способ остановить веб-панель (Ctrl+C).
func hideConsoleWindow() {}
