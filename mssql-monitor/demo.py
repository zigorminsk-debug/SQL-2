# -*- coding: utf-8 -*-
"""
demo.py — демонстрационный режим без реального SQL Server.
DemoDatabase подменяет драйвер и отвечает реалистичными данными,
в том числе «живыми» счётчиками, зависящими от времени суток.
Используется: кнопкой «Демо-отчёт» в GUI, флагом --demo-report и
для тестирования конвейера отчёта.
"""
import datetime
import random
import time

# ---------------------------------------------------------------- утилиты
def _now():
    return datetime.datetime.now()


def _ago(**kw):
    return _now() - datetime.timedelta(**kw)


def _hour():
    n = _now()
    return n.hour + n.minute / 60.0 + n.second / 3600.0


def _in(h, a, b):
    return a <= h < b if a < b else (h >= a or h < b)


# «дневная» нагрузка
def _load(h):
    if _in(h, 9.6, 10.9):          # утренний пик (отчёты)
        return 1.0
    if _in(h, 14.9, 15.6):         # послеобеденные блокировки
        return 0.75
    if 9 <= h < 19:
        return 0.5 + 0.1
    if 7 <= h < 9 or 19 <= h < 22:
        return 0.3
    return 0.12                    # ночь


def _cpu(h):
    base = 8 + 55 * _load(h)
    if _in(h, 9.6, 10.9):
        base += 18
    return min(98.0, base + 3 * (1 if int(h * 7) % 2 else -1))


def _batch(h):
    return 30 + 380 * _load(h)


SQL_TEXTS = [
    "SELECT o.Id, o.Number, c.Name FROM dbo.Orders o JOIN dbo.Customers c ON "
    "c.Id = o.CustomerId WHERE o.CreatedAt >= @1 AND o.Status = @2 ORDER BY o.CreatedAt DESC",
    "exec dbo.usp_CalcDailyReport @Date = '2026-09-30', @Region = 4",
    "UPDATE TOP (500) o SET o.Processed = 1, o.UpdatedBy = SUSER_SNAME() "
    "FROM dbo.Orders o WHERE o.Processed = 0",
    "INSERT INTO dbo.ReportCache (KeyHash, Payload) VALUES (@k, @p)",
    "SELECT * FROM dbo.Events WHERE Name LIKE @p1 AND CreatedAt > @p2",
    "DELETE FROM dbo.Sessions WHERE ExpiresAt < GETDATE()",
]

LOGIN = "CORP\\srv-report"
PROG = ".Net SqlClient Data Provider"
HOST_ = "APP-SRV-07"


