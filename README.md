# MS SQL Monitor

Мониторинг, диагностика и рекомендации по настройке **Microsoft SQL Server**,
в том числе для 1С. Программа не меняет конфигурацию SQL Server: изменения
по рекомендациям отчёта выполняет администратор после проверки.

## Программы в репозитории

- **[`mssql-monitor-go/`](mssql-monitor-go/README.md)** — основная Go-версия:
  автономный EXE со встроенным веб-интерфейсом, профильный аудит по статьям,
  отмена диагностики и кнопка «Выход».
- **[`mssql-monitor/`](mssql-monitor/README.md)** — Python-версия с оконным
  интерфейсом Tkinter и сборкой в автономный EXE через PyInstaller.

Исходники, тесты, иконки, документация и примеры отчётов относятся к SQL Monitor.
Телеметрия, настройки машины, пароли и локальные данные мониторинга не должны
попадать в Git.

## Автоматическая сборка Windows x64

Workflow: [SQL Monitor - Windows EXE](https://github.com/zigorminsk-debug/SQL-2/actions/workflows/build-windows.yml).
Файл: [`.github/workflows/build-windows.yml`](.github/workflows/build-windows.yml).

Сборка запускается при изменении SQL Monitor в `main` и ветках `arena/**`,
для pull request в `main` и вручную через `workflow_dispatch`.

1. Откройте **Actions → SQL Monitor - Windows EXE**.
2. Выберите завершённый успешный запуск нужной ветки/коммита.
3. Скачайте один из артефактов:
   - **MSSQLMonitor-Go-windows-x64** — основная версия;
   - **MSSQLMonitor-Python-windows-x64** — Python/Tkinter-версия.
4. Распакуйте ZIP. `MSSQLMonitor.exe` открывает GUI; CLI запускается с
   `-cli` (Go) или `--cli` (Python).

В каждом архиве: GUI и CLI EXE, демо-отчёт, инструкция, номер запуска/коммит и
SHA-256 файлов. Артефакты хранятся 30 дней. Публикация GitHub Release не
выполняется автоматически. Для ручного запуска workflow через интерфейс
GitHub его файл должен быть включён в основную ветку.

Перед сборкой выполняются Go-тесты с `-race`, `go vet`, тесты Python и тесты
браузерного кода. Собранные CLI проверяются на Windows без подключения к SQL
Server: вывод версии и генерация демо-отчёта.
