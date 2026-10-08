# -*- coding: utf-8 -*-
"""
analysis.py — аналитика и рекомендации.
  * База знаний по типам ожиданий (waits) — категории, описание, действия.
  * Движок правил: data (глубокая диагностика) + samples (суточный мониторинг)
    + события блокировок/долгих запросов -> список рекомендаций.
  * Поиск «окон замедления» на таймлайне с разбором причин.
  * Оценка «здоровья» сервера (0..100).
"""

ARTICLE_DEPLOYMENT = "https://habr.com/ru/articles/1079630/"
ARTICLE_OPERATION = "https://habr.com/ru/articles/1080996/"
RCSI_ADVICE = "Сначала проверьте режим блокировок и совместимость конфигурации 1С. RCSI влияет только на READ COMMITTED, не устраняет REPEATABLE READ/SERIALIZABLE и конфликты писателей. Перевод в управляемый режим требует доработки и тестирования кода 1С. Оцените место под версии строк в tempdb (при ADR — PVS), длинные транзакции и окно обслуживания; согласуйте изменение с DBA. Не включайте принудительный откат рабочих транзакций."
PARALLEL_ADVICE = "Подберите MAXDOP по доступным online CPU и NUMA, а cost threshold — по планам, CPU и дельтам ожиданий за рабочий период. Для тяжёлой ERP параллелизм полезен отчётам; для множества бухгалтерских баз важнее конкуренция. Универсальных 1, 8 или 25–50 нет. Меняйте один параметр за раз, сравнивайте с эталонным периодом и проверяйте регрессии в Query Store. Сам по себе CXPACKET не является ошибкой."

# =====================================================================
# БАЗА ЗНАНИЙ ПО ОЖИДАНИЯМ
# =====================================================================
WAIT_CATEGORIES = {
    "cpu": {"title": "CPU и планировщик",
            "desc": "Запросы ждут доступа к процессору или связаны с параллелизмом. "
                    "Часто означает нехватку вычислительных ресурсов либо неоптимальные "
                    "параллельные планы.",
            "actions": ["Проверить загрузку CPU процессом SQL Server и ОС в целом",
                        "Настроить MAXDOP и cost threshold for parallelism",
                        "Найти самые дорогие запросы по CPU и оптимизировать их"]},
    "lock": {"title": "Блокировки",
             "desc": "Ожидание блокировок (LOCK) — одни транзакции мешают другим. "
                     "Типично для длинных транзакций и последовательного изменения "
                     "«горячих» строк.",
             "actions": [RCSI_ADVICE,
                         "Сократить длительность транзакций, проверять индексы WHERE/JOIN",
                         "Избегать изменения ключей и «горячих» таблиц длинными транзакциями"]},
    "latch": {"title": "Защёлки (latch)",
              "desc": "Внутренние ожидания структур данных SQL Server. Высокие значения "
                      "часто связаны с tempdb (PFS/GAM/SGAM) или с большим объёмом "
                      "модификаций страниц.",
              "actions": ["При PAGELATCH на страницах 2:1:1–2:1:128 — увеличить число "
                          "файлов tempdb",
                          "Проверить tempdb и тяжёлые операции сортировок/spool",
                          "Проверить память: вытеснение страниц усиливает latch-ожидания"]},
    "io": {"title": "Дисковый ввод-вывод",
            "desc": "Ожидание чтения/записи страниц с диска. Либо медленная подсистема, "
                    "либо нехватка памяти буферного пула (данные не помещаются), либо "
                    "сканирования больших таблиц.",
            "actions": ["Проверить задержки диска (менее 10–20 мс на чтение — норма)",
                        "Увеличить память / max server memory, проверить PLE",
                        "Найти запросы с большими logical/physical reads и оптимизировать",
                        "Проверить фрагментацию индексов и статистику"]},
    "network": {"title": "Сеть и клиентские приложения",
                "desc": "Сервер отдал данные, но клиент медленно их читает "
                        "(ASYNC_NETWORK_IO и подобные) — узкое место на стороне приложения.",
                "actions": ["Проверить, нет ли выбора «всей таблицы» в приложение",
                            "Оптимизировать обработку результатов на стороне клиента",
                            "Уменьшить объём возвращаемых данных (TOP, пагинация)"]},
    "memory": {"title": "Память (гранты)",
               "desc": "Ожидание памяти для выполнения запросов (RESOURCE_SEMAPHORE и др.) — "
                       "запросам не хватает памяти на сортировки/хеширования.",
               "actions": ["Проверить max server memory и PLE",
                           "Найти запросы с большими грантами памяти и оптимизировать их",
                           "Проверить оценку кардинальности/устаревшую статистику"]},
    "log": {"title": "Журнал транзакций",
            "desc": "Ожидание записи в журнал транзакций (WRITELOG и др.) — медленный "
                    "диск под файлы .ldf или слишком частые мелкие коммиты.",
            "actions": ["Разместить .ldf на быстром диске (SSD)",
                        "Проверить частоту транзакций, объём журнала и рост файлов",
                        "Проверить, делаются ли резервные копии журнала (модель FULL)"]},
    "hadr": {"title": "Высокая доступность / репликация",
             "desc": "Ожидания, связанные с AlwaysOn/репликацией (HADR_*), — транзакции "
                     "ждут синхронизации со вторичной репликой.",
             "actions": ["Проверить сетевую задержку между репликами",
                         "Рассмотреть асинхронную реплику для отчётных нагрузок",
                         "Проверить производительность диска журнала на вторичной реплике"]},
    "other": {"title": "Прочее",
              "desc": "Ожидания, не попавшие в основные категории.",
              "actions": ["Проанализировать динамику и связь с конкретными задачами"]},
}