class DemoDatabase(object):
    """Подменяет db.Database: те же методы, данные синтетические."""

    server = "DEMO-SQL\\SQL2022"
    driver_name = "демо-режим (без сервера)"

    def __init__(self):
        self._t0 = time.time()
        self._perf_cum = {"batch": 4712000.0, "trans": 9200000.0, "comp": 402000.0,
                          "recomp": 21000.0, "lazy": 88000.0, "scan": 129000.0,
                          "isearch": 5230000.0, "lwait": 4410000.0}
        # стартовые накопленные ожидания (профиль «проблемного» сервера, ~11 дней аптайма)
        self._wait_cum = {
            "PAGEIOLATCH_SH": 34 * 260000.0, "CXPACKET": 16 * 260000.0,
            "CXCONSUMER": 8 * 260000.0, "SOS_SCHEDULER_YIELD": 7 * 260000.0,
            "LCK_M_IX": 2.6 * 260000.0, "LCK_M_S": 1.4 * 260000.0,
            "WRITELOG": 7 * 260000.0, "ASYNC_NETWORK_IO": 6 * 260000.0,
            "RESOURCE_SEMAPHORE": 3 * 260000.0, "PAGELATCH_UP": 2 * 260000.0,
            "BACKUPIO": 1 * 260000.0, "OLEDB": 2 * 260000.0,
        }

    def query_any(self, sqls):
        for s in sqls:
            try:
                return self.query(s)
            except Exception:
                continue
        return self.query(sqls[-1])

    # ------------------------------------------------ служебные
    def close(self):
        pass

    def test(self):
        return {"srv": "DEMO-SQL", "v": self._ver_row()["v"]}

    def server_now(self):
        return _now()

    def server_utc_offset_minutes(self):
        return 180

    def query_one(self, sql):
        rows = self.query(sql)
        return rows[0] if rows else None

    def query_iter(self, sql, cap=60000, batch=2000):
        for r in self.query(sql)[:cap]:
            yield r

    # ------------------------------------------------ данные
    def _ver_row(self):
        return {"srv": "DEMO-SQL\\SQL2022",
                "v": ("Microsoft SQL Server 2022 (RTM-CU14) (KB5038325) - 16.0.4165.4 (X64) "
                      "Build 16.0.4165.4: Standard Edition (64-bit) on Windows Server 2019 "
                      "Standard 10.0 <X64> (Build 17763:)\n\t(c) Microsoft Corporation. "
                      "All rights reserved.\n"),
                "inst": "SQL2022", "edition": "Standard Edition (64-bit)",
                "ver": "16.0.4165.4", "level": "RTM", "major": "16", "cu": "CU14",
                "machine": "DEMO-SQL", "clustered": 0,
                "collation": "Cyrillic_General_CI_AS"}

    def query(self, sql):
        s = sql.strip()
        h = _hour()
        U = s.upper()

        # ---------- лёгкий опрос ----------
        if "RING_BUFFER_SCHEDULER_MONITOR" in U:
            idle = max(1, int(100 - _cpu(h) * 1.08))
            return [{"rec": ("<Record id='3' type='RING_BUFFER_SCHEDULER_MONITOR' "
                             "time='%d'><SchedulerMonitorEvent><System>"
                             "<ProcessUtilization>%d</ProcessUtilization>"
                             "<SystemIdle>%d</SystemIdle>"
                             "<MemoryUtilization>100</MemoryUtilization>"
                             "</System></SchedulerMonitorEvent></Record>"
                             % (int(time.time()), int(_cpu(h)), idle))}]
        if "DM_OS_PERFORMANCE_COUNTERS" in U:
            dt = max(1.0, time.time() - self._t0)
            self._t0 = time.time()
            ld = _load(h)
            rates = {
                "batch": _batch(h),
                "trans": _batch(h) * 3.2,
                "comp": _batch(h) * 0.11 if not _in(h, 9.6, 10.9) else _batch(h) * 0.22,
                "recomp": _batch(h) * 0.012,
                "lazy": 45 if _in(h, 9.6, 10.9) else (8 if ld > 0.4 else 1),
                "scan": 25 + 300 * ld,
                "isearch": 3000 + 22000 * ld,
                "lwait": 3500 if _in(h, 14.9, 15.6) else 120 * ld,
            }
            for k, r in rates.items():
                self._perf_cum[k] += r * dt
            c = self._perf_cum
            bchr = 97.9 if not _in(h, 9.6, 10.9) else 94.3
            rows = [
                ("MSSQL$SQL2022:SQL Statistics", "Batch Requests/sec", "", c["batch"], 272696576),
                ("MSSQL$SQL2022:Databases", "Transactions/sec", "_Total", c["trans"], 272696576),
                ("MSSQL$SQL2022:SQL Statistics", "SQL Compilations/sec", "", c["comp"], 272696576),
                ("MSSQL$SQL2022:SQL Statistics", "SQL Re-Compilations/sec", "", c["recomp"], 272696576),
                ("MSSQL$SQL2022:Buffer Manager", "Page life expectancy", "",
                 265 if _in(h, 9.6, 10.9) else (1620 if _load(h) < 0.2 else 540), 65792),
                ("MSSQL$SQL2022:Buffer Manager", "Buffer cache hit ratio", "", bchr * 740, 1073874176),
                ("MSSQL$SQL2022:Buffer Manager", "Buffer cache hit ratio base", "", 740.0, 1073874176),
                ("MSSQL$SQL2022:Buffer Manager", "Lazy Writes/sec", "", c["lazy"], 272696576),
                ("MSSQL$SQL2022:Access Methods", "Full Scans/sec", "", c["scan"], 272696576),
                ("MSSQL$SQL2022:Access Methods", "Index Searches/sec", "", c["isearch"], 272696576),
                ("MSSQL$SQL2022:Memory Manager", "Target Server Memory (KB)", "", 25165824, 65792),
                ("MSSQL$SQL2022:Memory Manager", "Total Server Memory (KB)", "", 24510000, 65792),
                ("MSSQL$SQL2022:Locks", "Lock Wait Time (ms)", "_Total", c["lwait"], 272696576),
            ]
            return [dict(zip(("object_name", "counter_name", "instance_name",
                              "cntr_value", "cntr_type"), r)) for r in rows]

        if "DM_OS_WAIT_STATS" in U:
            dt = max(1.0, time.time() - self._t0) if not self._wait_cum else 60.0
            profile = {"PAGEIOLATCH_SH": 34, "CXPACKET": 16, "CXCONSUMER": 8,
                       "SOS_SCHEDULER_YIELD": 7, "LCK_M_IX": 2, "LCK_M_S": 2,
                       "WRITELOG": 7, "ASYNC_NETWORK_IO": 6, "RESOURCE_SEMAPHORE": 3,
                       "PAGELATCH_UP": 2, "BACKUPIO": 1, "OLEDB": 2}
            if _in(h, 14.9, 15.6):
                profile = {"LCK_M_IX": 38, "LCK_M_S": 22, "PAGEIOLATCH_SH": 8,
                           "CXPACKET": 6, "WRITELOG": 6, "SOS_SCHEDULER_YIELD": 4,
                           "ASYNC_NETWORK_IO": 5}
            if _in(h, 9.6, 10.9):
                profile = {"PAGEIOLATCH_SH": 40, "CXPACKET": 20, "CXCONSUMER": 10,
                           "SOS_SCHEDULER_YIELD": 9, "RESOURCE_SEMAPHORE": 5,
                           "WRITELOG": 3, "ASYNC_NETWORK_IO": 4}
            for k, share in profile.items():
                cur = self._wait_cum.get(k, 10 ** 6 + 1000 * len(k))
                self._wait_cum[k] = cur + share * 800.0
            rows = []
            base = {"WAITFOR": 10 ** 9, "SLEEP_TASK": 10 ** 9, "BROKER_TASK_STOP": 10 ** 8,
                    "DIRTY_PAGE_POLL": 10 ** 8, "HADR_FILESTREAM_IOMGR_IOCOMPLETION": 10 ** 7,
                    "XE_TIMER_EVENT": 10 ** 8, "LAZYWRITER_SLEEP": 10 ** 9,
                    "LOGMGR_QUEUE": 10 ** 8, "CHECKPOINT_QUEUE": 10 ** 8,
                    "BROKER_TO_FLUSH": 10 ** 8, "SOS_WORK_DISPATCHER": 10 ** 8}
            for k, v in base.items():
                rows.append({"wait_type": k, "waiting_tasks_count": int(v / 100),
                             "wait_time_ms": float(v), "signal_wait_time_ms": v * 0.02,
                             "max_wait_time_ms": 5200.0})
            for k, v in self._wait_cum.items():
                rows.append({"wait_type": k, "waiting_tasks_count": int(v / 350),
                             "wait_time_ms": float(v), "signal_wait_time_ms": v * 0.21,
                             "max_wait_time_ms": 32000.0})
            return rows

        if "DM_EXEC_REQUESTS" in U:
            return self._requests(h)

        if "COUNT(*) AS CNT FROM SYS.DM_EXEC_SESSIONS" in U or (
                "DM_EXEC_SESSIONS" in U and "COUNT(*)" in U):
            return [{"cnt": int(38 + 26 * _load(h))}]

        if "DM_IO_VIRTUAL_FILE_STATS" in U and "JOIN SYS.MASTER_FILES" in U:
            return [
                {"db": "SalesDB", "logical_name": "SalesDB_data", "type_desc": "ROWS",
                 "num_of_reads": 920000000, "io_stall_read_ms": 31400000000,
                 "num_of_writes": 41000000, "io_stall_write_ms": 590000000,
                 "size_on_disk_bytes": 64 * 10 ** 9,
                 "physical_name": "E:\\Data\\SalesDB_data.mdf"},
                {"db": "SalesDB", "logical_name": "SalesDB_log", "type_desc": "LOG",
                 "num_of_reads": 1200000, "io_stall_read_ms": 14000000,
                 "num_of_writes": 210000000, "io_stall_write_ms": 2520000000,
                 "size_on_disk_bytes": 15 * 10 ** 9,
                 "physical_name": "F:\\Logs\\SalesDB_log.ldf"},
                {"db": "ReportDB", "logical_name": "ReportDB", "type_desc": "ROWS",
                 "num_of_reads": 143000000, "io_stall_read_ms": 4110000000,
                 "num_of_writes": 8100000, "io_stall_write_ms": 129000000,
                 "size_on_disk_bytes": 32 * 10 ** 9,
                 "physical_name": "E:\\Data\\ReportDB.mdf"},
                {"db": "WarehouseDB", "logical_name": "WH", "type_desc": "ROWS",
                 "num_of_reads": 61000000, "io_stall_read_ms": 1450000000,
                 "num_of_writes": 3100000, "io_stall_write_ms": 42000000,
                 "size_on_disk_bytes": 107 * 10 ** 9,
                 "physical_name": "G:\\WH\\WH.mdf"},
                {"db": "tempdb", "logical_name": "tempdev", "type_desc": "ROWS",
                 "num_of_reads": 41000000, "io_stall_read_ms": 520000000,
                 "num_of_writes": 21000000, "io_stall_write_ms": 152000000,
                 "size_on_disk_bytes": 8 * 10 ** 9,
                 "physical_name": "D:\\TempDB\\tempdev.mdf"},
            ]

        if "SOURCE_DATABASE_ID IS NULL" in U:
            return [{"name": "SalesDB"}, {"name": "ReportDB"}, {"name": "WarehouseDB"}]

        if "DM_IO_VIRTUAL_FILE_STATS" in U:
            rows = [
                {"database_id": 2, "r": 41000000, "r_ms": 520000000, "w": 21000000,
                 "w_ms": 152000000, "rb": 41 * 10 ** 11, "wb": 12 * 10 ** 11},
                {"database_id": 5, "r": 920000000, "r_ms": 31400000000, "w": 41000000,
                 "w_ms": 590000000, "rb": 92 * 10 ** 11, "wb": 31 * 10 ** 11},
                {"database_id": 6, "r": 143000000, "r_ms": 4110000000, "w": 8100000,
                 "w_ms": 129000000, "rb": 18 * 10 ** 11, "wb": 6 * 10 ** 11},
                {"database_id": 7, "r": 61000000, "r_ms": 1450000000, "w": 3100000,
                 "w_ms": 42000000, "rb": 8 * 10 ** 11, "wb": 2 * 10 ** 11},
            ]
            return rows

        if "TEMPDB.SYS.DM_DB_FILE_SPACE_USAGE" in U:
            return [{"user_mb": 2310.0, "internal_mb": 980.0,
                     "version_mb": 2600.0 if _in(h, 14.9, 15.6) else 320.0,
                     "free_mb": 4120.0}]

        # ---------- глубокая диагностика ----------
        if "@@SERVERNAME" in U:
            return [self._ver_row()]
        if "DM_HADR_INSTANCE_REPLICA_STATES" in U:
            return [{"n": 0}]
        if "FROM SYS.CONFIGURATIONS" in U:
            return [
                {"name": "max server memory (MB)", "value_in_use": 2147483647,
                 "value": 2147483647, "minimum": 16, "maximum": 2147483647,
                 "is_dynamic": 1, "is_advanced": 1},
                {"name": "min server memory (MB)", "value_in_use": 0, "value": 0,
                 "minimum": 0, "maximum": 2147483647, "is_dynamic": 1, "is_advanced": 1},
                {"name": "max degree of parallelism", "value_in_use": 0, "value": 0,
                 "minimum": 0, "maximum": 32767, "is_dynamic": 1, "is_advanced": 1},
                {"name": "cost threshold for parallelism", "value_in_use": 5, "value": 5,
                 "minimum": 0, "maximum": 32767, "is_dynamic": 1, "is_advanced": 1},
                {"name": "optimize for ad hoc workloads", "value_in_use": 0, "value": 0,
                 "minimum": 0, "maximum": 1, "is_dynamic": 1, "is_advanced": 1},
                {"name": "max worker threads", "value_in_use": 0, "value": 0,
                 "minimum": 128, "maximum": 65535, "is_dynamic": 1, "is_advanced": 1},
                {"name": "clr enabled", "value_in_use": 0, "value": 0, "minimum": 0,
                 "maximum": 1, "is_dynamic": 1, "is_advanced": 1},
                {"name": "xp_cmdshell", "value_in_use": 0, "value": 0, "minimum": 0,
                 "maximum": 1, "is_dynamic": 1, "is_advanced": 1},
                {"name": "priority boost", "value_in_use": 0, "value": 0, "minimum": 0,
                 "maximum": 1, "is_dynamic": 0, "is_advanced": 1},
                {"name": "lightweight pooling", "value_in_use": 0, "value": 0,
                 "minimum": 0, "maximum": 1, "is_dynamic": 0, "is_advanced": 1},
                {"name": "remote query timeout (s)", "value_in_use": 600, "value": 600,
                 "minimum": 0, "maximum": 2147483647, "is_dynamic": 1, "is_advanced": 0},
                {"name": "blocked process threshold (s)", "value_in_use": 0, "value": 0,
                 "minimum": 0, "maximum": 86400, "is_dynamic": 1, "is_advanced": 1},
            ]
        if "DM_OS_SYS_INFO" in U:
            return [{"cpu_count": 16, "hyperthread_ratio": 2,
                     "sqlserver_start_time": _ago(days=11, hours=3),
                     "phys_kb": 34359738368, "committed_kb": 25100000,
                     "committed_target_kb": 25160000}]
        if "DM_OS_SCHEDULERS" in U:
            return [{"total": 18, "online": 16, "nodes": 1}]
        if "DM_OS_PROCESS_MEMORY" in U:
            return [{"in_use_kb": 24980000, "vas_kb": 137438953472}]
        if "DM_SERVER_SERVICES" in U:
            return [
                {"servicename": "SQL Server (SQL2022)", "startup_type_desc": "Automatic",
                 "status_desc": "Running", "service_account": "NT Service\\MSSQL$SQL2022"},
                {"servicename": "SQL Server Agent (SQL2022)", "startup_type_desc": "Automatic",
                 "status_desc": "Running", "service_account": "NT Service\\SQLAgent$SQL2022"},
                {"servicename": "SQL Server Browser", "startup_type_desc": "Disabled",
                 "status_desc": "Stopped", "service_account": "NT AUTHORITY\\LOCAL SERVICE"},
            ]
        if "DM_OS_MEMORY_CLERKS" in U:
            return [
                {"type": "MEMORYCLERK_SQLBUFFERPOOL", "pages_kb": 22910000000 / 1024},
                {"type": "CACHESTORE_SQLCP", "pages_kb": 2620000},
                {"type": "CACHESTORE_OBJCP", "pages_kb": 640000},
                {"type": "MEMORYCLERK_SQLQUERYEXEC", "pages_kb": 410000},
                {"type": "OBJECTSTORE_LOCK_MANAGER", "pages_kb": 190000},
                {"type": "MEMORYCLERK_SQLCLR", "pages_kb": 60000},
                {"type": "CACHESTORE_PHDR", "pages_kb": 120000},
                {"type": "MEMORYCLERK_SQLGENERAL", "pages_kb": 105000},
            ]
        if "DM_EXEC_CACHED_PLANS" in U:
            return [
                {"objtype": "Adhoc", "cnt": 52300, "size_b": 3900000000},
                {"objtype": "Prepared", "cnt": 8100, "size_b": 610000000},
                {"objtype": "Proc", "cnt": 4200, "size_b": 480000000},
                {"objtype": "CheckClusive", "cnt": 120, "size_b": 9000000},
            ][0:4]
        if "DM_EXEC_QUERY_PLAN" in U:
            return [
                {"plan_xml": _PLAN_XML_CONV, "sql_text": SQL_TEXTS[0]},
                {"plan_xml": _PLAN_XML_SPILL, "sql_text": SQL_TEXTS[1]},
                {"plan_xml": _PLAN_XML_OK, "sql_text": SQL_TEXTS[3]},
            ]
        if "DM_EXEC_QUERY_STATS" in U:
            if "QUERY_HASH" in U:
                return [
                    {"query_hash": bytes.fromhex("0x7a11e2b4c9d0f1a2"[-16:]),
                     "plan_count": 24, "execs": 51000,
                     "sql_text": "SELECT * FROM dbo.Orders WHERE CustomerId = @p0 AND "
                                 "Status IN (@p1, @p2)"},
                    {"query_hash": bytes.fromhex("1f2e3d4c5b6a7988"),
                     "plan_count": 11, "execs": 12000,
                     "sql_text": "SELECT TOP (@n) * FROM dbo.Events WHERE Kind = @k"},
                ]
            order = "worker" if "TOTAL_WORKER_TIME DESC" in U else (
                "reads" if "TOTAL_LOGICAL_READS DESC" in U else "avg")
            base_rows = self._qstats_rows()
            return base_rows if order != "avg" else base_rows[1:5]
        if "DM_DB_MISSING_INDEX_DETAILS" in U:
            dbname = "SalesDB"
            return [
                {"obj": "[SalesDB].[dbo].[Orders]", "equality_columns": "[CustomerId], [Status]",
                 "inequality_columns": None, "included_columns": "[CreatedAt], [Number]",
                 "unique_compiles": 340, "user_seeks": 48211, "user_scans": 0,
                 "avg_user_impact": 91.3, "last_user_seek": _ago(hours=1)},
                {"obj": "[SalesDB].[dbo].[Events]", "equality_columns": "[Kind]",
                 "inequality_columns": "[CreatedAt]", "included_columns": "[Payload]",
                 "unique_compiles": 120, "user_seeks": 19840, "user_scans": 0,
                 "avg_user_impact": 74.8, "last_user_seek": _ago(minutes=30)},
                {"obj": "[SalesDB].[dbo].[Sessions]", "equality_columns": "[UserId]",
                 "inequality_columns": "[ExpiresAt]", "included_columns": None,
                 "unique_compiles": 60, "user_seeks": 9021, "user_scans": 0,
                 "avg_user_impact": 68.1, "last_user_seek": _ago(hours=2), "db": dbname},
            ]
        if "DM_DB_INDEX_PHYSICAL_STATS" in U:
            return [
                {"sch": "dbo", "tbl": "Events", "ix_name": "IX_Events_CreatedAt",
                 "frag": 71.4, "page_count": 128400, "fill_factor": 0, "type_desc": "NONCLUSTERED",
                 "db": "SalesDB"},
                {"sch": "dbo", "tbl": "Orders", "ix_name": "IX_Orders_CustomerId",
                 "frag": 44.9, "page_count": 52100, "fill_factor": 0,
                 "type_desc": "NONCLUSTERED", "db": "SalesDB"},
                {"sch": "dbo", "tbl": "OrderLines", "ix_name": "PK_OrderLines",
                 "frag": 33.2, "page_count": 96200, "fill_factor": 0,
                 "type_desc": "CLUSTERED", "db": "SalesDB"},
                {"sch": "dbo", "tbl": "Sessions", "ix_name": "IX_Sessions_Expires",
                 "frag": 21.8, "page_count": 2200, "fill_factor": 0,
                 "type_desc": "NONCLUSTERED", "db": "SalesDB"},
            ]
        if "DM_DB_STATS_PROPERTIES" in U:
            return [
                {"sch": "dbo", "tbl": "Orders", "stat_name": "_WA_Sys_00000007_14E61C24",
                 "last_updated": _ago(days=23), "rows": 4820000,
                 "rows_sampled": 120000, "modification_counter": 2910000,
                 "no_recompute": 0, "db": "SalesDB"},
                {"sch": "dbo", "tbl": "Events", "stat_name": "IX_Events_CreatedAt",
                 "last_updated": _ago(days=16), "rows": 31100000,
                 "rows_sampled": 770000, "modification_counter": 18400000,
                 "no_recompute": 0, "db": "SalesDB"},
                {"sch": "dbo", "tbl": "Customers", "stat_name": "PK_Customers",
                 "last_updated": _ago(days=2), "rows": 96000, "rows_sampled": 96000,
                 "modification_counter": 900, "no_recompute": 0, "db": "SalesDB"},
            ]
        if "DM_DB_INDEX_USAGE_STATS" in U:
            return [
                {"sch": "dbo", "tbl": "Orders", "ix_name": "IX_Orders_Guid",
                 "type_desc": "NONCLUSTERED", "reads": 0, "updates": 912000,
                 "size_mb": 1024.0, "db": "SalesDB"},
                {"sch": "dbo", "tbl": "Events", "ix_name": "IX_Events_Old",
                 "type_desc": "NONCLUSTERED", "reads": 0, "updates": 141000,
                 "size_mb": 5120.0, "db": "SalesDB"},
                {"sch": "dbo", "tbl": "Sessions", "ix_name": "IX_Sessions_User",
                 "type_desc": "NONCLUSTERED", "reads": 0, "updates": 42000,
                 "size_mb": 640.0, "db": "SalesDB"},
            ]
        if "FROM SYS.DATABASES D" in U and "STATE_DESC = N'ONLINE'" in U:
            return [
                {"name": "master", "state_desc": "ONLINE", "recovery_model_desc": "SIMPLE",
                 "log_reuse_wait_desc": "NOTHING", "compatibility_level": 160,
                 "page_verify_option_desc": "CHECKSUM", "is_read_committed_snapshot_on": 1,
                 "is_auto_shrink_on": 0, "is_auto_close_on": 0, "is_auto_create_stats_on": 1,
                 "is_auto_update_stats_on": 1, "snapshot_isolation_state_desc": "ON",
                 "log_reuse_wait": 0, "is_system": 1},
                {"name": "tempdb", "state_desc": "ONLINE", "recovery_model_desc": "SIMPLE",
                 "log_reuse_wait_desc": "NOTHING", "compatibility_level": 160,
                 "page_verify_option_desc": "CHECKSUM", "is_read_committed_snapshot_on": 0,
                 "is_auto_shrink_on": 0, "is_auto_close_on": 0, "is_auto_create_stats_on": 1,
                 "is_auto_update_stats_on": 1, "snapshot_isolation_state_desc": "ON",
                 "log_reuse_wait": 0, "is_system": 1},
                {"name": "SalesDB", "state_desc": "ONLINE", "recovery_model_desc": "FULL",
                 "log_reuse_wait_desc": "LOG_BACKUP", "compatibility_level": 110,
                 "page_verify_option_desc": "CHECKSUM", "is_read_committed_snapshot_on": 0,
                 "is_auto_shrink_on": 0, "is_auto_close_on": 0, "is_auto_create_stats_on": 1,
                 "is_auto_update_stats_on": 1, "snapshot_isolation_state_desc": "OFF",
                 "log_reuse_wait": 2, "is_system": 0},
                {"name": "ReportDB", "state_desc": "ONLINE", "recovery_model_desc": "FULL",
                 "log_reuse_wait_desc": "LOG_BACKUP", "compatibility_level": 130,
                 "page_verify_option_desc": "TORN_PAGE_DETECTION",
                 "is_read_committed_snapshot_on": 0, "is_auto_shrink_on": 1,
                 "is_auto_close_on": 0, "is_auto_create_stats_on": 1,
                 "is_auto_update_stats_on": 1, "snapshot_isolation_state_desc": "OFF",
                 "log_reuse_wait": 2, "is_system": 0},
                {"name": "WarehouseDB", "state_desc": "ONLINE", "recovery_model_desc": "SIMPLE",
                 "log_reuse_wait_desc": "NOTHING", "compatibility_level": 150,
                 "page_verify_option_desc": "CHECKSUM", "is_read_committed_snapshot_on": 1,
                 "is_auto_shrink_on": 0, "is_auto_close_on": 0, "is_auto_create_stats_on": 1,
                 "is_auto_update_stats_on": 1, "snapshot_isolation_state_desc": "ON",
                 "log_reuse_wait": 0, "is_system": 0},
            ]
        if "FROM SYS.MASTER_FILES" in U and "DM_OS_VOLUME_STATS" not in U:
            return [
                {"db": "master", "logical_name": "master", "type_desc": "ROWS",
                 "size_mb": 6.0, "growth": 10, "is_percent_growth": 1,
                 "physical_name": "C:\\Program Files\\...\\master.mdf"},
                {"db": "SalesDB", "logical_name": "SalesDB_data", "type_desc": "ROWS",
                 "size_mb": 61440.0, "growth": 1024, "is_percent_growth": 0,
                 "physical_name": "E:\\Data\\SalesDB_data.mdf"},
                {"db": "SalesDB", "logical_name": "SalesDB_log", "type_desc": "LOG",
                 "size_mb": 14336.0, "growth": 10, "is_percent_growth": 1,
                 "physical_name": "F:\\Logs\\SalesDB_log.ldf"},
                {"db": "ReportDB", "logical_name": "ReportDB", "type_desc": "ROWS",
                 "size_mb": 30720.0, "growth": 1, "is_percent_growth": 0,
                 "physical_name": "E:\\Data\\ReportDB.mdf"},
                {"db": "ReportDB", "logical_name": "ReportDB_log", "type_desc": "LOG",
                 "size_mb": 8192.0, "growth": 10, "is_percent_growth": 1,
                 "physical_name": "E:\\Data\\ReportDB_log.ldf"},
                {"db": "WarehouseDB", "logical_name": "WH", "type_desc": "ROWS",
                 "size_mb": 102400.0, "growth": 512, "is_percent_growth": 0,
                 "physical_name": "G:\\WH\\WH.mdf"},
                {"db": "tempdb", "logical_name": "tempdev", "type_desc": "ROWS",
                 "size_mb": 8192.0, "growth": 128, "is_percent_growth": 0,
                 "physical_name": "D:\\TempDB\\tempdev.mdf"},
                {"db": "tempdb", "logical_name": "templog", "type_desc": "LOG",
                 "size_mb": 2048.0, "growth": 64, "is_percent_growth": 0,
                 "physical_name": "D:\\TempDB\\templog.ldf"},
            ]
        if "DM_DB_LOG_SPACE_USAGE" in U:
            used = {"SalesDB": 96.4, "ReportDB": 88.1, "WarehouseDB": 22.0}
            name = next((k for k in used if ("[%s]" % k) in s), None)
            return [{"log_mb": 14336.0, "used_pct": used.get(name, 30.0)}]
        if "DM_DB_LOG_INFO" in U:
            name = "SalesDB" if "[SalesDB]" in s else ("ReportDB" if "[ReportDB]" in s else
                                                       ("WarehouseDB" if "[WarehouseDB]" in s else None))
            vlf = {"SalesDB": 1420, "ReportDB": 810, "WarehouseDB": 120}
            return [{"vlf": vlf.get(name, 90)}] if name else []
        if "DM_OS_VOLUME_STATS" in U:
            return [
                {"volume_mount_point": "C:\\", "file_system_type": "NTFS",
                 "total_gb": 119.87, "free_gb": 41.22},
                {"volume_mount_point": "E:\\", "file_system_type": "NTFS",
                 "total_gb": 1023.9, "free_gb": 68.4},
                {"volume_mount_point": "F:\\", "file_system_type": "NTFS",
                 "total_gb": 511.9, "free_gb": 38.7},
                {"volume_mount_point": "G:\\", "file_system_type": "NTFS",
                 "total_gb": 2047.9, "free_gb": 912.0},
                {"volume_mount_point": "D:\\", "file_system_type": "NTFS",
                 "total_gb": 255.9, "free_gb": 133.2},
            ]
        if "TEMPDB.SYS.DATABASE_FILES" in U:
            return [
                {"name": "tempdev", "size_mb": 8192.0,
                 "physical_name": "D:\\TempDB\\tempdev.mdf", "type_desc": "ROWS"},
                {"name": "templog", "size_mb": 2048.0,
                 "physical_name": "D:\\TempDB\\templog.ldf", "type_desc": "LOG"},
            ]
        if "DM_TRAN_ACTIVE_SNAPSHOT_DATABASE_TRANSACTIONS" in U:
            return [{"session_id": 61, "elapsed_time_seconds": 640, "db": "SalesDB"}]
        if "DATABASE_QUERY_STORE_OPTIONS" in U:
            name = ("SalesDB" if "[SalesDB]" in s else
                    ("ReportDB" if "[ReportDB]" in s else
                     ("WarehouseDB" if "[WarehouseDB]" in s else None)))
            if not name:
                return []
            return [{"state": "OFF" if name in ("SalesDB", "ReportDB") else "READ_WRITE"}]
        if "MSDB.DBO.BACKUPSET" in U:
            return [
                {"db": "SalesDB", "recovery_model_desc": "FULL",
                 "last_full": _ago(hours=20), "last_diff": _ago(hours=11),
                 "last_log": _ago(minutes=43)},
                {"db": "ReportDB", "recovery_model_desc": "FULL",
                 "last_full": _ago(days=10, hours=2), "last_diff": None, "last_log": None},
                {"db": "WarehouseDB", "recovery_model_desc": "SIMPLE",
                 "last_full": _ago(hours=30), "last_diff": _ago(hours=26), "last_log": None},
            ]
        if "SYSJOBHISTORY" in U:
            return [
                {"job": "Ночное обслуживание индексов", "step_id": 1, "step_name": "Rebuild",
                 "run_date": int(_now().strftime("%Y%m%d")), "run_time": 21200,
                 "run_status": 0,
                 "message": "Executed as user: sa. ... The job step was retried without "
                            "success. Consequently, the step failed. [SQLSTATE 42000] "
                            "(Error 3147) Backup and file manipulation operations ... "
                            "must be serialized."},
                {"job": "Импорт из 1С", "step_id": 2, "step_name": "Load",
                 "run_date": int(_now().strftime("%Y%m%d")), "run_time": 41500,
                 "run_status": 0,
                 "message": "Executed as user: sa. ... Ошибка: Время ожидания операции "
                            "истекло. [SQLSTATE HY000] (Error -2). Шаг завершился с ошибкой."},
            ]
        if "SYSTEM_HEALTH" in U:
            return [{"td": _DEADLOCK_XML}]
        if "SP_READERRORLOG" in U:
            return [
                {"LogDate": _ago(days=2, hours=5), "ProcessInfo": "spid61",
                 "Text": "SQL Server has encountered 184 occurrence(s) of I/O requests "
                         "taking longer than 15 seconds to complete on file "
                         "[E:\\Data\\ReportDB.mdf] in database [ReportDB] (8)."},
                {"LogDate": _ago(days=1, hours=9), "ProcessInfo": "spid55",
                 "Text": "autogrow of database file took 438 milliseconds. Consider using "
                         "ALTER DATABASE ... FILEGROWTH..." },
                {"LogDate": _ago(hours=12), "ProcessInfo": "spid63",
                 "Text": "autogrow of database file took 1290 milliseconds."},
                {"LogDate": _ago(hours=3), "ProcessInfo": "spid70",
                 "Text": "Error: 17883, Severity: 1, State: 0. The Scheduler 7 appears to "
                         "be hung. SPID 0x1a2b..."},
                {"LogDate": _ago(hours=7), "ProcessInfo": "Backup",
                 "Text": "Database backed up. Database: master..."},
            ]
        if "TRACESTATUS" in U:
            return [{"TraceFlag": 3226, "Status": 1, "Global": 1, "Session": 1},
                    {"TraceFlag": 1118, "Status": 1, "Global": 1, "Session": 1}]
        if "GETDATE() AS OFF" in U or "DATEDIFF(MINUTE" in U:
            return [{"off": 180}]
        if "GETDATE() AS NOW" in U:
            return [{"now": _now()}]
        # живые запросы/блокировщики (deep)
        if "MOST_RECENT_SQL_HANDLE" in U:
            return [{"session_id": 58, "login_name": LOGIN, "host_name": HOST_,
                     "program_name": PROG, "status": "sleeping", "db_name": "SalesDB",
                     "tran_count": 3,
                     "sql_text": "BEGIN TRAN UPDATE TOP (500) o SET o.Processed = 1, "
                                 "o.UpdatedBy = SUSER_SNAME() FROM dbo.Orders o WHERE "
                                 "o.Processed = 0 -- далее курсор и PRINT в цикле",
                     "wait_type": None, "wait_time": None}]
        raise RuntimeError("демо: неизвестный запрос: %s" % s[:120])

    # ------------------------------------------------ вспомогательные
    def _requests(self, h):
        rows = []
        rows.append({
            "session_id": 58, "status": "running", "blocking_session_id": 0,
            "wait_type": None, "wait_time": 0, "wait_resource": None, "cpu_time": 145000,
            "total_elapsed_time": 210000, "reads": 84200, "writes": 3100,
            "logical_reads": 2412000, "command": "UPDATE", "percent_complete": 0,
            "db_name": "SalesDB", "login_name": LOGIN, "host_name": HOST_,
            "program_name": PROG, "session_status": "running",
            "sql_text": SQL_TEXTS[2], "object_name": None})
        if _load(h) > 0.3:
            rows += [
                {"session_id": 61, "status": "suspended", "blocking_session_id": 58,
                 "wait_type": "LCK_M_IX", "wait_time": 92000, "wait_resource": "5:1:92831",
                 "cpu_time": 30, "total_elapsed_time": 95000, "reads": 0, "writes": 0,
                 "logical_reads": 102, "command": "UPDATE", "percent_complete": 0,
                 "db_name": "SalesDB", "login_name": "CORP\\app_user", "host_name": "APP-SRV-02",
                 "program_name": ".Net SqlClient Data Provider", "session_status": "sleeping",
                 "sql_text": SQL_TEXTS[2], "object_name": None},
                {"session_id": 62, "status": "suspended", "blocking_session_id": 58,
                 "wait_type": "LCK_M_S", "wait_time": 71000, "wait_resource": "5:2:118322",
                 "cpu_time": 12, "total_elapsed_time": 73000, "reads": 210, "writes": 0,
                 "logical_reads": 9400, "command": "SELECT", "percent_complete": 0,
                 "db_name": "SalesDB", "login_name": "CORP\\report_user",
                 "host_name": "BI-SRV", "program_name": "Report Builder",
                 "session_status": "sleeping", "sql_text": SQL_TEXTS[0], "object_name": None},
                {"session_id": 77, "status": "running", "blocking_session_id": 0,
                 "wait_type": "PAGEIOLATCH_SH", "wait_time": 340, "wait_resource": "5:3:11945",
                 "cpu_time": 890000, "total_elapsed_time": 940000, "reads": 4100000,
                 "writes": 0, "logical_reads": 98200000, "command": "SELECT",
                 "percent_complete": 0, "db_name": "SalesDB", "login_name": "CORP\\bi",
                 "host_name": "BI-SRV", "program_name": "Power BI",
                 "session_status": "running", "sql_text": SQL_TEXTS[1], "object_name": None},
            ]
        return rows

    def _qstats_rows(self):
        def row(exec_, tot_el, tot_cpu, tot_reads, text, obj=None, db="SalesDB"):
            return {"execution_count": exec_, "total_worker_time": tot_cpu,
                    "total_elapsed_time": tot_el, "total_logical_reads": tot_reads,
                    "total_physical_reads": tot_reads // 20,
                    "total_logical_writes": tot_reads // 400, "total_spills": 1800,
                    "creation_time": _ago(days=10), "last_execution_time": _ago(minutes=3),
                    "db_name": db, "schema_name": "dbo", "object_name": obj,
                    "sql_text": text}
        return [
            row(1840, 412000000, 388000000, 51200000000, SQL_TEXTS[1], "usp_CalcDailyReport"),
            row(91000, 296000000, 281000000, 94000000000, SQL_TEXTS[0]),
            row(220, 180000000, 174000000, 31000000000,
                "SELECT c.Name, COUNT(*) FROM dbo.Orders o JOIN dbo.Customers c ... "
                "GROUP BY c.Name ORDER BY 2 DESC"),
            row(610000, 91000000, 88000000, 14000000000, SQL_TEXTS[3], None, "ReportDB"),
            row(4400, 64000000, 61000000, 8800000000, SQL_TEXTS[4]),
            row(980000, 42000000, 41000000, 2100000000, SQL_TEXTS[5]),
        ]


