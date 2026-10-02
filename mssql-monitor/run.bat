@echo off
rem Запуск MS SQL Monitor из исходников (без сборки EXE).
rem Требуется Python 3.9+ с https://python.org (галочка "Add python.exe to PATH").
cd /d "%~dp0"
python -m pip install -r requirements.txt -q 2>nul
python app.py
if errorlevel 1 pause
