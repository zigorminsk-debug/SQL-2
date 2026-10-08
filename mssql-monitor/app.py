# -*- coding: utf-8 -*-
"""
MS SQL Monitor — программа контроля и оптимизации Microsoft SQL Server.

GUI:   python app.py            (или готовый MSSQLMonitor.exe)
CLI:   python app.py --cli ... (суточный мониторинг без интерфейса)
Демо:  python app.py --demo-report demo.html

Возможности:
  * мониторинг ресурсов: CPU, память, ввод-вывод, tempdb, сессии;
  * скорость обработки: запросов/с, транзакций/с, компиляции;
  * суточный мониторинг с накоплением данных и графиками загрузки;
  * глубокая диагностика: настройки, ожидания, запросы, индексы,
    статистика, блокировки, взаимоблокировки, бэкапы, журнал ошибок;
  * автоматические рекомендации по устранению проблем и оптимизации;
  * подробный самодостаточный HTML-отчёт (графики + разбор причин замедлений).
"""
import argparse
import base64
import datetime
import json
import logging
import os
import queue
import sys
import threading
import time
import webbrowser

from console import configure_console
import analysis
from lifecycle import BackgroundTasks
import checks
import demo
import report
from db import Database
from monitor import MonitorEngine
from storage import Storage

VERSION = "1.0.0"
APP_TITLE = "MS SQL Monitor — контроль и оптимизация SQL Server"

log = logging.getLogger("app")


# =====================================================================
# Пути (портабельно: рядом с exe, иначе в профиле пользователя)
# =====================================================================
def base_dir():
    if getattr(sys, "frozen", False):          # PyInstaller
        return os.path.dirname(sys.executable)
    return os.path.dirname(os.path.abspath(__file__))


def _writable(p):
    try:
        os.makedirs(p, exist_ok=True)
        probe = os.path.join(p, ".probe")
        open(probe, "w").close()
        os.remove(probe)
        return True
    except Exception:
        return False


def data_root():
    b = base_dir()
    if _writable(os.path.join(b, "data")):
        return b
    home = os.path.join(os.path.expanduser("~"), "MSSQLMonitor")
    os.makedirs(home, exist_ok=True)
    return home


DATA_DIR = os.path.join(data_root(), "data")
REPORTS_DIR = os.path.join(data_root(), "reports")
LOGS_DIR = os.path.join(data_root(), "logs")
CONFIG_PATH = os.path.join(data_root(), "config.json")
DB_PATH = os.path.join(DATA_DIR, "monitor.db")


# =====================================================================
# Конфигурация
# =====================================================================
DEFAULT_CONFIG = {
    "server": "localhost", "port": 1433, "database": "master",
    "windows_auth": True, "user": "", "password_b64": "",
    "save_password": False, "encrypt": False, "trust_cert": True,
    "driver": "auto",
    "interval": 60, "duration_hours": 24, "auto_report": True,
    "long_query_s": 30, "frag_check": True, "errorlog_check": True,
    "plan_xml_check": True, "demo_mode": False,
}


def load_config():
    cfg = dict(DEFAULT_CONFIG)
    try:
        with open(CONFIG_PATH, "r", encoding="utf-8") as f:
            cfg.update(json.load(f))
    except Exception:
        pass
    return cfg


def save_config(cfg):
    try:
        os.makedirs(os.path.dirname(CONFIG_PATH), exist_ok=True)
        with open(CONFIG_PATH, "w", encoding="utf-8") as f:
            json.dump(cfg, f, ensure_ascii=False, indent=2)
    except Exception as e:
        log.warning("Не удалось сохранить настройки: %s", e)


def encode_pwd(p):
    return base64.b64encode(p.encode("utf-8")).decode("ascii") if p else ""


def decode_pwd(b):
    try:
        return base64.b64decode(b or "").decode("utf-8")
    except Exception:
        return ""


# =====================================================================
# Генерация отчёта (общая для GUI/CLI/демо)
# =====================================================================
REPORT_CHECK_NAMES = {
    "server": "Сервер и версия", "configs": "Конфигурация",
    "sys_info": "Система", "clerks": "Память (clerks)",
    "plan_cache": "Кэш планов", "wait_stats": "Ожидания",
    "requests_live": "Текущие сессии", "qstats": "Статистика запросов",
    "plan_issues": "Анализ планов", "fragmentation": "Фрагментация индексов",
    "missing_indexes": "Отсутствующие индексы", "stats": "Статистика",
    "unused_indexes": "Неиспользуемые индексы", "databases": "Базы данных",
    "io_files": "Файлы и ввод-вывод", "volumes": "Тома",
    "tempdb": "tempdb", "trace_flags": "Флаги трассировки",
    "query_store": "Query Store", "backups": "Резервные копии",
    "jobs_failed": "Задания SQL Agent", "deadlocks": "Взаимоблокировки",
    "errorlog": "Журнал ошибок",
}