_PLAN_XML_CONV = """<?xml version="1.0" encoding="utf-16"?>
<ShowPlanXML xmlns="http://schemas.microsoft.com/sqlserver/2004/07/showplan" Version="1.481" Build="16.0.4165.4">
<BatchSequence><Batch><Statements><StmtSimple StatementText="SELECT o.Id, o.Number, c.Name FROM dbo.Orders o JOIN dbo.Customers c ON c.Id = o.CustomerId WHERE o.CreatedAt &gt;= @1 AND o.Status = @2" StatementId="1">
<QueryPlan DegreeOfParallelism="4" CachedPlanSize="128">
<Warnings><PlanAffectingConvert ConvertIssue="Seek Plan" Expression="[dbo].[Orders].[Status] = Convert(nvarchar(10),[@2],0)"/></Warnings>
<RelOp AvgRowSize="45" EstimateRows="1892.4" LogicalOp="Clustered Index Scan" PhysicalOp="Clustered Index Scan">
<IndexScan Ordered="0" ForcedIndex="0" NoExpandHint="0"><Object Database="[SalesDB]" Schema="[dbo]" Table="[Orders]" Index="[PK_Orders]"/></IndexScan>
</RelOp></QueryPlan></StmtSimple></Statements></Batch></BatchSequence></ShowPlanXML>"""