WAIT_TYPES = {
    "SOS_SCHEDULER_YIELD": ("cpu", "Отказ от кванта процессора",
        "Запросы добровольно уступают процессор — сумма времени ожидания сигнала велика. "
        "Частый признак нехватки CPU или большого количества мелких запросов.",
        ["Проверить общую загрузку CPU и MAXDOP", "Найти топ запросов по CPU",
         "Проверить статистику и планы (сканирования «съедают» CPU)"]),
    "THREADPOOL": ("cpu", "Нет свободных рабочих потоков",
        "КРИТИЧНО: пул рабочих потоков исчерпан, новые запросы не могут начать выполняться. "
        "Обычно следствие блокировок или массовых параллельных запросов.",
        ["Проверить блокировки и взаимоблокировки немедленно",
         "Ограничить MAXDOP для крупных запросов", "Проверить max worker threads"]),
    "CXPACKET": ("cpu", "Обмен данными параллельных потоков",
        "Параллельные запросы ждут друг друга. Часть этого ожидания нормальна, но высокая "
        "доля говорит о неоптимальном параллелизме.",
        ["Проверить cost threshold for parallelism (по умолчанию 5 — часто мало)",
         "Проверить MAXDOP", "Оптимизировать сами запросы, вызывающие параллелизм"]),
    "CXCONSUMER": ("cpu", "Потребление строк параллельными потоками",
        "Сопровождает CXPACKET на новых версиях SQL Server. Оценивается вместе с CXPACKET.",
        ["Как для CXPACKET: cost threshold, MAXDOP, оптимизация запросов"]),
    "RESOURCE_SEMAPHORE": ("memory", "Ожидание гранта памяти",
        "Запросы стоят в очереди за памятью на сортировку/хеширование. Число одновременно "
        "выполняемых «тяжёлых» запросов превысило лимит памяти.",
        ["Найти запросы с большими грантами памяти", "Проверить статистику (ошибки кардинальности)",
         "Проверить объём памяти сервера"]),
    "RESOURCE_SEMAPHORE_QUERY_COMPILE": ("memory", "Очередь на компиляцию",
        "Не хватает памяти для компиляции запросов — слишком много одновременных компиляций "
        "или очень сложные запросы.",
        ["Включить optimize for ad hoc workloads", "Проверить параметризацию запросов"]),
    "CMEMTHREAD": ("memory", "Доступ к объектам памяти",
        "Конкуренция за внутренние структуры памяти — часто при очень высокой частоте "
        "мелких операций или неправильной настройке потоков.",
        ["Проверить трассировочные флаги и MAXDOP", "Профилировать горячие объекты"]),
    "ASYNC_NETWORK_IO": ("network", "Клиент не забирает данные",
        "SQL Server сформировал результат, но приложение медленно его читает — узкое место "
        "на стороне приложения или сети.",
        ["Проверить, нет ли SELECT * и выгрузки миллионов строк",
         "Оптимизировать клиентский код (буферизация, пагинация)",
         "Проверить сетевые задержки между приложением и сервером"]),
    "PAGEIOLATCH_SH": ("io", "Чтение страниц с диска (общее)",
        "Запросы читают с диска страницы данных (из-за нехватки памяти в буферном пуле или "
        "больших сканирований).",
        ["Проверить PLE и объём памяти", "Найти запросы с большими чтениями",
         "Проверить индексы и статистику"]),
    "PAGEIOLATCH_EX": ("io", "Запись страниц на диск (эксклюзивно)",
        "Модифицированные страницы сбрасываются на диск — интенсивная запись данных.",
        ["Проверить задержки записи на диске", "Проверить частоту checkpoint и объём изменений"]),
    "PAGEIOLATCH_UP": ("io", "Чтение системных страниц",
        "Обновление системных страниц (PFS/GAM и др.) читается с диска — актуально для tempdb "
        "и интенсивных вставок.",
        ["Проверить tempdb (число файлов)", "Проверить задержки диска"]),
    "WRITELOG": ("log", "Запись в журнал транзакций",
        "Транзакции ждут записи в лог. Диск под .ldf не успевает или транзакции слишком частые.",
        ["Разместить журнал на быстром SSD/NVMe", "Проверить задержку записи (<5–10 мс)",
         "Пересмотреть частоту мелких коммитов"]),
    "LOGMGR_RESERVE_APPEND": ("log", "Нет места в журнале",
        "Журнал транзакций заполнен и не может расти — проверьте рост файла и резервные "
        "копии журнала.",
        ["Проверить log_reuse_wait и автоприращение .ldf",
         "Настроить регулярные бэкапы журнала (модель FULL)"]),
    "PAGELATCH_EX": ("latch", "Защёлка на изменение страницы",
        "Конкуренция за «горячие» страницы (вставки в таблицу с identity-ключом или tempdb).",
        ["Если ресурс 2:1:* — contention в tempdb: добавить файлы",
         "Проверить «последнюю страницу» при вставках (identity/sequence)"]),
    "PAGELATCH_SH": ("latch", "Защёлка на чтение страницы", "Конкуренция за чтение страниц.",
        ["Проверить память и сканирования"]),
    "PAGELATCH_UP": ("latch", "Защёлка на обновление системных страниц",
        "Типично для contention в tempdb (PFS/GAM/SGAM).",
        ["Увеличить число файлов данных tempdb (по числу ядер, до 8)"]),
    "LATCH_EX": ("latch", "Эксклюзивная защёлка",
        "Внутренняя конкуренция при изменении структур в памяти.",
        ["Проверить tempdb и тяжёлые операции DML"]),
    "LCK_M_IS": ("lock", "Блокировка: намерение-разделяемая",
        "Запросы ждут установки блокировки намерения. Смотрите общие рекомендации по "
        "блокировкам.", ["Сократить транзакции", RCSI_ADVICE]),
    "LCK_M_S": ("lock", "Блокировка: разделяемая (S)",
        "Читатели ждут завершения пишущих транзакций.", ["Сократить транзакции", RCSI_ADVICE]),
    "LCK_M_U": ("lock", "Блокировка: обновления (U)",
        "Ожидание блокировки обновления (типично для UPDATE с поиском).",
        ["Проверить индексы по условиям WHERE у UPDATE"]),
    "LCK_M_X": ("lock", "Блокировка: эксклюзивная (X)",
        "Транзакции ждут эксклюзивного доступа — длинные пишущие транзакции блокируют других.",
        ["Сократить длительность транзакций", RCSI_ADVICE,
         "Проверить, нет ли эскалации блокировок"]),
    "LCK_M_IX": ("lock", "Блокировка: намерение-эксклюзивная",
        "Ожидание блокировки намерения на изменение.",
        ["Сократить транзакции", "Проверить индексы у операций DML"]),
    "LCK_M_SCH_M": ("lock", "Блокировка схемы (изменение)",
        "Ожидание изменения схемы — вероятно, выполняется DDL (ALTER) или перестроение.",
        ["Выполнять обслуживание индексов в окно низкой нагрузки"]),
    "OLEDB": ("network", "Вызовы OLE DB (связанные серверы)",
        "Ожидание ответа от связанных серверов (linked servers) или внешних вызовов.",
        ["Проверить производительность связанных серверов",
         "По возможности вынести запросы к linked servers в автономные задания"]),
    "HADR_SYNC_COMMIT": ("hadr", "Синхронная фиксация на реплику",
        "Каждая транзакция ждёт подтверждения записи на синхронную вторичную реплику "
        "AlwaysOn.",
        ["Проверить сеть и диск журнала на вторичной реплике",
         "Перенести отчётную нагрузку на readable secondary",
         "Рассмотреть асинхронную репликацию, если допустимо"]),
    "BACKUPIO": ("io", "Ввод-вывод резервного копирования",
        "Ожидания, связанные с записью бэкапов.",
        ["Проверить скорость устройства резервных копий", "Использовать сжатие бэкапов"]),
    "IO_COMPLETION": ("io", "Завершение операций ввода-вывода",
        "Ожидание мелких операций ввода-вывода (не страниц данных).",
        ["Проверить задержки диска, tempdb и сортировки"]),
    "ASYNC_IO_COMPLETION": ("io", "Асинхронный ввод-вывод",
        "Ожидание асинхронных операций (например, рост файлов, бэкапы).",
        ["Проверить автоприращение файлов БД (длительные автогроу)",
         "Выделить достаточный размер файлов заранее"]),
    "MSQL_DQ": ("network", "Распределённые запросы",
        "Ожидание распределённых запросов (linked servers).",
        ["Оптимизировать запросы к linked servers"]),
}

_BENIGN_PREFIX = ("BROKER_", "SLEEP_", "XE_", "QDS_", "PREEMPTIVE_XXX", "SP_SERVER",
                  "SQLTRACE_", "DIRTY_PAGE", "REDI_", "HADR_LOGCAPTURE", "HADR_DB")


def wait_info(wt):
    """Тип ожидания -> (категория, название, описание, действия)."""
    if wt in WAIT_TYPES:
        cat, title, desc, actions = WAIT_TYPES[wt]
        return cat, title, desc, actions
    if wt.startswith("LCK_M_"):
        return ("lock", "Блокировки: " + wt.replace("LCK_M_", ""),
                "Ожидание блокировки уровня %s." % wt.replace("LCK_M_", ""),
                WAIT_CATEGORIES["lock"]["actions"])
    if wt.startswith("PAGEIOLATCH_"):
        return WAIT_TYPES.get("PAGEIOLATCH_SH",
                              ("io", "PAGEIOLATCH", "", []))
    if wt.startswith("PAGELATCH_"):
        return WAIT_TYPES.get("PAGELATCH_UP",
                              ("latch", "PAGELATCH", "", []))
    if wt.startswith("LATCH_"):
        return ("latch", wt, "Внутренние защёлки SQL Server.",
                WAIT_CATEGORIES["latch"]["actions"])
    if wt.startswith("PREEMPTIVE_"):
        return ("other", wt, "Ожидание внешних вызовов ОС (preemptive).",
                ["Проверить внешние операции (linked servers, файловые операции)"])
    if wt.startswith("HADR_"):
        return WAIT_TYPES["HADR_SYNC_COMMIT"]
    if wt.startswith("LCK_"):
        return ("lock", wt, "Ожидание блокировки.", WAIT_CATEGORIES["lock"]["actions"])
    return ("other", wt, "Прочий тип ожидания.", WAIT_CATEGORIES["other"]["actions"])


def wait_category_totals(rows, top_n=None):
    """rows = [{wait_type, wait_ms,...}] -> {cat: ms} и топ по категориям."""
    totals = {}
    for r in rows:
        cat = wait_info(r.get("wait_type") or "")[0]
        totals[cat] = totals.get(cat, 0.0) + (r.get("wait_ms") or 0.0)
    return totals


# =====================================================================
# ПЕРЦЕНТИЛИ И УТИЛИТЫ
# =====================================================================
def _percentile(sorted_vals, p):
    if not sorted_vals:
        return None
    k = int(round(p * (len(sorted_vals) - 1)))
    return sorted_vals[min(k, len(sorted_vals) - 1)]


