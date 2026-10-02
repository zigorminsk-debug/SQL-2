@echo off
rem ============================================================
rem  Сборка MSSQLMonitor.exe (Windows, один файл)
rem  Запуск:  build.bat            -> GUI-версия  (MSSQLMonitor.exe)
rem           build.bat cli        -> консольная   (MSSQLMonitorCLI.exe)
rem  Требуется установленный Python 3.10-3.12 (python.org, "Add to PATH")
rem ============================================================
setlocal EnableExtensions
chcp 65001 >nul
cd /d "%~dp0"

rem --- находим Python ---
set "PY="
py -3.12 -c "import sys" >nul 2>&1 && set "PY=py -3.12"
if not defined PY py -3 -c "import sys" >nul 2>&1 && set "PY=py -3"
if not defined PY python -c "import sys" >nul 2>&1 && set "PY=python"
if not defined PY (
    echo [X] Python не найден.
    echo     Установите Python 3.10-3.12 с https://www.python.org/downloads/
    echo     и отметьте галочку "Add python.exe to PATH", затем повторите.
    pause
    exit /b 1
)
echo [i] Использую: %PY%

rem --- окружение ---
if exist .venv-build rmdir /s /q .venv-build
echo [1/3] Создаю виртуальное окружение...
%PY% -m venv .venv-build || goto :fail
call .venv-build\Scripts\activate.bat

python -m pip install --upgrade pip -q
echo [2/3] Устанавливаю зависимости (pyodbc, pymssql, pyinstaller)...
pip install -r requirements.txt pyinstaller || goto :fail

echo [3/3] Собираю EXE...
set "ICON="
if exist icon.ico set "ICON=--icon icon.ico"

if /i "%~1"=="cli" (
    pyinstaller --clean --noconfirm --onefile --console --name MSSQLMonitorCLI ^
        %ICON% --collect-all pymssql app.py || goto :fail
    set "OUT=dist\MSSQLMonitorCLI.exe"
    set "WHAT=консольная версия (для Task Scheduler: суточный мониторинг + отчёт)"
) else (
    pyinstaller --clean --noconfirm --onefile --windowed --name MSSQLMonitor ^
        %ICON% --collect-all pymssql app.py || goto :fail
    set "OUT=dist\MSSQLMonitor.exe"
    set "WHAT=GUI-версия"
)
echo.
echo ============================================================
if exist %OUT% (
    echo  ГОТОВО: %OUT%  (%WHAT%)
    echo  Файл самодостаточен: рядом ничего ставить не нужно.
    echo  Для pyodbc дополнительно можно установить системный
    echo  "ODBC Driver 18 for SQL Server" - но pymssql работает и без него.
) else (
    echo  [X] EXE не собрался - смотрите сообщения выше.
)
echo ============================================================
pause
exit /b 0

:fail
echo [X] Ошибка сборки. Смотрите вывод выше.
pause
exit /b 1