_PLAN_XML_SPILL = """<?xml version="1.0" encoding="utf-16"?>
<ShowPlanXML xmlns="http://schemas.microsoft.com/sqlserver/2004/07/showplan" Version="1.481" Build="16.0.4165.4">
<BatchSequence><Batch><Statements><StmtSimple StatementText="exec dbo.usp_CalcDailyReport @Date = '2026-09-30', @Region = 4" StatementId="1">
<QueryPlan DegreeOfParallelism="8" CachedPlanSize="512">
<Warnings><SpillToTempDb SpillLevel="2" SpilledThreadCount="8" EstRows="2100000"/></Warnings>
<RelOp LogicalOp="Sort" PhysicalOp="Sort"><Object Database="[SalesDB]" Schema="[dbo]" Table="[Events]"/></RelOp>
</QueryPlan></StmtSimple></Statements></Batch></BatchSequence></ShowPlanXML>"""

_PLAN_XML_OK = """<?xml version="1.0" encoding="utf-16"?>
<ShowPlanXML xmlns="http://schemas.microsoft.com/sqlserver/2004/07/showplan" Version="1.481" Build="16.0.4165.4">
<BatchSequence><Batch><Statements><StmtSimple StatementText="INSERT INTO dbo.ReportCache..." StatementId="1">
<QueryPlan DegreeOfParallelism="1" CachedPlanSize="24">
<RelOp LogicalOp="Clustered Index Insert" PhysicalOp="Clustered Index Insert"/>
</QueryPlan></StmtSimple></Statements></Batch></BatchSequence></ShowPlanXML>"""