def generate_report(db, storage=None, period_hours=None, options=None,
                    log_cb=None, sample_interval=None):
    """Полный цикл: глубокая диагностика + таймлайн -> HTML (строка)."""
    def prog(name):
        if log_cb:
            log_cb("Сбор: %s…" % REPORT_CHECK_NAMES.get(name, name))

    t0 = t1 = None
    samples = []
    blocking_events = []
    long_queries = []
    if storage is not None:
        try:
            n, mn, mx = storage.sample_count()
            if n:
                t1 = mx
                t0 = (mn if period_hours is None else
                      max(mn, mx - period_hours * 3600))
                samples = storage.load_samples(t0, t1)
                blocking_events = storage.load_blocking(t0, t1)
                long_queries = storage.load_long_queries(t0, t1)
                if log_cb:
                    log_cb("Загружены данные мониторинга: %d точек (%s — %s)"
                           % (len(samples),
                              datetime.datetime.fromtimestamp(t0).strftime("%d.%m %H:%M"),
                              datetime.datetime.fromtimestamp(t1).strftime("%d.%m %H:%M")))
            else:
                if log_cb:
                    log_cb("Накопленных данных мониторинга нет — отчёт по текущему снимку")
        except Exception as e:
            log.exception("storage load failed")
            if log_cb:
                log_cb("Хранилище мониторинга недоступно: %s" % e)

    data = checks.collect_deep(db, options=options, progress=prog,
                               blocking_events=blocking_events,
                               long_queries=long_queries)
    if hasattr(db, "check_cancelled"):
        db.check_cancelled()
    if log_cb:
        if data.get("errors"):
            log_cb("Недоступные проверки: %s" % ", ".join(data["errors"].keys()))
        log_cb("Анализ и выработка рекомендаций…")
    findings = analysis.build_findings(data, samples, blocking_events, long_queries)
    health = analysis.health_score(findings)
    peaks = analysis.find_slowdown_windows(samples, blocking_events, long_queries)
    srv = data.get("server") or {}
    meta = {
        "server_display": getattr(db, "server", None) or srv.get("srv") or "MS SQL Server",
        "server_full": srv.get("srv") or getattr(db, "server", ""),
        "generated_str": datetime.datetime.now().strftime("%d.%m.%Y %H:%M:%S"),
        "interval": sample_interval,
        "driver": getattr(db, "driver_name", None) or "",
    }
    html_text = report.build_report(data, findings, health, peaks, samples,
                                    blocking_events, long_queries, meta)
    if log_cb:
        log_cb("Оценка: %d/100 (%s). Рекомендаций: критичных %d, важных %d, инфо %d"
               % (health["score"], health["grade"], health["crit"], health["warn"],
                  health["info"]))
    return html_text


def save_report(html_text):
    os.makedirs(REPORTS_DIR, exist_ok=True)
    name = "report_%s.html" % datetime.datetime.now().strftime("%Y-%m-%d_%H%M")
    path = os.path.join(REPORTS_DIR, name)
    with open(path, "w", encoding="utf-8") as f:
        f.write(html_text)
    return path