def _fnum(v):
    try:
        return float(v) if v is not None else None
    except (TypeError, ValueError):
        return None


def _dt_h(v):
    """datetime -> часы назад (float) или None."""
    import datetime as _d
    if isinstance(v, _d.datetime):
        return (_d.datetime.now() - v).total_seconds() / 3600.0
    return None


def _fmt_ms(ms):
    if ms is None:
        return "—"
    if ms >= 3600000:
        return "%.1f ч" % (ms / 3600000.0)
    if ms >= 60000:
        return "%.1f мин" % (ms / 60000.0)
    return "%.0f с" % (ms / 1000.0)


# =====================================================================
# ОКНА ЗАМЕДЛЕНИЯ (анализ суточного графика)
# =====================================================================
def find_slowdown_windows(samples, blocking_events=None, long_queries=None):
    """Находит интервалы повышенной нагрузки/проблем и объясняет их причины."""
    out = []
    if not samples or len(samples) < 10:
        return out
    blocking_events = blocking_events or []
    long_queries = long_queries or []

    cpu = sorted(s["sql_cpu"] for s in samples if _fnum(s.get("sql_cpu")) is not None)
    batch = sorted(s["batch_req"] for s in samples if _fnum(s.get("batch_req")) is not None)
    ple = sorted(s["ple"] for s in samples if _fnum(s.get("ple")) is not None)
    cpu_hi = _percentile(cpu, 0.85)
    batch_hi = _percentile(batch, 0.85)
    ple_lo = _percentile(ple, 0.10)

    flags = []
    for s in samples:
        f = 0
        c, b = _fnum(s.get("sql_cpu")), _fnum(s.get("batch_req"))
        if c is not None and cpu_hi is not None and c >= max(cpu_hi, 50):
            f |= 1
        if b is not None and batch_hi is not None and batch_hi > 0 and b >= batch_hi * 1.2:
            f |= 1
        if (s.get("blocked") or 0) > 0:
            f |= 2
        p = _fnum(s.get("ple"))
        if p is not None and ple_lo is not None and p < 300:
            f |= 1
        flags.append(f)

    # объединяем подряд идущие проблемные точки (допуск 2 «нормальных»)
    windows = []
    i = 0
    n = len(samples)
    while i < n:
        if flags[i] == 0:
            i += 1
            continue
        j = i
        gap = 0
        while j + 1 < n and gap <= 2:
            j += 1
            if flags[j] != 0:
                gap = 0
            else:
                gap += 1
        if gap > 2 and j >= 1 and flags[j] == 0:
            j -= gap
        # убираем хвостовые нули
        while j > i and flags[j] == 0:
            j -= 1
        if j - i >= 2:
            windows.append((i, j))
        i = j + 1

    for (a, b) in windows:
        seg = samples[a:b + 1]
        t0, t1 = seg[0]["ts"], seg[-1]["ts"]
        cpus = [_fnum(s.get("sql_cpu")) for s in seg]
        cpus = [c for c in cpus if c is not None]
        waits = {}
        for s in seg:
            for (wt, wms, _tasks, _sig) in (s.get("waits") or []):
                waits[wt] = waits.get(wt, 0.0) + (wms or 0.0)
        bl = [e for e in blocking_events if t0 <= e["ts"] <= t1]
        lq = [e for e in long_queries if t0 <= e["ts"] <= t1]
        cats = wait_category_totals([{"wait_type": k, "wait_ms": v} for k, v in waits.items()])
        w = {
            "t0": t0, "t1": t1, "n": len(seg),
            "cpu_avg": sum(cpus) / len(cpus) if cpus else None,
            "cpu_max": max(cpus) if cpus else None,
            "batch_avg": (sum(_fnum(s.get("batch_req")) or 0 for s in seg) /
                          max(1, len([s for s in seg if _fnum(s.get("batch_req")) is not None]))),
            "ple_min": min((_fnum(s.get("ple")) or 10 ** 9) for s in seg),
            "waits": sorted(waits.items(), key=lambda kv: -kv[1])[:8],
            "wait_cats": cats, "blocked": bl, "long": lq,
            "blocked_cnt": len({(e.get("session_id"), e.get("blocking_session_id"))
                                for e in bl}),
        }
        w["reasons"] = _window_reasons(w)
        out.append(w)

    # самые «тяжёлые» окна вперёд
    out.sort(key=lambda w: (-(w["blocked_cnt"] or 0), -(w["cpu_max"] or 0), -w["n"]))
    return out[:6]


def _window_reasons(w):
    reasons = []
    total = sum(v for _k, v in w["wait_cats"].items()) or 1.0
    cats = sorted(w["wait_cats"].items(), key=lambda kv: -kv[1])
    for cat, ms in cats[:3]:
        if ms / total < 0.1:
            continue
        share = ms / total * 100
        cat_ru = WAIT_CATEGORIES[cat]["title"]
        if cat == "cpu":
            reasons.append("Высокая доля ожиданий CPU/параллелизма (%s — %.0f%% времени окна): "
                           "вероятна нехватка процессорных ресурсов или неоптимальные "
                           "параллельные планы." % (cat_ru, share))
        elif cat == "io":
            reasons.append("Доминируют ожидания дискового ввода-вывода (%.0f%%): данные "
                           "читаются с диска — проверьте память (PLE), индексы и быстродействие "
                           "накопителей." % share)
        elif cat == "lock":
            reasons.append("Значительная доля блокировок (%.0f%%): в этот момент одни "
                           "транзакции ожидали других." % share)
        elif cat == "memory":
            reasons.append("Ожидания грантов памяти (%.0f%%): запросам не хватало памяти на "
                           "сортировку/хеширование." % share)
        elif cat == "network":
            reasons.append("Ожидания на клиенте (%.0f%%): приложения медленно забирали данные."
                           % share)
        elif cat == "log":
            reasons.append("Ожидания записи в журнал транзакций (%.0f%%): диск под .ldf не "
                           "успевал за потоком транзакций." % share)
        elif cat == "latch":
            reasons.append("Внутренние защёлки (%.0f%%): возможен contention в tempdb или на "
                           "«горячих» страницах." % share)
        elif cat == "hadr":
            reasons.append("Ожидания репликации (%.0f%%): фиксация транзакций ждала синхронную "
                           "реплику." % share)
    for wt, ms in w["waits"][:3]:
        if wt in ("SOS_SCHEDULER_YIELD", "THREADPOOL", "CXPACKET", "CXCONSUMER",
                  "PAGEIOLATCH_SH", "WRITELOG", "ASYNC_NETWORK_IO",
                  "RESOURCE_SEMAPHORE", "LCK_M_X", "LCK_M_IX", "LCK_M_S", "PAGELATCH_UP"):
            _cat, title, desc, _acts = wait_info(wt)
            reasons.append("%s: %s" % (title, desc))
            break
    if w["blocked_cnt"]:
        reasons.append("Зафиксировано эпизодов блокировок: %d. Смотрите раздел «Блокировки» — "
                       "там приведены цепочки на этот период." % len(w["blocked"]))
    if w["long"]:
        top = sorted(w["long"], key=lambda q: -(q.get("elapsed_s") or 0))[:3]
        reasons.append("Выполнялись долгие запросы: " + "; ".join(
            "сессия %s — %s с (%s)" % (q.get("session_id"), q.get("elapsed_s"),
                                       (q.get("text_head") or "")[:60]) for q in top))
    if w["ple_min"] and w["ple_min"] < 300:
        reasons.append("Page life expectancy опускался до %d с (<300 — эвристика). Сопоставьте с эталоном и физическими чтениями." % w["ple_min"])
    if not reasons:
        reasons.append("Повышенная активность без явных ожиданий: вероятно, плановая нагрузка "
                       "(отчёты, задания). Проверьте, какие задания запускались в это время.")
    return reasons[:6]


# =====================================================================
# ПРАВИЛА-РЕКОМЕНДАЦИИ
# =====================================================================
def _finding(fid, sev, cat, title, evidence, why, action, sql=None):
    return {"id": fid, "severity": sev, "cat": cat, "title": title,
            "evidence": [str(e) for e in evidence], "why": why,
            "action": action, "sql": sql}