_DEADLOCK_XML = """<RingBufferTarget truncated="0" processingTime="2459023" totalEventsProcessed="14812">
<Event name="xml_deadlock_report" package="sqlserver" timestamp="2026-10-01T15:03:12.100Z">
<data name="xml_report"><value>
<deadlock><victim-list><victimProcess id="process1850a4c8108"/></victim-list>
<process-list>
<process id="process1850a4c8108" taskpriority="0" logused="0" waitresource="KEY: 5:72057594047512576 (8194443284a0)" waittime="7335" ownerId="9182736" transactionname="SELECT" lasttranstarted="2026-10-01T15:03:04.770" XDES="0x1850b130848" lockMode="S" schedulerid="9" kpid="0" status="suspended" spid="62" sbid="0" ecid="0" priority="0" trancount="0" lastbatchstarted="2026-10-01T15:03:04.770" lastbatchcompleted="2026-10-01T15:03:04.750" clientapp="Report Builder" hostname="BI-SRV" hostpid="4212" loginname="CORP\\report_user" isolationlevel="read committed (2)" currentdatabase="SalesDB" currentoptimizer="2">
<inputbuf>SELECT o.Id, o.Number, c.Name FROM dbo.Orders o JOIN dbo.Customers c ON c.Id = o.CustomerId WHERE o.CreatedAt &gt;= '2026-09-30' AND o.Status = 'NEW'</inputbuf></process>
<process id="process1850a4c9230" taskpriority="0" logused="20480" waitresource="KEY: 5:72057594046857216 (61a06abd401c)" waittime="7340" ownerId="9182735" transactionname="user_transaction" lasttranstarted="2026-10-01T15:02:57.590" XDES="0x1850b0a9088" lockMode="X" schedulerid="11" kpid="0" status="suspended" spid="58" sbid="0" ecid="0" priority="0" trancount="2" lastbatchstarted="2026-10-01T15:03:04.750" lastbatchcompleted="2026-10-01T15:03:04.700" clientapp=".Net SqlClient Data Provider" hostname="APP-SRV-07" loginname="CORP\\srv-report" isolationlevel="read committed (2)" currentdatabase="SalesDB">
<inputbuf>UPDATE TOP (500) o SET o.Processed = 1, o.UpdatedBy = SUSER_SNAME() FROM dbo.Orders o WHERE o.Processed = 0</inputbuf></process>
</process-list>
<resource-list>
<keylock hobtid="72057594047512576" dbid="5" objectname="SalesDB.dbo.Orders" indexname="PK_Orders" id="lock18509c2b300" mode="X" associatedObjectId="72057594047512576"/>
<keylock hobtid="72057594046857216" dbid="5" objectname="SalesDB.dbo.Orders" indexname="IX_Orders_CustomerId" id="lock18509c2b580" mode="S" associatedObjectId="72057594046857216"/>
</resource-list></deadlock>
</value></data></Event>
<Event name="xml_deadlock_report" package="sqlserver" timestamp="2026-09-30T11:22:44.310Z">
<data name="xml_report"><value>
<deadlock><victim-list><victimProcess id="processaaa"/></victim-list>
<process-list>
<process id="processaaa" spid="71" waitresource="KEY: 7:72057594051555328 (8fa35)" waittime="4122" transactionname="SELECT" lastbatchstarted="2026-09-30T11:22:40.190" clientapp="Power BI" hostname="BI-SRV" loginname="CORP\\bi" isolationlevel="read committed (2)" currentdatabase="ReportDB">
<inputbuf>SELECT * FROM dbo.FactSales WHERE DateKey = 20260929 AND RegionKey = 4</inputbuf></process>
<process id="processbbb" spid="69" waitresource="KEY: 7:72057594051555329 (8fa99)" waittime="4125" transactionname="user_transaction" lastbatchstarted="2026-09-30T11:22:39.990" clientapp="SQLAgent - Ticker" hostname="DEMO-SQL" loginname="sa" isolationlevel="read committed (2)" currentdatabase="ReportDB">
<inputbuf>UPDATE dbo.FactSales SET Amount = Amount * 1.0 WHERE DateKey = 20260929</inputbuf></process>
</process-list>
<resource-list><keylock hobtid="72057594051555328" dbid="7" objectname="ReportDB.dbo.FactSales" indexname="PK_FactSales" id="lock1" mode="X" associatedObjectId="72057594051555328"/></resource-list></deadlock>
</value></data></Event>
</RingBufferTarget>"""