# =====================================================================
# GUI
# =====================================================================
def run_gui():
    import tkinter as tk
    from tkinter import ttk

    cfg = load_config()

    # ---------- логирование ----------
    os.makedirs(LOGS_DIR, exist_ok=True)
    fmt = logging.Formatter("%(asctime)s [%(levelname)s] %(message)s", "%H:%M:%S")
    fh = logging.FileHandler(os.path.join(
        LOGS_DIR, "app_%s.log" % datetime.date.today().strftime("%Y%m%d")),
        encoding="utf-8")
    fh.setFormatter(fmt)
    logging.basicConfig(level=logging.INFO, handlers=[fh],
                        format="%(asctime)s %(levelname)s %(message)s")

    class App(tk.Tk):
        def __init__(self):
            super().__init__()
            self.title(APP_TITLE)
            self.geometry("1120x760")
            self.minsize(960, 640)
            self.cfg = cfg
            self.db = None
            self.tasks = BackgroundTasks()
            self.monitor = None
            self.storage = Storage(DB_PATH)
            self.storage.purge(14)
            self.q = queue.Queue()
            self.cpu_hist = []

            style = ttk.Style(self)
            try:
                style.theme_use("vista" if "vista" in style.theme_names() else "clam")
            except Exception:
                pass
            self._build_ui()
            self._poll_id = self.after(200, self._poll)

        # ------------------------------------------------- UI
        def _build_ui(self):
            head = ttk.Frame(self)
            head.pack(fill="x", padx=12, pady=(10, 4))
            ttk.Label(head, text="MS SQL Monitor", font=("Segoe UI", 16, "bold"),
                      foreground="#1a5fb4").pack(side="left")
            ttk.Label(head, text="  контроль ресурсов · суточный мониторинг · "
                                 "рекомендации по оптимизации",
                      foreground="#5b6b7c").pack(side="left")
            self.status_lbl = ttk.Label(head, text="● не подключено", foreground="#b71c1c")
            self.status_lbl.pack(side="right")

            nb = ttk.Notebook(self)
            nb.pack(fill="both", expand=True, padx=12, pady=6)
            self.tab_conn = ttk.Frame(nb)
            self.tab_mon = ttk.Frame(nb)
            self.tab_rep = ttk.Frame(nb)
            self.tab_log = ttk.Frame(nb)
            nb.add(self.tab_conn, text="  Подключение  ")
            nb.add(self.tab_mon, text="  Мониторинг  ")
            nb.add(self.tab_rep, text="  Диагностика и отчёт  ")
            nb.add(self.tab_log, text="  Журнал  ")
            self._tab_conn()
            self._tab_mon()
            self._tab_rep()
            self._tab_log()

            bar = ttk.Frame(self, relief="groove", padding=(8, 3))
            bar.pack(fill="x", side="bottom")
            self.bar_lbl = ttk.Label(bar, text="Готово")
            self.bar_lbl.pack(side="left")
            ttk.Button(bar, text="Выход", command=self.on_close).pack(side="right")
            ttk.Label(bar, text="v%s" % VERSION, foreground="#90a4ae").pack(side="right")

        def _lab(self, parent, text, w=16):
            return ttk.Label(parent, text=text, width=w, anchor="e")

        def _tab_conn(self):
            f = ttk.Frame(self.tab_conn, padding=16)
            f.pack(fill="both", expand=True)
            self.v_server = tk.StringVar(value=cfg.get("server", "localhost"))
            self.v_port = tk.StringVar(value=str(cfg.get("port", 1433)))
            self.v_db = tk.StringVar(value=cfg.get("database", "master"))
            self.v_win = tk.BooleanVar(value=cfg.get("windows_auth", True))
            self.v_user = tk.StringVar(value=cfg.get("user", ""))
            self.v_pwd = tk.StringVar(value=decode_pwd(cfg.get("password_b64")))
            self.v_savepwd = tk.BooleanVar(value=cfg.get("save_password", False))
            self.v_encrypt = tk.BooleanVar(value=cfg.get("encrypt", False))
            self.v_trust = tk.BooleanVar(value=cfg.get("trust_cert", True))
            self.v_driver = tk.StringVar(value=cfg.get("driver", "auto"))
            self.v_demo = tk.BooleanVar(value=cfg.get("demo_mode", False))

            r = 0
            self._lab(f, "Сервер:").grid(row=r, column=0, sticky="e", pady=3)
            ttk.Entry(f, textvariable=self.v_server, width=42).grid(
                row=r, column=1, sticky="w", pady=3)
            ttk.Label(f, text="например SRV01 или SRV01\\SQL2022", foreground="#90a4ae").grid(
                row=r, column=2, sticky="w")
            r += 1
            self._lab(f, "Порт:").grid(row=r, column=0, sticky="e", pady=3)
            ttk.Entry(f, textvariable=self.v_port, width=10).grid(row=r, column=1, sticky="w")
            r += 1
            self._lab(f, "База данных:").grid(row=r, column=0, sticky="e", pady=3)
            ttk.Entry(f, textvariable=self.v_db, width=42).grid(row=r, column=1, sticky="w")
            r += 1
            self._lab(f, "Аутентификация:").grid(row=r, column=0, sticky="e", pady=3)
            authf = ttk.Frame(f)
            authf.grid(row=r, column=1, sticky="w")
            ttk.Radiobutton(authf, text="Windows (доменная)", variable=self.v_win,
                            value=True, command=self._auth_switch).pack(side="left")
            ttk.Radiobutton(authf, text="SQL Server (логин/пароль)", variable=self.v_win,
                            value=False, command=self._auth_switch).pack(side="left", padx=10)
            r += 1
            self._lab(f, "Логин:").grid(row=r, column=0, sticky="e", pady=3)
            self.e_user = ttk.Entry(f, textvariable=self.v_user, width=42)
            self.e_user.grid(row=r, column=1, sticky="w")
            r += 1
            self._lab(f, "Пароль:").grid(row=r, column=0, sticky="e", pady=3)
            self.e_pwd = ttk.Entry(f, textvariable=self.v_pwd, width=42, show="•")
            self.e_pwd.grid(row=r, column=1, sticky="w")
            ttk.Checkbutton(f, text="сохранить пароль локально (не рекомендуется)",
                            variable=self.v_savepwd).grid(row=r, column=2, sticky="w")
            r += 1
            self._lab(f, "Шифрование:").grid(row=r, column=0, sticky="e", pady=3)
            encf = ttk.Frame(f)
            encf.grid(row=r, column=1, sticky="w")
            ttk.Checkbutton(encf, text="использовать TLS", variable=self.v_encrypt).pack(side="left")
            ttk.Checkbutton(encf, text="доверять самоподписанному сертификату",
                            variable=self.v_trust).pack(side="left", padx=10)
            r += 1
            self._lab(f, "Драйвер:").grid(row=r, column=0, sticky="e", pady=3)
            drvf = ttk.Frame(f)
            drvf.grid(row=r, column=1, sticky="w")
            ttk.Combobox(drvf, textvariable=self.v_driver, state="readonly", width=22,
                         values=["auto", "pyodbc (ODBC Driver)", "pymssql (FreeTDS)"]).pack(side="left")
            r += 1
            ttk.Separator(f).grid(row=r, columnspan=3, sticky="ew", pady=8)
            r += 1
            btns = ttk.Frame(f)
            btns.grid(row=r, columnspan=3, sticky="w")
            self.btn_test = ttk.Button(btns, text="Проверить подключение",
                                       command=self.test_connection)
            self.btn_test.pack(side="left")
            self.btn_save = ttk.Button(btns, text="Сохранить настройки",
                                       command=self.save_settings)
            self.btn_save.pack(side="left", padx=8)
            ttk.Checkbutton(btns, text="Демо-режим (без сервера, синтетические данные)",
                            variable=self.v_demo, command=self._demo_switch).pack(
                                side="left", padx=12)
            r += 1
            self.conn_info = ttk.Label(f, text="", foreground="#2e7d32", wraplength=700,
                                       justify="left")
            self.conn_info.grid(row=r, columnspan=3, sticky="w", pady=(8, 0))
            self._auth_switch()

        def _auth_switch(self):
            if self.v_win.get():
                self.e_user.state(["disabled"])
                self.e_pwd.state(["disabled"])
            else:
                self.e_user.state(["!disabled"])
                self.e_pwd.state(["!disabled"])

        def _demo_switch(self):
            if self.v_demo.get():
                self.conn_info.config(
                    text="Демо-режим включён: программа работает с виртуальным сервером "
                         "DEMO-SQL\\SQL2022 с реалистичными проблемами.",
                    foreground="#e65100")
                self.status_lbl.config(text="● демо-режим", foreground="#e65100")
            else:
                self.conn_info.config(text="", foreground="#2e7d32")
                self.status_lbl.config(text="● не подключено", foreground="#b71c1c")

        # ------------------------------------------------- мониторинг
        def _tab_mon(self):
            f = ttk.Frame(self.tab_mon, padding=16)
            f.pack(fill="both", expand=True)
            dash = ttk.LabelFrame(f, text="Текущие показатели (обновляются при опросе)",
                                  padding=10)
            dash.pack(fill="x")
            self.live = {}
            items = [
                ("cpu", "CPU SQL, %", "#1a5fb4"), ("syscpu", "CPU системы, %", "#e8710a"),
                ("batch", "Запросов/с", "#188038"), ("ple", "PLE, с", "#7b1fa2"),
                ("mem", "Память SQL, ГБ", "#00838f"), ("tempdb", "tempdb, МБ", "#5d4037"),
                ("blocked", "Заблокировано", "#c62828"), ("sessions", "Сессии", "#546e7a"),
            ]
            for i, (k, title, _c) in enumerate(items):
                cell = ttk.Frame(dash)
                cell.grid(row=0, column=i, padx=10, pady=2)
                lbl = ttk.Label(cell, text="—", font=("Segoe UI", 17, "bold"),
                                foreground=_c)
                lbl.pack()
                ttk.Label(cell, text=title, foreground="#5b6b7c").pack()
                self.live[k] = lbl

            self.chart = tk.Canvas(f, height=120, bg="#fbfcfe",
                                   highlightthickness=1, highlightbackground="#dde4ec")
            self.chart.pack(fill="x", pady=(10, 4))
            self.chart.bind("<Configure>", lambda e: self._draw_chart())

            ctl = ttk.LabelFrame(f, text="Суточный мониторинг", padding=10)
            ctl.pack(fill="x", pady=6)
            self.v_interval = tk.StringVar(value=str(cfg.get("interval", 60)))
            self.v_duration = tk.StringVar(value="%d ч" % cfg.get("duration_hours", 24))
            self.v_auto = tk.BooleanVar(value=cfg.get("auto_report", True))
            row1 = ttk.Frame(ctl)
            row1.pack(fill="x")
            self._lab(row1, "Интервал опроса, сек:", 20).pack(side="left")
            ttk.Spinbox(row1, from_=10, to=3600, increment=10, width=7,
                        textvariable=self.v_interval).pack(side="left", padx=(2, 14))
            self._lab(row1, "Длительность:", 14).pack(side="left")
            ttk.Combobox(row1, textvariable=self.v_duration, state="readonly", width=8,
                         values=["1 ч", "4 ч", "8 ч", "12 ч", "24 ч", "48 ч"]).pack(
                             side="left", padx=(2, 14))
            ttk.Checkbutton(row1, text="сформировать отчёт автоматически по завершении",
                            variable=self.v_auto).pack(side="left")
            row2 = ttk.Frame(ctl)
            row2.pack(fill="x", pady=(8, 0))
            self.btn_start = ttk.Button(row2, text="▶ Начать мониторинг",
                                        command=self.start_monitor)
            self.btn_start.pack(side="left")
            self.btn_stop = ttk.Button(row2, text="■ Остановить", command=self.stop_monitor,
                                       state="disabled")
            self.btn_stop.pack(side="left", padx=8)
            self.mon_lbl = ttk.Label(row2, text="мониторинг не запущен",
                                     foreground="#5b6b7c")
            self.mon_lbl.pack(side="left", padx=12)
            ttk.Label(ctl, text="Программа опрашивает только системные представления "
                                "(только чтение). Данные хранятся локально в data/monitor.db "
                                "и переживают перезапуск.",
                      foreground="#90a4ae", wraplength=900, justify="left").pack(
                          anchor="w", pady=(6, 0))

        # ------------------------------------------------- отчёт
        def _tab_rep(self):
            f = ttk.Frame(self.tab_rep, padding=16)
            f.pack(fill="both", expand=True)
            self.v_period = tk.StringVar(value="24")
            self.v_frag = tk.BooleanVar(value=cfg.get("frag_check", True))
            self.v_elog = tk.BooleanVar(value=cfg.get("errorlog_check", True))
            self.v_pxml = tk.BooleanVar(value=cfg.get("plan_xml_check", True))

            box = ttk.LabelFrame(f, text="Глубокая диагностика и отчёт", padding=10)
            box.pack(fill="x")
            ttk.Label(box, text="Отчёт включает: графики нагрузки за период, разбор причин "
                                "замедлений, ожидания, топ запросов, блокировки, индексы, "
                                "статистику, базы данных, диски, настройки, бэкапы, "
                                "журнал ошибок и рекомендации с готовыми T-SQL.",
                      wraplength=920, justify="left", foreground="#5b6b7c").pack(anchor="w")
            row = ttk.Frame(box)
            row.pack(fill="x", pady=8)
            self._lab(row, "Период графика:", 15).pack(side="left")
            ttk.Combobox(row, textvariable=self.v_period, state="readonly", width=16,
                         values=["1", "4", "8", "12", "24", "48", "168",
                                 "все накопленные"]).pack(side="left", padx=(2, 16))
            ttk.Label(row, text="часов мониторинга").pack(side="left")
            ttk.Button(row, text="📄 Сформировать отчёт",
                       command=self.make_report).pack(side="left", padx=18)
            self.btn_demo = ttk.Button(row, text="Демо-отчёт (без сервера)",
                                       command=self.make_demo_report)
            self.btn_demo.pack(side="left")
            checks_row = ttk.Frame(box)
            checks_row.pack(fill="x")
            ttk.Checkbutton(checks_row, text="фрагментация индексов (может быть долго)",
                            variable=self.v_frag).pack(side="left")
            ttk.Checkbutton(checks_row, text="журнал ошибок",
                            variable=self.v_elog).pack(side="left", padx=10)
            ttk.Checkbutton(checks_row, text="анализ планов запросов",
                            variable=self.v_pxml).pack(side="left")
            self.rep_status = ttk.Label(f, text="", foreground="#1a5fb4", wraplength=920,
                                        justify="left")
            self.rep_status.pack(anchor="w", pady=(8, 0))

            lbox = ttk.LabelFrame(f, text="Последние отчёты", padding=8)
            lbox.pack(fill="both", expand=True, pady=(10, 0))
            self.reports_list = tk.Listbox(lbox, height=7)
            self.reports_list.pack(side="left", fill="both", expand=True)
            sb = ttk.Scrollbar(lbox, command=self.reports_list.yview)
            sb.pack(side="left", fill="y")
            self.reports_list.config(yscrollcommand=sb.set)
            self.reports_list.bind("<Double-Button-1>",
                                   lambda e: self.open_selected_report())
            btns = ttk.Frame(lbox)
            btns.pack(side="left", fill="y", padx=8)
            ttk.Button(btns, text="Открыть", command=self.open_selected_report).pack(
                fill="x", pady=2)
            ttk.Button(btns, text="Обновить список",
                       command=self.refresh_reports).pack(fill="x", pady=2)
            ttk.Button(btns, text="Папка отчётов",
                       command=lambda: self._open_path(REPORTS_DIR)).pack(fill="x", pady=2)
            self.refresh_reports()

        def _tab_log(self):
            f = ttk.Frame(self.tab_log, padding=8)
            f.pack(fill="both", expand=True)
            self.log_text = tk.Text(f, wrap="none", font=("Consolas", 10),
                                    bg="#10212f", fg="#d7e4f0")
            ys = ttk.Scrollbar(f, orient="vertical", command=self.log_text.yview)
            xs = ttk.Scrollbar(f, orient="horizontal", command=self.log_text.xview)
            self.log_text.config(yscrollcommand=ys.set, xscrollcommand=xs.set)
            self.log_text.grid(row=0, column=0, sticky="nsew")
            ys.grid(row=0, column=1, sticky="ns")
            xs.grid(row=1, column=0, sticky="ew")
            f.rowconfigure(0, weight=1)
            f.columnconfigure(0, weight=1)
            self.log_text.tag_config("err", foreground="#ff8a80")
            self.log_text.tag_config("ok", foreground="#a5d6a7")
            self.log("Программа запущена. Версия %s." % VERSION)
            self.log("Подсказка: заполните вкладку «Подключение», нажмите «Проверить "
                     "подключение», затем — «Начать мониторинг» или сразу сформируйте отчёт.")

        # ------------------------------------------------- очередь/лог
        def log(self, msg, tag=""):
            self.q.put(("log", msg, tag))

        # ------------------------------------------------- подключения
        def _make_db(self):
            if self.v_demo.get():
                return self.tasks.register(demo.DemoDatabase())
            port = int(self.v_port.get() or 1433)
            pref = "auto"
            if self.v_driver.get().startswith("pyodbc"):
                pref = "pyodbc"
            elif self.v_driver.get().startswith("pymssql"):
                pref = "pymssql"
            return self.tasks.register(Database(
                server=self.v_server.get().strip(), database=self.v_db.get().strip() or "master",
                windows_auth=self.v_win.get(), user=self.v_user.get().strip(),
                password=self.v_pwd.get(), port=port,
                encrypt=self.v_encrypt.get(), trust_cert=self.v_trust.get(),
                preferred=pref))

        def test_connection(self):
            self.btn_test.state(["disabled"])
            self.conn_info.config(text="Подключение…", foreground="#5b6b7c")

            def work():
                try:
                    db = self._make_db()
                    if not isinstance(db, demo.DemoDatabase):
                        db.connect()
                    t = db.test()
                    self.db = db
                    ver = (t.get("v") or "").split("\n")[0]
                    self.q.put(("log", "Подключение успешно: %s" % ver, "ok"))
                    self.q.put(("log", "Драйвер: %s" % db.driver_name, ""))
                    self.q.put(("conn_ok", "%s\n%s" % (db.server, ver), None))
                except Exception as e:
                    self.q.put(("log", "Ошибка подключения: %s" % e, "err"))
                    self.q.put(("conn_fail", str(e), None))

            self.tasks.start(work)

        def save_settings(self):
            c = self.cfg
            c["server"] = self.v_server.get().strip()
            try:
                c["port"] = int(self.v_port.get() or 1433)
            except ValueError:
                c["port"] = 1433
            c["database"] = self.v_db.get().strip() or "master"
            c["windows_auth"] = self.v_win.get()
            c["user"] = self.v_user.get().strip()
            c["password_b64"] = encode_pwd(self.v_pwd.get()) if self.v_savepwd.get() else ""
            c["save_password"] = self.v_savepwd.get()
            c["encrypt"] = self.v_encrypt.get()
            c["trust_cert"] = self.v_trust.get()
            c["driver"] = self.v_driver.get()
            c["demo_mode"] = self.v_demo.get()
            c["interval"] = int(self.v_interval.get() or 60)
            c["duration_hours"] = int(self.v_duration.get().split()[0] or 24)
            c["auto_report"] = self.v_auto.get()
            c["frag_check"] = self.v_frag.get()
            c["errorlog_check"] = self.v_elog.get()
            c["plan_xml_check"] = self.v_pxml.get()
            save_config(c)
            self.log("Настройки сохранены: %s" % CONFIG_PATH, "ok")

        # ------------------------------------------------- мониторинг
        def _set_val(self, key, val):
            self.live[key].config(text=val)

        def _on_sample(self, s):
            self._set_val("cpu", "%.0f" % s["sql_cpu"] if s.get("sql_cpu") is not None else "—")
            self._set_val("syscpu", "%.0f" % s["sys_cpu"] if s.get("sys_cpu") is not None else "—")
            self._set_val("batch", "%.0f" % s.get("batch_req") if s.get("batch_req") is not None else "—")
            self._set_val("ple", "%d" % s.get("ple") if s.get("ple") is not None else "—")
            m = s.get("mem_total_mb")
            self._set_val("mem", "%.1f" % (m / 1024.0) if m else "—")
            self._set_val("tempdb", "%.0f" % s.get("tempdb_used_mb") if s.get("tempdb_used_mb") is not None else "—")
            bl = s.get("blocked")
            self._set_val("blocked", ("%d ⛔" % bl) if bl else "0")
            self._set_val("sessions", "%d" % s.get("sessions") if s.get("sessions") is not None else "—")
            if s.get("sql_cpu") is not None:
                self.cpu_hist.append(s["sql_cpu"])
                self.cpu_hist = self.cpu_hist[-180:]
                self._draw_chart()
            n, mn, mx = self.storage.sample_count()
            self.mon_lbl.config(
                text="тик №%d · всего точек в базе: %d · %s"
                     % (s.get("_n", "?"), n,
                        datetime.datetime.fromtimestamp(s["ts"]).strftime("%H:%M:%S")))

        def _draw_chart(self):
            c = self.chart
            try:
                c.delete("all")
            except Exception:
                return
            w = c.winfo_width() or 900
            h = c.winfo_height() or 120
            if w < 20 or not self.cpu_hist:
                c.create_text(w // 2, h // 2, text="график CPU (появится при мониторинге)",
                              fill="#90a4ae")
                return
            vals = self.cpu_hist
            step = max(1.0, (w - 10) / max(1, len(vals) - 1))
            for gy in range(0, 4):
                y = 6 + (h - 16) * gy / 3.0
                c.create_line(5, y, w - 5, y, fill="#e3e9f1")
            pts = []
            for i, v in enumerate(vals):
                y = 6 + (h - 16) * (1 - max(0, min(100, v)) / 100.0)
                pts += [5 + i * step, y]
            if len(pts) >= 4:
                c.create_line(*pts, fill="#1a5fb4", width=2)
            c.create_text(w - 8, 10, anchor="e", text="CPU SQL, %", fill="#61707f")

        def start_monitor(self):
            if self.monitor and self.monitor.is_alive():
                return
            try:
                interval = int(self.v_interval.get())
            except ValueError:
                interval = 60
            hours = int(self.v_duration.get().split()[0])
            self.save_settings_silent()

            def work():
                db = self.db
                try:
                    if db is None:
                        db = self._make_db()
                        if not isinstance(db, demo.DemoDatabase):
                            db.connect()
                            self.db = db
                    n, mn, mx = self.storage.sample_count()
                    counter = {"n": 0}

                    def on_sample(s):
                        counter["n"] += 1
                        s["_n"] = counter["n"]
                        self.q.put(("sample", s, None))

                    def on_log(msg):
                        self.q.put(("log", msg, ""))

                    def on_done(engine):
                        self.q.put(("mon_done", engine, None))

                    eng = MonitorEngine(
                        db, self.storage, interval=interval,
                        duration_s=hours * 3600,
                        long_query_s=self.cfg.get("long_query_s", 30),
                        on_sample=on_sample, on_log=on_log, on_done=on_done)
                    if self.tasks.stopping.is_set():
                        return
                    self.monitor = eng
                    self.q.put(("log", "Запуск мониторинга на %d ч (интервал %d c)…" %
                                (hours, interval), "ok"))
                    eng.start()
                except Exception as e:
                    self.q.put(("log", "Не удалось запустить мониторинг: %s" % e, "err"))

            self.tasks.start(work)
            self.btn_start.state(["disabled"])
            self.btn_stop.state(["!disabled"])

        def save_settings_silent(self):
            try:
                self.save_settings()
            except Exception:
                pass

        def stop_monitor(self):
            if self.monitor:
                self.monitor.stop()
                self.log("Останавливаю мониторинг (дождитесь завершения тика)…")
                self.btn_stop.state(["disabled"])

        def _monitor_done(self, engine):
            self.btn_start.state(["!disabled"])
            self.btn_stop.state(["disabled"])
            self.mon_lbl.config(text="мониторинг завершён · точек: %d"
                                % engine.state.get("samples", 0))
            self.log("Мониторинг завершён: %d тиков, ошибок: %d"
                     % (engine.state.get("samples", 0), engine.state.get("errors", 0)), "ok")
            if not self.tasks.stopping.is_set() and not engine._stop_event.is_set() and self.v_auto.get():
                self.log("Автоматическая генерация отчёта…")
                self.make_report()

        # ------------------------------------------------- отчёты
        def make_report(self):
            self.btn_report_busy(True)
            self.rep_status.config(text="Идёт сбор данных и анализ…")
            period = self.v_period.get()
            hours = None if period.startswith("все") else int(period)
            opts = {"fragmentation": self.v_frag.get(),
                    "errorlog": self.v_elog.get(),
                    "plan_xml": self.v_pxml.get()}
            try:
                interval = int(self.v_interval.get())
            except ValueError:
                interval = 60

            def log_cb(msg):
                self.q.put(("log", msg, ""))

            def work():
                try:
                    db = self.db
                    if db is None:
                        db = self._make_db()
                        if not isinstance(db, demo.DemoDatabase):
                            db.connect()
                            self.db = db
                    html_text = generate_report(
                        db, storage=self.storage, period_hours=hours,
                        options=opts, log_cb=log_cb, sample_interval=interval)
                    if self.tasks.stopping.is_set():
                        return
                    path = save_report(html_text)
                    self.q.put(("report_done", path, None))
                except Exception as e:
                    log.exception("report failed")
                    self.q.put(("log", "Ошибка формирования отчёта: %s" % e, "err"))
                    self.q.put(("report_fail", str(e), None))

            self.tasks.start(work)

        def make_demo_report(self):
            self.btn_report_busy(True)
            self.rep_status.config(text="Формируется демо-отчёт (виртуальный сервер)…")
            self.log("Демо-отчёт: виртуальный сервер DEMO-SQL\\SQL2022…")

            def work():
                try:
                    db = demo.DemoDatabase()
                    samples = demo.generate_demo_samples()
                    blocking, longq = demo.generate_demo_events(samples)
                    data = checks.collect_deep(db, options={"fragmentation": True,
                                                            "errorlog": True,
                                                            "plan_xml": True})
                    findings = analysis.build_findings(data, samples, blocking, longq)
                    health = analysis.health_score(findings)
                    peaks = analysis.find_slowdown_windows(samples, blocking, longq)
                    meta = {"server_display": db.server,
                            "server_full": db.server,
                            "generated_str": datetime.datetime.now().strftime(
                                "%d.%m.%Y %H:%M:%S"),
                            "interval": 60, "driver": "демо-режим"}
                    html_text = report.build_report(data, findings, health, peaks,
                                                    samples, blocking, longq, meta)
                    if self.tasks.stopping.is_set():
                        return
                    path = save_report(html_text)
                    self.q.put(("report_done", path, None))
                except Exception as e:
                    log.exception("demo report failed")
                    self.q.put(("log", "Ошибка демо-отчёта: %s" % e, "err"))
                    self.q.put(("report_fail", str(e), None))

            self.tasks.start(work)

        def btn_report_busy(self, busy):
            pass

        def _report_done(self, path, _):
            self.rep_status.config(
                text="✓ Отчёт готов: %s\nОтчёт открылся в браузере; файл также сохранён "
                     "в папке отчётов." % path, foreground="#2e7d32")
            self.log("Отчёт сохранён: %s" % path, "ok")
            self.refresh_reports()
            try:
                webbrowser.open("file://" + os.path.abspath(path))
            except Exception:
                pass

        def refresh_reports(self):
            self.reports_list.delete(0, "end")
            try:
                files = sorted(
                    [f for f in os.listdir(REPORTS_DIR) if f.endswith(".html")],
                    reverse=True)[:50]
                for f in files:
                    self.reports_list.insert("end", f)
            except Exception:
                pass

        def open_selected_report(self):
            sel = self.reports_list.curselection()
            if sel:
                self._open_path(os.path.join(REPORTS_DIR,
                                             self.reports_list.get(sel[0])))

        @staticmethod
        def _open_path(p):
            try:
                if sys.platform == "win32":
                    os.startfile(p)  # noqa
                elif sys.platform == "darwin":
                    os.system("open '%s'" % p)
                else:
                    os.system("xdg-open '%s'" % p)
            except Exception:
                pass

        # ------------------------------------------------- очередь conn_ok/fail
        # (обрабатывается в _poll через универсальный диспатчер)
        def _poll(self):
            if self.tasks.stopping.is_set():
                return
            try:
                while True:
                    item = self.q.get_nowait()
                    kind = item[0]
                    if kind == "log":
                        _, msg, tag = item
                        self.log_text.insert("end", "%s  %s\n" % (
                            datetime.datetime.now().strftime("%H:%M:%S"), msg), tag or ())
                        self.log_text.see("end")
                        log.info(msg)
                    elif kind == "sample":
                        self._on_sample(item[1])
                    elif kind == "mon_done":
                        self._monitor_done(item[1])
                    elif kind == "report_done":
                        self._report_done(item[1], None)
                    elif kind == "conn_ok":
                        self.btn_test.state(["!disabled"])
                        self.status_lbl.config(text="● подключено", foreground="#2e7d32")
                        self.conn_info.config(text="✓ Подключение успешно:\n%s" % item[1],
                                              foreground="#2e7d32")
                    elif kind == "conn_fail":
                        self.btn_test.state(["!disabled"])
                        self.status_lbl.config(text="● ошибка подключения",
                                               foreground="#b71c1c")
                        self.conn_info.config(text="✗ %s" % item[1][:600],
                                              foreground="#b71c1c")
                    elif kind == "report_fail":
                        self.rep_status.config(text="✗ Ошибка: %s" % item[1][:600],
                                               foreground="#b71c1c")
                        self.btn_report_busy(False)
            except queue.Empty:
                pass
            self._poll_id = self.after(250, self._poll)

        def on_close(self):
            if self.tasks.stopping.is_set():
                return
            import tkinter.messagebox as mb
            if not mb.askyesno("MS SQL Monitor", "Остановить все задачи мониторинга и диагностики и выйти?\nСобранные данные сохранятся."):
                return
            self.tasks.stop()
            self.after_cancel(self._poll_id)
            if self.monitor:
                self.monitor.stop()
            def disable(widget):
                for child in widget.winfo_children():
                    if isinstance(child, (ttk.Button, ttk.Entry, ttk.Checkbutton, ttk.Combobox)):
                        child.state(["disabled"])
                    disable(child)
            disable(self)
            self.bar_lbl.config(text="Завершение фоновых задач… ожидание отмены/таймаута SQL-запроса")
            self.after(100, self._wait_for_shutdown)

        def _wait_for_shutdown(self):
            # A startup worker can have created a monitor while Exit was pressed.
            if self.monitor:
                self.monitor.stop()
            if self.tasks.alive() or (self.monitor and self.monitor.is_alive()):
                self.after(100, self._wait_for_shutdown)
                return
            self.tasks.close_connections()
            self.storage.close()
            self.destroy()

    app = App()
    app.protocol("WM_DELETE_WINDOW", app.on_close)
    app.mainloop()


