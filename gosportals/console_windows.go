//go:build windows

package main

import "syscall"

// hideConsoleWindow прячет окно консоли: после того как главное окно
// приложения открылось, терминал больше не нужен. Программа продолжает
// жить (сервер + heartbeat), и завершится при закрытии главного окна.
func hideConsoleWindow() {
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	user32 := syscall.NewLazyDLL("user32.dll")
	getConsoleWindow := kernel32.NewProc("GetConsoleWindow")
	showWindow := user32.NewProc("ShowWindow")
	hwnd, _, _ := getConsoleWindow.Call()
	if hwnd != 0 {
		showWindow.Call(hwnd, 0) // SW_HIDE = 0
	}
}