# =====================================================================
# ДЕМО-ТАЙМЛАЙН СУТОЧНОГО МОНИТОРИНГА (24 ч, 1 минута)
# =====================================================================
def generate_demo_samples(interval=60, hours=24):
    rnd = random.Random(42)
    now = int(time.time())
    n = int(hours * 3600 / interval)
    samples = []
    for i in range(n):
        ts = now - (n - 1 - i) * interval
        lt = datetime.datetime.fromtimestamp(ts)
        h = lt.hour + lt.minute / 60.0
        ld = _load(h)
        peak = _in(h, 9.6, 10.9)
        block = _in(h, 15.0, 15.6)
        cpu = _cpu(h) + rnd.uniform(-3, 3)
        batch = _batch(h) * rnd.uniform(0.85, 1.15)
        if block:
            batch *= 0.8
        ple = int((265 + rnd.randint(-25, 25)) if peak else
                  (400 + rnd.randint(-60, 60)) if ld > 0.35 else
                  (1500 + rnd.randint(-250, 250)))
        bchr = 94.3 + rnd.uniform(-0.6, 0.6) if peak else 97.5 + rnd.uniform(-0.5, 0.5)
        lazy = 35 + rnd.uniform(-8, 8) if peak else (5 if ld > 0.35 else 1.0)
        io_rd = (38 + rnd.uniform(-6, 6)) if peak else (9 + 4 * ld + rnd.uniform(-1, 2))
        io_wr = (6 + 3 * ld + rnd.uniform(-1, 1))
        waits = []
        if peak:
            waits = [("PAGEIOLATCH_SH", rnd.uniform(28, 36)),
                     ("CXPACKET", rnd.uniform(12, 18)),
                     ("CXCONSUMER", rnd.uniform(5, 9)),
                     ("SOS_SCHEDULER_YIELD", rnd.uniform(6, 10)),
                     ("RESOURCE_SEMAPHORE", rnd.uniform(2, 4))]
        elif block:
            waits = [("LCK_M_IX", rnd.uniform(30, 42)),
                     ("LCK_M_S", rnd.uniform(15, 24)),
                     ("PAGEIOLATCH_SH", rnd.uniform(4, 8)),
                     ("WRITELOG", rnd.uniform(3, 6))]
        elif ld > 0.35:
            waits = [("PAGEIOLATCH_SH", rnd.uniform(8, 14)),
                     ("CXPACKET", rnd.uniform(4, 8)),
                     ("ASYNC_NETWORK_IO", rnd.uniform(3, 7)),
                     ("WRITELOG", rnd.uniform(3, 6)),
                     ("SOS_SCHEDULER_YIELD", rnd.uniform(2, 5))]
        else:
            waits = [("WRITELOG", rnd.uniform(1, 3)),
                     ("ASYNC_NETWORK_IO", rnd.uniform(1, 2)),
                     ("BACKUPIO", rnd.uniform(0, 2))]
        samples.append({
            "ts": ts, "dt": interval,
            "sql_cpu": round(max(0, cpu), 1),
            "sys_cpu": round(max(0, cpu * 1.12 + 4), 1),
            "batch_req": round(batch, 1),
            "trans_sec": round(batch * 3.1, 1),
            "compiles_sec": round(batch * (0.2 if peak else 0.08), 1),
            "recompiles_sec": round(batch * 0.01, 2),
            "lazy_writes": round(lazy, 1),
            "ple": ple, "bchr": round(bchr, 2),
            "full_scans": round(20 + 260 * ld, 1),
            "index_searches": round(2800 + 20000 * ld, 1),
            "mem_target_mb": 24576.0,
            "mem_total_mb": round(23900 + 300 * ld, 1),
            "io_rd_lat": round(io_rd, 2), "io_wr_lat": round(io_wr, 2),
            "io_rd_mbps": round(40 + 240 * ld, 1), "io_wr_mbps": round(12 + 80 * ld, 1),
            "tempdb_used_mb": round(2600 + 1800 * ld + (4200 if block else 0), 1),
            "tempdb_version_mb": round(2400 if block else (300 * ld), 1),
            "sessions": int(36 + 40 * ld),
            "blocked": 6 if block else 0,
            "blocked_max_wait": 138.0 if block else 0.0,
            "waits": [(w, round(ms * interval / 60.0, 3), round(ms / 8.0, 1),
                       round(ms * 0.2, 3)) for w, ms in waits],
        })
    return samples