# =====================================================================
# CLI
# =====================================================================
def run_cli(args):
    os.makedirs(LOGS_DIR, exist_ok=True)
    logging.basicConfig(
        level=logging.INFO,
        format="%(asctime)s %(levelname)s %(message)s",
        handlers=[logging.StreamHandler(sys.stdout),
                  logging.FileHandler(os.path.join(
                      LOGS_DIR, "cli_%s.log" % datetime.date.today().strftime("%Y%m%d")),
                      encoding="utf-8")])
    if args.demo:
        db = demo.DemoDatabase()
    else:
        db = Database(server=args.server, database=args.database,
                      windows_auth=(args.auth == "windows"), user=args.user,
                      password=args.password, port=args.port,
                      encrypt=args.encrypt, trust_cert=True,
                      preferred=args.driver if args.driver != "auto" else "auto")
        db.connect()
        print("Подключено: %s" % (db.test().get("v") or "").split("\n")[0])
    storage = Storage(DB_PATH)
    interval = args.interval
    hours = args.hours

    def say(msg):
        print("  %s" % msg)

    if not args.skip_monitor:
        print("Мониторинг %d ч (интервал %d c). Ctrl+C — прервать." % (hours, interval))
        eng = MonitorEngine(db, storage, interval=interval,
                            duration_s=hours * 3600, long_query_s=args.long_query)
        eng._log = say
        try:
            eng.start()
            while eng.is_alive():
                time.sleep(1)
        except KeyboardInterrupt:
            print("Остановка…")
            eng.stop()
            eng.join(timeout=30)
    print("Сбор глубокой диагностики и формирование отчёта…")
    html_text = generate_report(
        db, storage=storage, period_hours=None,
        options={"fragmentation": not args.no_frag, "errorlog": not args.no_errorlog,
                 "plan_xml": True},
        log_cb=say, sample_interval=interval)
    path = save_report(html_text)
    print("Готово: %s" % os.path.abspath(path))
    storage.close()
    return 0