def build_findings(data, samples=None, blocking_events=None, long_queries=None):
    """Главная функция: data от collect_deep + таймлайн -> список рекомендаций."""
    samples = samples or []
    blocking_events = blocking_events or []
    long_queries = long_queries or []
    F = []
    cfg = {r.get("name"): r for r in (data.get("configs") or [])}
    sysinfo = data.get("sys_info") or {}
    phys_kb = _fnum(sysinfo.get("phys_kb"))
    phys_gb = phys_kb / 1048576.0 if phys_kb else None
    cpu_count = _inum(sysinfo.get("cpu_count"))

    # Resource limits require measured workload and NUMA context.
    m = cfg.get("max server memory (MB)") or {}
    cur = _fnum(m.get("value_in_use"))
    if phys_gb and cur is not None and (cur >= 2147483647 or cur > phys_gb * 1024 * .95):
        F.append(_finding("MEM-01", "warning", "Память", "Проверьте резерв памяти ОС",
            ["max server memory = %s МБ; ОЗУ = %.1f ГБ" % (cur, phys_gb), ARTICLE_DEPLOYMENT],
            "Неограниченный лимит или резерв менее 5% требует проверки давления на ОС.",
            "Задайте лимит по пику потребления ОС, служб и других инстансов, а не фиксированным 75%. Пример статьи: резерв 6 ГБ при 8 ядрах/32 ГБ; это не универсальная формула. Проверьте LPIM и резерв памяти гипервизора.", None))
    for name, fid in [("max degree of parallelism", "DOP-01"), ("cost threshold for parallelism", "DOP-03")]:
        cur = _fnum((cfg.get(name) or {}).get("value_in_use"))
        if cur is not None and ((name.startswith("max") and (cur in (0, 1) or (cpu_count and cur > cpu_count))) or (name.startswith("cost") and cur <= 5)):
            F.append(_finding(fid, "info", "Параллелизм", name + ": проверить профиль нагрузки",
                ["Фактически: %s" % cur, ARTICLE_OPERATION],
                "Значение параметра само по себе не доказывает причину замедления.", PARALLEL_ADVICE, None))

    # ---------- 4. optimize for ad hoc workloads ----------
    try:
        m = cfg.get("optimize for ad hoc workloads")
        if m is not None and _fnum(m.get("value_in_use")) == 0:
            pc = {r.get("objtype"): r for r in (data.get("plan_cache") or [])}
            adhoc = pc.get("Adhoc") or {}
            if (_fnum(adhoc.get("cnt")) or 0) > 2000:
                F.append(_finding(
                    "CACHE-01", "warning", "План кэша",
                    "optimize for ad hoc workloads выключен, в кэше много ad-hoc планов",
                    ["Одноразовых (Adhoc) планов в кэше: %s" % int(_fnum(adhoc.get("cnt")) or 0),
                     "Отключите лишние одноразовые планы, включив параметр"],
                    "Ad-hoc запросы (без параметризации) засоряют кэш планов и отнимают "
                    "память у буферного пула.",
                    "Включите optimize for ad hoc workloads — в кэше будет храниться только "
                    "«заглушка» плана при первом выполнении.",
                    "EXEC sys.sp_configure N'show advanced options', 1; RECONFIGURE;\n"
                    "EXEC sys.sp_configure N'optimize for ad hoc workloads', 1; RECONFIGURE;"))
            else:
                F.append(_finding(
                    "CACHE-02", "info", "План кэша",
                    "optimize for ad hoc workloads выключен",
                    ["Adhoc-планов в кэше: %s" % int(_fnum(adhoc.get("cnt")) or 0)],
                    "При большом количестве непараметризованных запросов кэш планов "
                    "переполняется.",
                    "Включите параметр — это безопасно и снижает потребление памяти.",
                    "EXEC sys.sp_configure N'show advanced options', 1; RECONFIGURE;\n"
                    "EXEC sys.sp_configure N'optimize for ad hoc workloads', 1; RECONFIGURE;"))
    except Exception:
        pass

    # ---------- 5. опасные параметры ----------
    try:
        for name, sev, why, act in (
                ("priority boost", "warning",
                 "SQL Server работает с повышенным приоритетом процесса — может «заморозить» ОС.",
                 "Выключите параметр: EXEC sys.sp_configure N'priority boost', 0; RECONFIGURE;"),
                ("lightweight pooling", "warning",
                 "Fiber-режим включён — Microsoft не рекомендует, вызывает проблемы с "
                 "планированием и синхронизацией.",
                 "Выключите: EXEC sys.sp_configure N'lightweight pooling', 0; RECONFIGURE;"),
                ("xp_cmdshell", "info",
                 "xp_cmdshell включён — расширяет поверхность атаки.",
                 "Если не используется — выключите: EXEC sys.sp_configure N'xp_cmdshell', 0; "
                 "RECONFIGURE;"),
                ("clr enabled", "info",
                 "CLR включён; с SQL 2017 используйте STRICT mode.",
                 "Проверьте, что используются только доверенные сборки."),
        ):
            m = cfg.get(name)
            if m is not None and _fnum(m.get("value_in_use")) == 1:
                F.append(_finding("CFG-" + name[:6].replace(" ", "").upper(), sev,
                                  "Конфигурация", "Параметр «%s» включён" % name,
                                  ["Текущее значение: %s" % m.get("value_in_use")],
                                  why, act, act.split(": ", 1)[-1] if ": " in act else None))
    except Exception:
        pass

    # ---------- 6. signal wait / CPU ----------
    try:
        ws = data.get("wait_stats") or {}
        total_ms = ws.get("total_ms") or 0
        signal_ms = ws.get("signal_ms") or 0
        if total_ms > 0 and signal_ms / total_ms > 0.2:
            F.append(_finding(
                "WAIT-SIGNAL", "warning", "CPU",
                "Высокий signal wait: %.0f%% времени ожидания" % (100 * signal_ms / total_ms),
                ["Signal wait: %s из %s суммарных ожиданий" %
                 (_fmt_ms(signal_ms), _fmt_ms(total_ms))],
                "Signal wait — время готовности запроса к запуску в очереди планировщика. "
                "Доля выше 20% — классический признак нехватки процессорных мощностей.",
                "Проверьте загрузку CPU, уменьшите нагрузку от тяжёлых запросов, проверьте "
                "MAXDOP и статистику.", None))
    except Exception:
        pass

    # ---------- 7. доминирующие ожидания ----------
    try:
        ws = data.get("wait_stats") or {}
        rows = ws.get("rows") or []
        cats = wait_category_totals(rows)
        total = sum(cats.values()) or 1.0
        for cat, ms in sorted(cats.items(), key=lambda kv: -kv[1]):
            share = ms / total
            if share < 0.15 or ms < 60000:
                continue
            share *= 100
            if cat == "io":
                F.append(_finding(
                    "WAIT-IO", "warning", "Ввод-вывод",
                    "Ожидания диска доминируют: %.0f%% (%s)" % (share, _fmt_ms(ms)),
                    ["Топ: " + ", ".join("%s (%s)" % (r["wait_type"], _fmt_ms(r["wait_ms"]))
                                         for r in rows[:5] if wait_info(r["wait_type"])[0] == "io")],
                    "Данные часто читаются с диска: либо буферному пулу не хватает памяти, "
                    "либо подсистема хранения медленная, либо запросы сканируют лишние данные.",
                    "1) Проверьте PLE и память. 2) Проверьте задержки диска (раздел "
                    "«Файлы и диски»). 3) Оптимизируйте топ-запросы по чтениям и создайте "
                    "недостающие индексы.", None))
            elif cat == "lock":
                F.append(_finding(
                    "WAIT-LOCK", "warning", "Блокировки",
                    "Ожидания блокировок: %.0f%% (%s)" % (share, _fmt_ms(ms)),
                    ["Топ: " + ", ".join("%s (%s)" % (r["wait_type"], _fmt_ms(r["wait_ms"]))
                                         for r in rows[:5] if wait_info(r["wait_type"])[0] == "lock")],
                    "Транзакции ждут друг друга — пользовательские запросы «тормозят» из-за "
                    "конкуренции за строки/таблицы.",
                    RCSI_ADVICE, None))
            elif cat == "cpu" and "WAIT-CPU" not in [f["id"] for f in F]:
                F.append(_finding(
                    "WAIT-CPU", "warning", "CPU",
                    "Ожидания CPU/параллелизма: %.0f%% (%s)" % (share, _fmt_ms(ms)),
                    ["Топ: " + ", ".join("%s (%s)" % (r["wait_type"], _fmt_ms(r["wait_ms"]))
                                         for r in rows[:5] if wait_info(r["wait_type"])[0] == "cpu")],
                    "Сервер проводит много времени в ожидании процессора или координации "
                    "параллельных планов.",
                    "Настройте MAXDOP/cost threshold, оптимизируйте самые дорогие по CPU "
                    "запросы (раздел «Топ запросов»), проверьте статистику.", None))
            elif cat == "memory":
                F.append(_finding(
                    "WAIT-MEM", "warning", "Память",
                    "Ожидания грантов памяти: %.0f%% (%s)" % (share, _fmt_ms(ms)),
                    ["Основные типы: " + ", ".join(r["wait_type"] for r in rows[:5]
                                                   if wait_info(r["wait_type"])[0] == "memory")],
                    "Запросам не хватает памяти для сортировок и хеширования — они стоят в "
                    "очереди RESOURCE_SEMAPHORE.",
                    "Проверьте max server memory, обновите статистику (ошибки кардинальности "
                    "раздувают гранты), оптимизируйте запросы с сортировками.", None))
            elif cat == "network":
                F.append(_finding(
                    "WAIT-NET", "info", "Клиентские приложения",
                    "Ожидания на стороне клиента: %.0f%% (%s)" % (share, _fmt_ms(ms)),
                    ["Основной тип: ASYNC_NETWORK_IO"],
                    "Сервер сформировал результат, но приложения медленно читают данные.",
                    "Найдите запросы, возвращающие огромные выборки; добавьте пагинацию/"
                    "TOP; проверьте сеть и обработку на клиенте.", None))
            elif cat == "log":
                F.append(_finding(
                    "WAIT-LOG", "warning", "Журнал транзакций",
                    "Ожидания записи в журнал: %.0f%% (%s)" % (share, _fmt_ms(ms)),
                    ["Основной тип: WRITELOG"],
                    "Диск под файлами журнала (.ldf) не справляется с потоком транзакций.",
                    "Разместите .ldf на быстром SSD/NVMe, проверьте задержки записи, "
                    "проверьте частоту коммитов в приложении.", None))
            break  # достаточно самого значимого
    except Exception:
        pass

    # ---------- 8. давление на память по таймлайну ----------
    try:
        ples = [_fnum(s.get("ple")) for s in samples]
        ples = [p for p in ples if p is not None]
        if ples:
            ple_min = min(ples)
            below = len([p for p in ples if p < 300])
            if below >= max(3, len(ples) * 0.1):
                F.append(_finding(
                    "MEM-PRESS", "info", "Память",
                    "Page life expectancy регулярно ниже 300 с (минимум %d)" % ple_min,
                    ["Точек с PLE<300: %d из %d" % (below, len(ples))],
                    "300 с — эвристика, не универсальный норматив PLE.",
                    "Сравните с эталоном системы, lazy writes и физическими чтениями. Проверьте планы и сканы до решения об увеличении памяти.", None))
        lazy = [_fnum(s.get("lazy_writes")) for s in samples]
        lazy = [x for x in lazy if x is not None]
        if lazy and sum(lazy) / len(lazy) > 20:
            F.append(_finding(
                "MEM-LAZY", "warning", "Память",
                "Lazy writer работает постоянно (в среднем %.0f стр/с)" %
                (sum(lazy) / len(lazy)),
                ["Это означает активное вытеснение страниц из памяти"],
                "Буферный пул переполнен — SQL Server вынужден постоянно сбрасывать "
                "страницы на диск.",
                "Увеличьте память для SQL Server или снизьте объём чтений.", None))
    except Exception:
        pass

    # ---------- 9. блокировки ----------
    try:
        if blocking_events:
            sev = "critical" if len(blocking_events) > 50 else "warning"
            chains = {}
            for e in blocking_events:
                chains.setdefault(e.get("blocking_session_id"), set()).add(e.get("session_id"))
            worst_ev = "—"
            if chains:
                worst = max(chains.items(), key=lambda kv: len(kv[1]))
                worst_ev = "сессия %s блокировала %d других сессий" % (worst[0], len(worst[1]))
            F.append(_finding(
                "BLK-01", sev, "Блокировки",
                "Зафиксированы блокировки: %d эпизодов за период" % len(blocking_events),
                ["Уникальных «блокирующих» сессий: %d" % len(chains),
                 "Худшая: %s" % worst_ev],
                "Одни сессии удерживали блокировки, другие ждали. Это прямая причина "
                "«зависаний» запросов у пользователей.",
                "Найдите блокирующую сессию, сократите транзакцию и проверьте индексы. " + RCSI_ADVICE, None))
        live = data.get("requests_live") or {}
        if (live.get("blockers") or []) and not blocking_events:
            F.append(_finding(
                "BLK-02", "warning", "Блокировки",
                "Прямо сейчас есть блокировки (%d сессий-виновников)" % len(live["blockers"]),
                ["Сессии: " + ", ".join(str(b.get("session_id")) for b in live["blockers"][:10])],
                "На момент снятия диагностики часть запросов была заблокирована.",
                "Изучите запросы виновников в разделе «Текущие сессии».", None))
    except Exception:
        pass

    # ---------- 10. взаимоблокировки ----------
    try:
        dls = [d for d in (data.get("deadlocks") or [])
               if not _dt_h(d.get("time")) or _dt_h(d.get("time")) <= 48]
        if dls:
            objs = {}
            for d in dls:
                for o in d.get("objects") or []:
                    objs[o] = objs.get(o, 0) + 1
            F.append(_finding(
                "DL-01", "warning" if len(dls) <= 5 else "critical", "Взаимоблокировки",
                "Взаимоблокировки за последние 48 ч: %d" % len(dls),
                ["Затронутые объекты: " + ", ".join("%s (%d)" % kv for kv in
                                                    sorted(objs.items(), key=lambda x: -x[1])[:8])],
                "Две сессии блокируют друг друга, одна откатывается. Чаще всего причина — "
                "разный порядок доступа к таблицам или отсутствие подходящих индексов.",
                "1) Выровняйте порядок доступа к таблицам в приложении. "
                "2) Проверьте индексы по условиям конфликтных запросов. "
                "3) " + RCSI_ADVICE,
                None))
    except Exception:
        pass

    # ---------- 11. отсутствующие индексы ----------
    try:
        mi = data.get("missing_indexes") or []
        good = [r for r in mi if (_fnum(r.get("user_seeks")) or 0) >= 100
                and (_fnum(r.get("avg_user_impact")) or 0) >= 60]
        if good:
            top = good[:7]
            F.append(_finding(
                "IDX-01", "warning", "Индексы",
                "Отсутствующие индексы: %d рекомендаций с высокой эффективностью" % len(good),
                ["Например: %s (seeks: %s, ожидаемый эффект: %s%%)" %
                 ((r.get("obj") or "?").split(".")[-1], int(_fnum(r.get("user_seeks")) or 0),
                  int(_fnum(r.get("avg_user_impact")) or 0)) for r in top[:4]],
                "Оптимизатор сообщил бы о значительном ускорении, если бы существовали "
                "перечисленные индексы. Это одна из самых частых причин медленных запросов.",
                "Создайте индексы по готовым DDL из раздела «Отсутствующие индексы» "
                "(проверьте на тестовой среде; учитывайте накладные расходы на INSERT/UPDATE).",
                top[0].get("ddl") or None))
    except Exception:
        pass

    # ---------- 12. фрагментация ----------
    try:
        frag = data.get("fragmentation") or []
        bad = [r for r in frag if (_fnum(r.get("frag")) or 0) >= 30]
        if bad:
            F.append(_finding(
                "IDX-02", "warning" if len(bad) > 5 else "info", "Индексы",
                "Сильная фрагментация: %d индексов (≥30%%)" % len(bad),
                ["Топ: %s — %s%%" % (r.get("ix_name"), int(_fnum(r.get("frag")) or 0))
                 for r in bad[:4]],
                "Логическое чтение фрагментированных индексов медленнее, больше упреждающих "
                "чтений и памяти.",
                "Перестройте (REBUILD) индексы с фрагментацией ≥30%, при 10–30% — "
                "реорганизуйте (REORGANIZE). Полные DDL — в разделе «Фрагментация».",
                "-- Пример:\nALTER INDEX [имя] ON [схема].[таблица] REBUILD;"))
        mid = [r for r in frag if 10 <= (_fnum(r.get("frag")) or 0) < 30]
        if mid:
            F.append(_finding(
                "IDX-03", "info", "Индексы",
                "Умеренная фрагментация: %d индексов (10–30%%)" % len(mid),
                ["Эти индексы достаточно реорганизовать (REORGANIZE), без REBUILD"],
                "Умеренная фрагментация слегка снижает производительность чтений.",
                "ALTER INDEX [имя] ON [схема].[таблица] REORGANIZE;", None))
    except Exception:
        pass

    # ---------- 13. устаревшая статистика ----------
    try:
        st = []
        for r in (data.get("stats") or []):
            rows_ = _fnum(r.get("rows")) or 0
            mods = _fnum(r.get("modification_counter")) or 0
            lu_h = _dt_h(r.get("last_updated"))
            if rows_ > 10000 and (mods > 0.25 * rows_ or
                                  (lu_h and lu_h > 336 and mods > 0.05 * rows_)):
                st.append(r)
        if st:
            F.append(_finding(
                "STAT-01", "warning", "Статистика",
                "Устаревшая статистика: %d объектов" % len(st),
                ["Топ: %s.%s.%s — изменено %s строк из %s, обновлялась %s" %
                 ((r.get("db") or "?").split(".")[-1], r.get("tbl"), r.get("stat_name"),
                  int(_fnum(r.get("modification_counter")) or 0), int(_fnum(r.get("rows")) or 0),
                  r.get("last_updated")) for r in sorted(
                      st, key=lambda r: -(_fnum(r.get("modification_counter")) or 0))[:4]],
                "Устаревшая статистика даёт оптимизатору неверные оценки кардинальности — "
                "выбираются плохие планы, растут гранты памяти и CPU. Типичная причина "
                "«внезапных» замедлений запроса.",
                "Обновите статистику по проблемным таблицам (или по всей базе в окно "
                "обслуживания).",
                "UPDATE STATISTICS [схема].[таблица] WITH FULLSCAN; -- по таблице\n"
                "EXEC sp_updatestats; -- вся база (быстрее, менее точно)"))
    except Exception:
        pass

    # ---------- 14. неиспользуемые индексы ----------
    try:
        un = data.get("unused_indexes") or []
        if un:
            tot_mb = sum(_fnum(r.get("size_mb")) or 0 for r in un)
            F.append(_finding(
                "IDX-04", "info", "Индексы",
                "Неиспользуемые индексы: %d шт (%.0f МБ)" % (len(un), tot_mb),
                ["Топ по объёму: %s.%s — %s МБ, %s записей изменений" %
                 (r.get("tbl"), r.get("ix_name"), int(_fnum(r.get("size_mb")) or 0),
                  int(_fnum(r.get("updates")) or 0)) for r in
                 sorted(un, key=lambda r: -(_fnum(r.get("size_mb")) or 0))[:4]],
                "Индексы не используются для чтения, но каждое изменение таблицы обновляет "
                "их — лишние записи, память и журнал.",
                "Счётчики сбрасываются при перезапуске сервера — сначала убедитесь, что "
                "прошёл репрезентативный период (несколько недель). Затем отключите "
                "(DISABLE) ненужные индексы и наблюдайте.",
                "ALTER INDEX [имя] ON [схема].[таблица] DISABLE; -- отключить\n"
                "DROP INDEX [имя] ON [схема].[таблица]; -- удалить"))
    except Exception:
        pass

    # ---------- 15. неявные преобразования ----------
    try:
        pi = data.get("plan_issues") or {}
        convs = pi.get("converts") or []
        if convs:
            F.append(_finding(
                "PLAN-CONV", "warning", "Планы запросов",
                "Неявные преобразования типов в %d топовых планах" % len(convs),
                ["Пример: %s (%s)" % (c.get("issue"), (c.get("expr") or "")[:90])
                 for c in convs[:3]],
                "CONVERT в предикатах мешает использованию индексов (index seek превращается "
                "в scan) и искажает оценки. Классика: сравнение NVARCHAR-параметра с "
                "VARCHAR-колонкой.",
                "Приведите типы параметров к типам колонок (или наоборот), проверьте "
                "сопоставление (collation) и типы в ORM/клиенте.", None))
        if pi.get("spills"):
            F.append(_finding(
                "PLAN-SPILL", "warning", "Планы запросов",
                "Проливы в tempdb в %d планах (SpillToTempDb)" % len(pi["spills"]),
                ["Пролив означает, что памяти на сортировку/хеш не хватило, и данные ушли "
                 "в tempdb"],
                "Сортировки и хеширования выполняются в tempdb вместо памяти — это на порядок "
                "медленнее.",
                "Обновите статистику, проверьте запрос (лишние сортировки), проверьте "
                "память сервера.", None))
        dup = (data.get("qstats") or {}).get("dup_plans") or []
        if dup:
            F.append(_finding(
                "PLAN-DUP", "info", "Планы запросов",
                "Много планов для одного запроса (возможен parameter sniffing)",
                ["%s разных планов для запроса: %s..." % (d.get("plan_count"),
                                                          (d.get("sql_text") or "")[:60])
                 for d in dup[:3]],
                "Один и тот же запрос (один query_hash) имеет множество планов — обычно "
                "это «разнюхивание параметров» или непараметризованные значения.",
                "Включите Query Store, найдите регрессии планов; рассмотрите "
                "OPTION (RECOMPILE) или OPTIMIZE FOR для проблемных запросов.", None))
    except Exception:
        pass

    # ---------- 16. долгие запросы ----------
    try:
        lq = long_queries or []
        if lq:
            top = sorted(lq, key=lambda q: -(q.get("elapsed_s") or 0))[:5]
            F.append(_finding(
                "Q-LONG", "info", "Запросы",
                "Долгие запросы за период: %d (дольше порога)" % len(lq),
                ["Топ: сессия %s — %s c: %s" % (q.get("session_id"), q.get("elapsed_s"),
                                                (q.get("text_head") or "")[:70])
                 for q in top[:4]],
                "Эти запросы работали дольше всех; подробные тексты — в разделе "
                "«Долгие запросы».",
                "Проверьте их планы (SET STATISTICS IO, TIME ON; актуальный план выполнения), "
                "индексы и статистику.", None))
    except Exception:
        pass

    # ---------- 17. базы данных ----------
    try:
        dbs = (data.get("databases") or {}).get("databases") or []
        compat = [d for d in dbs if not d.get("is_system") and
                  (_inum(d.get("compatibility_level")) or 0) < 130]
        if compat:
            major = _inum((data.get("server") or {}).get("major")) or 0
            F.append(_finding(
                "DB-COMPAT", "info", "Базы данных",
                "Устаревший уровень совместимости: %d баз" % len(compat),
                ["Базы: " + ", ".join("%s (%s)" % (d["name"], d.get("compatibility_level"))
                                      for d in compat[:6])],
                "Старый уровень совместимости отключает новый оптимизатор кардинальности "
                "(CE) и новые оптимизации.",
                "Повысьте уровень до версии сервера после тестирования (разница CE может "
                "менять планы).",
                "ALTER DATABASE [%s] SET COMPATIBILITY_LEVEL = %d;" %
                (compat[0]["name"], max(130, min(major * 10, 160)) if major else 130)))
        verify = [d for d in dbs if not d.get("is_system") and
                  (d.get("page_verify_option_desc") or "") != "CHECKSUM"]
        if verify:
            F.append(_finding(
                "DB-VERIFY", "warning", "Базы данных",
                "CHECKSUM выключен в %d базах" % len(verify),
                ["Базы: " + ", ".join(d["name"] for d in verify[:6])],
                "Без CHECKSUM повреждённые страницы не выявляются при чтении/бэкапе.",
                "ALTER DATABASE [%s] SET PAGE_VERIFY CHECKSUM;" % verify[0]["name"],
                "ALTER DATABASE [%s] SET PAGE_VERIFY CHECKSUM;" % verify[0]["name"]))
        ash = [d for d in dbs if not d.get("is_system") and _inum(d.get("is_auto_shrink_on")) == 1]
        if ash:
            F.append(_finding(
                "DB-SHRINK", "warning", "Базы данных",
                "AUTO_SHRINK включён: %s" % ", ".join(d["name"] for d in ash[:5]),
                ["AUTO_SHRINK постоянно уменьшает файлы и тут же они растут снова"],
                "Постоянные сжатия/росты фрагментируют файлы и нагружают диск.",
                "ALTER DATABASE [%s] SET AUTO_SHRINK OFF;" % ash[0]["name"],
                "ALTER DATABASE [%s] SET AUTO_SHRINK OFF;" % ash[0]["name"]))
        ac = [d for d in dbs if not d.get("is_system") and _inum(d.get("is_auto_close_on")) == 1]
        if ac:
            F.append(_finding(
                "DB-CLOSE", "warning", "Базы данных",
                "AUTO_CLOSE включён: %s" % ", ".join(d["name"] for d in ac[:5]),
                ["База закрывается, когда нет подключений, и открывается заново при каждом "
                 "подключении"],
                "Каждое новое подключение оплачивает запуск базы — лишние задержки.",
                "ALTER DATABASE [%s] SET AUTO_CLOSE OFF;" % ac[0]["name"],
                "ALTER DATABASE [%s] SET AUTO_CLOSE OFF;" % ac[0]["name"]))
        vlf = [d for d in dbs if (_inum(d.get("vlf")) or 0) > 1000]
        if vlf:
            F.append(_finding(
                "DB-VLF", "warning", "Базы данных",
                "Слишком много VLF в журнале: %s" % ", ".join(
                    "%s (%d)" % (d["name"], d.get("vlf")) for d in vlf[:5]),
                ["Виртуальных файлов журнала (VLF) больше 1000"],
                "Тысячи VLF замедляют восстановление базы, операции с журналом и бэкапы.",
                "Сожмите журнал до минимума и создайте заново одним приращением "
                "(в окно обслуживания).",
                "USE [%s];\nDBCC SHRINKFILE (лог_имя, 64);\n"
                "ALTER DATABASE [%s] MODIFY FILE ( NAME = лог_имя, SIZE = 8GB );"
                % (vlf[0]["name"], vlf[0]["name"])))
        # log full / reuse wait
        full = [d for d in dbs if not d.get("is_system") and
                (_fnum(d.get("log_used_pct")) or 0) > 80 and
                (d.get("recovery_model_desc") or "") == "FULL"]
        for d in full[:3]:
            F.append(_finding(
                "DB-LOG", "warning", "Журнал транзакций",
                "Журнал базы %s заполнен на %s%%" % (d["name"], int(d.get("log_used_pct"))),
                ["recovery_model: FULL, log_reuse_wait: %s" % d.get("log_reuse_wait_desc")],
                "Файл журнала почти заполнен. При исчерпании места запись в базу остановится.",
                "Проверьте регулярность бэкапов журнала (если FULL), размер и автоприращение "
                "файла .ldf.", None))
        # FULL без бэкапов журнала
        for d in dbs:
            if (d.get("recovery_model_desc") or "") != "FULL" or d.get("is_system"):
                continue
            b = next((x for x in (data.get("backups") or []) if x.get("db") == d["name"]), None)
            if b and not b.get("last_log") and b.get("last_full"):
                F.append(_finding(
                    "DB-LOGBACKUP", "warning", "Резервное копирование",
                    "База %s: модель FULL, но бэкапы журнала не делаются" % d["name"],
                    ["Последняя полная копия: %s" % b.get("last_full")],
                    "Без бэкапов журнала файл .ldf растёт бесконечно и не усекается.",
                    "Настройте регулярные бэкапы журнала транзакций (каждые 15–60 минут).",
                    None))
                break
    except Exception:
        pass

    # ---------- 18. бэкапы ----------
    try:
        bks = data.get("backups") or []
        old = []
        for b in bks:
            if (b.get("last_full_ago_h") or 0) > 168 or not b.get("last_full"):
                old.append(b)
        if old:
            F.append(_finding(
                "BK-01", "critical" if len(old) >= 2 else "warning",
                "Резервное копирование",
                "Нет свежих полных бэкапов: %d баз" % len(old),
                ["Базы: " + ", ".join(b["db"] for b in old[:6])],
                "Риск полной потери данных при сбое диска. Это самый приоритетный пункт.",
                "Настройте регулярные полные (ежедневно/еженедельно) + разностные или "
                "журнальные бэкапы.", None))
    except Exception:
        pass

    # ---------- 19. задания SQL Agent ----------
    try:
        jobs = data.get("jobs_failed") or []
        if jobs:
            F.append(_finding(
                "JOB-01", "warning", "Обслуживание",
                "Неудачные задания SQL Agent: %d за 2 дня" % len(jobs),
                ["Пример: %s — %s" % (j.get("job"), (j.get("message_head") or "")[:80])
                 for j in jobs[:3]],
                "Часть задач обслуживания/интеграции падает. Среди них могут быть бэкапы и "
                "обслуживание индексов — это влияет на производительность.",
                "Проверьте историю заданий в SSMS (SQL Server Agent → Jobs) и устраните "
                "причины сбоев.", None))
    except Exception:
        pass

    # ---------- 20. tempdb ----------
    try:
        td = data.get("tempdb") or {}
        datafiles = [f for f in (td.get("files") or []) if (f.get("type_desc") or "") == "ROWS"]
        if cpu_count and datafiles and len(datafiles) < min(8, cpu_count):
            F.append(_finding(
                "TEMP-01", "warning", "tempdb",
                "Файлов данных tempdb: %d (ядер: %d)" % (len(datafiles), cpu_count),
                ["Файлы: " + ", ".join(f.get("name") or "" for f in datafiles[:8])],
                "При конкурентном доступе возникают конфликты за системные страницы "
                "(PAGELATCH_UP на 2:1:*), что замедляет все сортировки и временные таблицы.",
                "Создайте по одному файлу данных на ядро (до 8) одинакового размера с "
                "одинаковым автоприращением.",
                "ALTER DATABASE tempdb ADD FILE ( NAME = N'tempdev2', "
                "FILENAME = N'X:\\tempdb\\tempdev2.ndf', SIZE = 8GB, FILEGROWTH = 512MB );"))
        sp = (td.get("space") or {})
        ver_mb = _fnum(sp.get("version_mb")) or 0
        if ver_mb > 1024:
            tr = td.get("snapshot_tr") or []
            F.append(_finding(
                "TEMP-02", "warning", "tempdb",
                "Version store в tempdb: %.0f МБ" % ver_mb,
                ["Старейшая транзакция снимков: сессия %s, %s с" %
                 ((tr[0].get("session_id") if tr else "?"),
                  (tr[0].get("elapsed_time_seconds") if tr else "?"))],
                "Длинные транзакции в базах с изоляцией снимков удерживают версионные данные "
                "в tempdb, раздувая её.",
                "Найдите и сократите длинные транзакции с уровнем изоляции SNAPSHOT/RCSI.",
                None))
    except Exception:
        pass

    # ---------- 21. журнал ошибок ----------
    try:
        el = data.get("errorlog") or []
        io833 = [e for e in el if "taking longer than" in (e.get("text") or "")]
        if io833:
            F.append(_finding(
                "ELOG-833", "warning", "Диски",
                "Предупреждения о медленном вводе-выводе (833): %d" % len(io833),
                ["Пример: %s" % (io833[0].get("text") or "")[:140]],
                "Диски не возвращают результаты операций за 15 секунд. Замедляются все "
                "операции с базой.",
                "Проверьте подсистему хранения (нагрузка SAN, кэш контроллера, очередь), "
                "разнесите файлы данных/журнала по разным томам.", None))
        grow = [e for e in el if "autogrow" in (e.get("text") or "")]
        if grow:
            F.append(_finding(
                "ELOG-GROW", "info", "Файлы",
                "События автоприращения файлов: %d" % len(grow),
                ["Пример: %s" % (grow[0].get("text") or "")[:140]],
                "Частые автоприращения останавливают запросы на время роста файла.",
                "Задайте файлам достаточный размер заранее и фиксированный шаг приращения "
                "(в МБ, не в %).", None))
        mem = [e for e in el if "memory" in (e.get("text") or "").lower()
               and "not enough" in (e.get("text") or "").lower()]
        if mem:
            F.append(_finding(
                "ELOG-MEM", "critical", "Память",
                "Ошибки нехватки памяти в журнале: %d" % len(mem),
                ["Пример: %s" % (mem[0].get("text") or "")[:140]],
                "SQL Server испытывал дефицит памяти для внутренних операций — возможны "
                "сбои запросов.",
                "Проверьте max server memory, потребление памяти другими процессами, "
                "рассмотрите увеличение ОЗУ.", None))
    except Exception:
        pass

    # ---------- 22. задержки дисков ----------
    try:
        rows = data.get("io_files") or []
        slow = []
        for r in rows:
            rd = _fnum(r.get("num_of_reads")) or 0
            wr = _fnum(r.get("num_of_writes")) or 0
            rl = (_fnum(r.get("io_stall_read_ms")) or 0) / rd if rd > 100 else 0
            wl = (_fnum(r.get("io_stall_write_ms")) or 0) / wr if wr > 100 else 0
            if rl > 25 or wl > 40:
                slow.append({"db": r.get("db"), "f": r.get("logical_name"),
                             "rl": rl, "wl": wl})
        if slow:
            slow.sort(key=lambda x: -max(x["rl"], x["wl"]))
            F.append(_finding(
                "IO-SLOW", "warning", "Диски",
                "Медленные файлы базы: %d (чтение >25 мс / запись >40 мс)" % len(slow),
                ["%s: чтение %.0f мс, запись %.0f мс" % (s["f"] or s["db"], s["rl"], s["wl"])
                 for s in slow[:5]],
                "Подсистема хранения не обеспечивает типовые задержки (норма: чтение "
                "<10–20 мс, запись <10–20 мс). Все ожидания ввода-вывода усиливаются.",
                "Проверьте настройки SAN/контроллера, очереди дисков, перенесите «горячие» "
                "файлы на SSD, разнесите данные и журнал по разным томам.", None))
    except Exception:
        pass

    # ---------- 23. место на дисках ----------
    try:
        rows = data.get("volumes") or []
        low = [r for r in rows if (_fnum(r.get("total_gb")) or 0) > 0 and
               (_fnum(r.get("free_gb")) or 0) < 0.1 * (_fnum(r.get("total_gb")) or 0)]
        if low:
            F.append(_finding(
                "VOL-FREE", "warning", "Диски",
                "Мало свободного места: %d томов (<10%%)" % len(low),
                ["%s: свободно %s ГБ из %s ГБ" % (r.get("volume_mount_point"),
                                                  r.get("free_gb"), r.get("total_gb"))
                 for r in low[:5]],
                "При заполнении тома файлы баз перестанут расти — запись остановится.",
                "Освободите место или расширьте томы; проверьте, что бэкапы не хранятся "
                "на томах баз.", None))
    except Exception:
        pass

    # ---------- 24. автоприращение в процентах ----------
    try:
        files = (data.get("databases") or {}).get("files") or []
        pct = [f for f in files if _inum(f.get("is_percent_growth")) == 1]
        tiny = [f for f in files if _inum(f.get("is_percent_growth")) == 0
                and 0 < (_fnum(f.get("growth")) or 0) * 8 / 1024 < 64]  # <64 МБ
        if pct:
            F.append(_finding(
                "FILE-GROW", "info", "Файлы",
                "Автоприращение в процентах: %d файлов" % len(pct),
                ["Пример: %s (%s) — рост %s%%" % (f.get("logical_name"), f.get("db"),
                                                  f.get("growth")) for f in pct[:4]],
                "Процентный рост непредсказуем: большие файлы растут гигабайтами "
                "(долгие паузы), маленькие — слишком часто.",
                "Установите фиксированный прирост (например, 256–512 МБ для данных, "
                "512–1024 МБ для журнала).", None))
        if tiny:
            F.append(_finding(
                "FILE-TINY", "info", "Файлы",
                "Очень маленький шаг автоприращения: %d файлов (<64 МБ)" % len(tiny),
                ["Пример: %s (%s) — шаг %s×8КБ" % (f.get("logical_name"), f.get("db"),
                                                   f.get("growth")) for f in tiny[:4]],
                "Файл растёт слишком часто — каждая операция роста останавливает запись.",
                "Увеличьте шаг приращения до 256–512 МБ.", None))
    except Exception:
        pass

    # ---------- 25. Query Store / RCSI ----------
    try:
        qs = data.get("query_store") or []
        off = [r for r in qs if (r.get("state") or "") not in ("READ_WRITE", "READ_ONLY")]
        if off and len(off) <= len(qs) and qs:
            F.append(_finding(
                "QS-01", "info", "Планы запросов",
                "Query Store выключен в %d базах" % len(off),
                ["Базы: " + ", ".join(r["db"] for r in off[:6])],
                "Query Store хранит историю планов и статистику выполнения — незаменим "
                "для поиска регрессий производительности.",
                "ALTER DATABASE [%s] SET QUERY_STORE = ON;" % off[0]["db"],
                "ALTER DATABASE [%s] SET QUERY_STORE = ON;" % off[0]["db"]))
    except Exception:
        pass

    # ---------- 26. компиляции ----------
    try:
        comp = [_fnum(s.get("compiles_sec")) for s in samples]
        batch = [_fnum(s.get("batch_req")) for s in samples]
        comp = [x for x in comp if x is not None]
        batch = [x for x in batch if x is not None]
        if comp and batch:
            avg_c = sum(comp) / len(comp)
            avg_b = sum(batch) / len(batch)
            if avg_b > 50 and avg_c / max(avg_b, 1) > 0.15:
                F.append(_finding(
                    "COMPILE-01", "warning", "Компиляция",
                    "Высокая доля компиляций: %.0f/с при %s запросах/с" % (avg_c, avg_b),
                    ["Каждый 6-й и более запрос компилируется заново"],
                    "Компиляция требует CPU и блокирует кэш планов — признак "
                    "непараметризованных запросов.",
                    "Проверьте параметризацию в приложении; включите optimize for ad hoc "
                    "workloads; для типовых нагрузок рассмотрите принудительную "
                    "параметризацию.", None))
    except Exception:
        pass

    # ---------- 27. uptime ----------
    try:
        st = sysinfo.get("sqlserver_start_time")
        h = _dt_h(st)
        if h is not None and h < 72:
            F.append(_finding(
                "UP-01", "info", "Обслуживание",
                "Сервер перезапускался %.0f ч назад" % h,
                ["Статистика ожиданий и счётчики индексов накоплены с момента запуска"],
                "Часть данных (wait stats, usage индексов) ещё не репрезентативна.",
                "Повторите диагностику через несколько дней нормальной работы.", None))
    except Exception:
        pass

    # The legacy Python report has no workload selector: don't infer the 1C
    # isolation mode or recommend enabling RCSI from instance-wide waits.
    for f in F:
        if f["id"].startswith("WAIT-"):
            f["severity"] = "info"
            f["evidence"].append("Накопленные ожидания с перезапуска; не доказательство текущей проблемы. " + ARTICLE_OPERATION)
            f["action"] += " Сопоставьте с дельтами за рабочий период и спокойным эталоном."

    # порядок: критично -> важно -> инфо
    order = {"critical": 0, "warning": 1, "info": 2}
    F.sort(key=lambda f: order.get(f["severity"], 3))
    for i, f in enumerate(F, 1):
        f["num"] = i
    return F


def _inum(v):
    try:
        if v is None:
            return None
        return int(v)
    except (TypeError, ValueError):
        try:
            return int(float(v))
        except Exception:
            return None


def health_score(findings):
    crit = len([f for f in findings if f["severity"] == "critical"])
    warn = len([f for f in findings if f["severity"] == "warning"])
    info = len([f for f in findings if f["severity"] == "info"])
    score = max(0, min(100, 100 - crit * 14 - warn * 5 - info * 1))
    if score >= 90:
        grade = "Отличное состояние"
    elif score >= 75:
        grade = "Хорошее состояние"
    elif score >= 55:
        grade = "Требует внимания"
    elif score >= 35:
        grade = "Серьёзные проблемы"
    else:
        grade = "Критическое состояние"
    return {"score": int(score), "grade": grade, "crit": crit,
            "warn": warn, "info": info}


SEV_TITLES = {"critical": "Критично", "warning": "Важно", "info": "Рекомендация"}
SEV_ICONS = {"critical": "⛔", "warning": "⚠️", "info": "💡"}