def generate_demo_events(samples):
    blocking, longq = [], []
    for s in samples:
        lt = datetime.datetime.fromtimestamp(s["ts"])
        h = lt.hour + lt.minute / 60.0
        if _in(h, 15.0, 15.55) and (s["ts"] % 180) < 60:
            for sid, bsid, wt, wres, cmd, text in (
                    (61, 58, "LCK_M_IX", "5:1:92831", "UPDATE", SQL_TEXTS[2]),
                    (62, 58, "LCK_M_S", "5:2:118322", "SELECT", SQL_TEXTS[0]),
                    (63, 61, "LCK_M_IX", "5:1:92834", "UPDATE", SQL_TEXTS[2])):
                blocking.append({
                    "ts": s["ts"], "session_id": sid, "blocking_session_id": bsid,
                    "wait_type": wt, "wait_time_s": 90 + (s["ts"] % 60),
                    "wait_resource": wres, "command": cmd, "login": LOGIN,
                    "host": HOST_, "program": PROG, "db": "SalesDB",
                    "elapsed_s": 120 + (s["ts"] % 90), "text_head": text[:400]})
        if _in(h, 9.55, 10.95) and lt.minute % 5 == 0:
            longq.append({"ts": s["ts"], "session_id": 77, "login": "CORP\\bi",
                          "program": "Power BI", "db": "SalesDB",
                          "elapsed_s": 620 + lt.minute * 2, "cpu_s": 590,
                          "logical_reads": 98200000, "writes": 0,
                          "command": "SELECT", "text_head": SQL_TEXTS[1],
                          "wait_type": "PAGEIOLATCH_SH"})
        if _in(h, 15.0, 15.55) and lt.minute % 4 == 0:
            longq.append({"ts": s["ts"], "session_id": 62, "login": "CORP\\report_user",
                          "program": "Report Builder", "db": "SalesDB",
                          "elapsed_s": 140 + lt.minute, "cpu_s": 1,
                          "logical_reads": 9400, "writes": 0,
                          "command": "SELECT", "text_head": SQL_TEXTS[0],
                          "wait_type": "LCK_M_S"})
    return blocking, longq