def run_demo_report(outpath):
    """Демонстрационный отчёт без сервера — также используется для тестов."""
    logging.basicConfig(level=logging.ERROR)
    db = demo.DemoDatabase()
    samples = demo.generate_demo_samples()
    blocking, longq = demo.generate_demo_events(samples)
    data = checks.collect_deep(db, options={"fragmentation": True, "errorlog": True,
                                            "plan_xml": True})
    findings = analysis.build_findings(data, samples, blocking, longq)
    health = analysis.health_score(findings)
    peaks = analysis.find_slowdown_windows(samples, blocking, longq)
    meta = {"server_display": db.server, "server_full": db.server,
            "generated_str": datetime.datetime.now().strftime("%d.%m.%Y %H:%M:%S"),
            "interval": 60, "driver": "демо-режим (без сервера)"}
    html_text = report.build_report(data, findings, health, peaks, samples,
                                    blocking, longq, meta)
    outdir = os.path.dirname(os.path.abspath(outpath))
    os.makedirs(outdir, exist_ok=True)
    with open(outpath, "w", encoding="utf-8") as f:
        f.write(html_text)
    print("Демо-отчёт: %s (%d КБ, рекомендаций: %d, оценка: %d/100)"
          % (os.path.abspath(outpath), len(html_text) // 1024, len(findings),
             health["score"]))
    return 0


