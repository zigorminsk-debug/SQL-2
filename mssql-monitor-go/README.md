# MS SQL Monitor (Go)

Контроль ресурсов, скорости обработки и настроек **Microsoft SQL Server** с
рекомендациями по оптимизации. Полная реализация на Go: один исполняемый файл,
без установки и зависимостей (встроенный драйвер go-mssqldb, веб-GUI внутри
бинарника). Порт Python-версии 1:1 (демо-отчёт даёт те же 34 находки).

## Что в комплекте (`release/`)

| Файл | Назначение |
|---|---|
| `MSSQLMonitor.exe` | **основная программа** (GUI). Двойной клик — откроется интерфейс в браузере (локальный адрес, интернет не нужен). |
| `MSSQLMonitorCLI.exe` | консольный вариант (те же флаги, вывод в терминал) — для планировщика/сервера. |
| `demo_report.html` | пример полного отчёта (демо-сервер, без подключения к реальному SQL Server). |

Сборки: Windows x64 (PE32+), иконка + манифест (DPI-aware, asInvoker).

## Быстрый старт

1. Запустите `MSSQLMonitor.exe` — откроется `http://127.0.0.1:8790`.
2. Вкладка «Подключение»: укажите сервер (например `SRV-SQL` или
   `SRV-SQL\SQL2022`), аутентификацию, нажмите «Подключиться».
   - Windows-аутентификация работает под текущим пользователем (SSPI);
     для доменной с `логином\паролем` укажите `ДОМЕН\пользователь`.
   - Флажок «Демо» — посмотреть интерфейс на синтетическом «проблемном»
     сервере без реального подключения.
3. Вкладка «Мониторинг» → «Запустить мониторинг» (по умолчанию сутки,
   опрос раз в минуту).
4. Вкладка «Отчёт» → «Сформировать отчёт» (или дождитесь автоотчёта).
   Отчёт — самодостаточный HTML: графики, разбор замедлений, топ запросов,
   индексы, статистика, базы, tempdb, бэкапы, задания, взаимоблокировки,
   журнал ошибок и ~30 правил рекомендаций с готовым T-SQL.

Данные хранятся рядом с EXE (`data/`, `reports/`, `logs/`); при отсутствии
прав записи — в `%USERPROFILE%\MSSQLMonitor`.

## Консольный режим

```bat
MSSQLMonitorCLI.exe -server SRV-SQL\SQL2022 -windows -hours 8 -interval 30
MSSQLMonitorCLI.exe -server 10.0.0.5 -user sa -password *** -database master
MSSQLMonitorCLI.exe -demo-report            &rem демо-отчёт без сервера
```

Флаги: `-cli -server -port -database -auth sql|windows -user -password
-encrypt mandatory|strict|optional|disable -hours -duration -interval
-long-query -skip-monitor -no-frag -no-errorlog -no-plan-xml -demo
-demo-report -host -http -no-browser -version`.

## Безопасность

- Программа **только читает** сервер: `SELECT` к системным представлениям
  (DMV). Никаких изменений.
- Все T-SQL команды из отчёта применяются **вручную** вами.
- Права для полного отчёта: `VIEW SERVER STATE`, чтение `msdb`
  (бэкапы/задания), доступ к `sp_readerrorlog`. Недостающие права не ломают
  отчёт — недоступные проверки просто отмечаются.
- Веб-GUI слушает только `127.0.0.1` (доступ с других машин по умолчанию
  запрещён; `0.0.0.0` — явным флагом `-host`, на свой страх и риск).

## Контакты и поддержка

- Разработчик: **Zakharevich Igor** — [позвонить: +375 (29) 337-14-12](tel:+375293371412).
- Сайт поддержки: [csl.by](https://csl.by/); [электронная почта](mailto:info@csl.by).
- Адрес: [г. Минск, ул. Фабрициуса, 9](https://csl.by/contacts/).
- Время работы: понедельник–пятница, 09:00–17:00.
- Услуги: [системное администрирование](https://csl.by/services/administration/),
  [заправка картриджей](https://csl.by/services/print/),
  [ремонт оргтехники](https://csl.by/services/repair/),
  [настройка АТС](https://csl.by/services/telephony/),
  [ремонт промышленной электроники](https://csl.by/services/repair/electronics/),
  [ремонт майнеров](https://csl.by/services/repair/miners/) и
  [видеонаблюдение и СКУД](https://csl.by/services/cctv/).

## Сборка из исходников

Требуется Go 1.21+:

```bat
build.bat
```

или вручную:

```sh
go run . -demo-report                                   # проверить
go run . -demo -no-browser -http 8790                   # демо-GUI
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath \
  -ldflags "-s -w -H windowsgui" -o MSSQLMonitor.exe .  # релиз GUI
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath \
  -ldflags "-s -w" -o MSSQLMonitorCLI.exe .             # релиз CLI
```

Иконка/манифест: `rsrc -arch amd64 -ico icon.ico -manifest app.manifest
-o rsrc_windows_amd64.syso` (файл `.syso` подхватывается сборкой Windows
автоматически).

## Автоматические сборки GitHub Actions

Workflow [`.github/workflows/build-windows.yml`](../.github/workflows/build-windows.yml)
запускается автоматически при каждом `push` и `pull request`; его также можно запустить вручную
во вкладке **Actions → Build Windows packages → Run workflow**. Он выполняет `go vet` и
`go test`, собирает GUI- и CLI-версии Go для Windows, а также проверяет и собирает Python-версию.
Готовые файлы доступны в Artifacts: `mssql-monitor-go-windows-v2.0.*` и
`mssql-monitor-python-windows-v2.0.*` (хранятся 14 дней). Номер версии в шапке
каждого EXE формируется автоматически: `2.0.<номер запуска>.<попытка>`.

## Структура исходников

| Файл | Содержимое |
|---|---|
| `main.go` | флаги, режимы (GUI / CLI / демо) |
| `webui.go` | веб-интерфейс и REST API (`/api/*`), `web/index.html` — русский GUI (встроен) |
| `db.go` | подключение (DSN, SSPI/NTLM, шифрование), пул, таймауты |
| `checks.go` | лёгкий тик мониторинга: CPU, счётчики, ожидания (дельты), IO, tempdb, сессии, блокировки |
| `checks_deep.go` | глубокая диагностика: настройки, ожидания, топ запросов, планы (XML), индексы, статистика, базы, tempdb, бэкапы, задания, deadlocks, журнал ошибок |
| `analysis.go`, `analysis_rules.go` | база знаний ожиданий, окна замедлений, ~30 правил рекомендаций, оценка здоровья |
| `report.go`, `report_svg.go`, `report_assets.go` | HTML-отчёт с SVG-графиками (CSS/JS идентичны Python-версии) |
| `demo.go` | демо-сервер без SQL Server (для обучения/тестов) |
| `monitor.go`, `storage.go` | движок суточного мониторинга, JSONL-хранилище |
| `config.go`, `logx.go`, `types.go` | конфигурация (пароль — base64), журнал, общие типы |
