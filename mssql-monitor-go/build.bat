@echo off
rem ============================================================
rem  MS SQL Monitor (Go) — сборка Windows-бинарников
rem  Требуется установленный Go 1.21+ (https://go.dev/dl/)
rem ============================================================
where go >nul 2>nul
if errorlevel 1 (
    echo ОШИБКА: Go не найден в PATH. Установите с https://go.dev/dl/
    exit /b 1
)

echo [1/3] Проверка сборки (Linux-сборка как smoke-тест)...
go build -o NUL . || exit /b 1

echo [2/3] Сборка GUI-версии (MSSQLMonitor.exe, без консоли, с иконкой)...
set GOOS=windows
set GOARCH=amd64
set CGO_ENABLED=0
go build -trimpath -ldflags "-s -w -H windowsgui" -o release\MSSQLMonitor.exe . || exit /b 1

echo [3/3] Сборка CLI-версии (MSSQLMonitorCLI.exe, с консолью)...
go build -trimpath -ldflags "-s -w" -o release\MSSQLMonitorCLI.exe . || exit /b 1

echo.
echo Готово: release\MSSQLMonitor.exe и release\MSSQLMonitorCLI.exe
pause