def main():
    configure_console()
    p = argparse.ArgumentParser(
        prog="MSSQLMonitor",
        description="MS SQL Monitor — контроль, диагностика и оптимизация MS SQL Server")
    p.add_argument("--version", action="version", version="MS SQL Monitor " + VERSION)
    p.add_argument("--demo-report", metavar="FILE.html",
                   help="сформировать демонстрационный отчёт без сервера и выйти")
    p.add_argument("--cli", action="store_true",
                   help="консольный режим: мониторинг + отчёт (для Task Scheduler)")
    p.add_argument("--server", default="localhost", help="сервер (или сервер\\инстанс)")
    p.add_argument("--port", type=int, default=1433)
    p.add_argument("--database", default="master")
    p.add_argument("--auth", choices=["windows", "sql"], default="windows")
    p.add_argument("--user", default="sa")
    p.add_argument("--password", default="")
    p.add_argument("--driver", default="auto", choices=["auto", "pyodbc", "pymssql"])
    p.add_argument("--encrypt", action="store_true")
    p.add_argument("--hours", type=int, default=24, help="длительность мониторинга, ч")
    p.add_argument("--interval", type=int, default=60, help="интервал опроса, с")
    p.add_argument("--long-query", type=float, default=30, dest="long_query",
                   help="порог фиксации долгих запросов, с")
    p.add_argument("--skip-monitor", action="store_true",
                   help="не мониторить, только снять снимок и сделать отчёт")
    p.add_argument("--no-frag", action="store_true", help="пропустить фрагментацию")
    p.add_argument("--no-errorlog", action="store_true", help="пропустить журнал ошибок")
    p.add_argument("--demo", action="store_true", help="использовать демо-сервер")
    args = p.parse_args()

    if args.demo_report:
        sys.exit(run_demo_report(args.demo_report))
    if args.cli:
        sys.exit(run_cli(args))
    run_gui()


if __name__ == "__main__":
    main()
