@echo off
rem ============================================================
rem  MS SQL Monitor (Go) — сборка Windows-бинарников
rem  Требуется установленный Go 1.21+ (https://go.dev/dl/)
rem
rem  Готовые пронумерованные сборки (2.0.0.N) делает GitHub Actions —
rem  см. .github/workflows/build-windows-go.yml в корне репозитория
rem  (Actions -> Build Windows EXE (Go) -> Releases).
rem  Локальная сборка помечается версией 2.0.0-dev.
rem ============================================================
where go >nul 2>nul
if errorlevel 1 (
    echo ОШИБКА: Go не найден в PATH. Установите с https://go.dev/dl/
    exit /b 1
)

set VER=2.0.0-dev

echo [1/4] Проверка сборки (Linux-сборка как smoke-тест)...
go build -o NUL . || exit /b 1

echo [2/4] Сборка GUI-версии (MSSQLMonitor.exe, без консоли, с иконкой)...
set GOOS=windows
set GOARCH=amd64
set CGO_ENABLED=0
go build -trimpath -ldflags "-s -w -H windowsgui -X main.Version=%VER%" -o release\MSSQLMonitor.exe . || exit /b 1

echo [3/4] Сборка CLI-версии (MSSQLMonitorCLI.exe, с консолью)...
go build -trimpath -ldflags "-s -w -X main.Version=%VER%" -o release\MSSQLMonitorCLI.exe . || exit /b 1

echo [4/4] Демо-отчёт и архив...
release\MSSQLMonitorCLI.exe -demo-report >nul 2>nul
if exist release\reports\demo_report.html move /y release\reports\demo_report.html release\demo_report.html >nul
if exist release\reports rmdir /s /q release\reports
if exist release\logs rmdir /s /q release\logs
if exist release\data rmdir /s /q release\data
powershell -NoProfile -Command "Compress-Archive -Force -Path 'release\MSSQLMonitor.exe','release\MSSQLMonitorCLI.exe','release\demo_report.html' -DestinationPath 'release\MSSQLMonitor-%VER%-win64.zip'" >nul 2>nul

echo.
echo Готово: release\MSSQLMonitor.exe и release\MSSQLMonitorCLI.exe (версия %VER%)
echo Пронумерованные релизные сборки: GitHub Actions -^> Releases (v2.0.0.N)
pause
