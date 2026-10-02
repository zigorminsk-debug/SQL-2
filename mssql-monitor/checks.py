# -*- coding: utf-8 -*-
"""
checks.py — сбор данных с SQL Server (только чтение, только системные DMV).

collect_light_sample() — лёгкий опрос (каждый интервал мониторинга):
  CPU, память, счётчики производительности, ожидания (дельта), блокировки,
  долгие запросы, ввод-вывод, tempdb.

collect_deep() — глубокая диагностика для отчёта: конфигурация, ожидания,
  запросы, планы, индексы, статистика, базы данных, бэкапы, джобы SQL Agent,
  взаимоблокировки, журнал ошибок и т.д.
"""
import re
import time
import datetime
from xml.etree import ElementTree as ET

from db import qdb, sql_str

# ---------------- ожидания: «безвредные» типы ----------------
BENIGN_WAITS = {
    "WAITFOR", "SLEEP_TASK", "SLEEP_BPOOL_FLUSH", "SLEEP_DBSTARTUP",
    "SLEEP_DCOMSTARTUP", "SLEEP_MASTERDBREADY", "SLEEP_MASTERMDREADY",
    "SLEEP_MASTERUPGRADED", "SLEEP_MSDBSTARTUP", "SLEEP_SYSTEMTASK",
    "SLEEP_TEMPDBSTARTUP", "BROKER_TASK_STOP", "BROKER_TO_FLUSH",
    "BROKER_EVENTHANDLER", "BROKER_RECEIVE_WAITFOR", "BROKER_TRANSMITTER",
    "BROKER_SHUTDOWN", "CHECKPOINT_QUEUE", "CHKPT", "DIRTY_PAGE_POLL",
    "DISPATCHER_QUEUE_SEMAPHORE", "EXECSYNC", "FT_IFTSHC_MUTEX",
    "HTTP_ENDPOINT_COLLCREATE", "HTTP_ENUMERATION", "HTTP_START",
    "LAZYWRITER_SLEEP", "LOGMGR_QUEUE", "MIRROR_SEND", "MISCELLANEOUS",
    "ONDEMAND_TASK_QUEUE", "PARALLEL_REDO_DRAIN_TRANS",
    "PARALLEL_REDO_LOG_BUFFER", "PARALLEL_REDO_WORKER_WAIT",
    "PREEMPTIVE_OS_GETDISKFREESPACE", "PREEMPTIVE_OS_LIBRARYOPS",
    "PREEMPTIVE_OS_QUERYREGISTRY", "PREEMPTIVE_OS_WRITEFILEGATHER",
    "QDS_ASYNC_QUEUE", "QDS_CLEANUP_STALE_QUERIES_TASK_MAIN_LOOP_SLEEP",
    "QDS_PERSIST_TASK_MAIN_LOOP_SLEEP", "QDS_SHUTDOWN_QUEUE",
    "REDO_THREAD_PENDING_WORK", "RESOURCE_QUEUE", "SERVER_IDLE_CHECK",
    "SOS_WORK_DISPATCHER", "SP_SERVER_DIAGNOSTICS_SLEEP",
    "SQLTRACE_BUFFER_FLUSH", "SQLTRACE_INCREMENTAL_FLUSH_SLEEP",
    "SQLTRACE_WAIT_ENTRIES", "TRACEWRITE", "WAIT_FOR_RESULTS",
    "WAIT_XTP_OFFLINE_CKPT_NEW_LOG", "WAIT_XTP_RECOVERY",
    "VDI_CLIENT_OTHER", "XE_DISPATCHER_WAIT", "XE_LIVE_TARGET_TVF",
    "XE_TIMER_EVENT", "DIRTY_PAGE_TABLE_SYNC", "HADR_FILESTREAM_IOMGR_IOCOMPLETION",
    "DEADLOCK_ENUM_MUTEX", "SOSHOST_PUBSUB", "HADR_DB_SEEDING",
}

SQL_REQUESTS = """
SELECT r.session_id, r.status, r.blocking_session_id, r.wait_type, r.wait_time,
       r.wait_resource, r.cpu_time, r.total_elapsed_time, r.reads, r.writes,
       r.logical_reads, r.command, r.percent_complete,
       DB_NAME(r.database_id) AS db_name,
       s.login_name, s.host_name, s.program_name, s.status AS session_status,
       t.text AS sql_text,
       CASE WHEN t.objectid IS NOT NULL
            THEN OBJECT_SCHEMA_NAME(t.objectid, t.dbid) + N'.' + OBJECT_NAME(t.objectid, t.dbid)
            ELSE NULL END AS object_name
FROM sys.dm_exec_requests r WITH (NOLOCK)
LEFT JOIN sys.dm_exec_sessions s WITH (NOLOCK) ON s.session_id = r.session_id
OUTER APPLY sys.dm_exec_sql_text(r.sql_handle) t
WHERE r.session_id >= 50 OR r.blocking_session_id <> 0
"""

SQL_PERF = """
SELECT RTRIM(LTRIM(object_name)) AS object_name,
       RTRIM(LTRIM(counter_name)) AS counter_name,
       RTRIM(LTRIM(instance_name)) AS instance_name,
       cntr_value, cntr_type
FROM sys.dm_os_performance_counters
WHERE counter_name IN (
  N'Batch Requests/sec', N'Transactions/sec', N'SQL Compilations/sec',
  N'SQL Re-Compilations/sec', N'Page life expectancy',
  N'Buffer cache hit ratio', N'Buffer cache hit ratio base',
  N'Lazy Writes/sec', N'Full Scans/sec', N'Index Searches/sec',
  N'Target Server Memory (KB)', N'Total Server Memory (KB)',
  N'Lock Wait Time (ms)')
"""

SQL_WAITS = """
SELECT wait_type, waiting_tasks_count, wait_time_ms, signal_wait_time_ms
FROM sys.dm_os_wait_stats
WHERE wait_time_ms > 0
"""

SQL_IO = """
SELECT database_id, SUM(CAST(num_of_reads AS BIGINT)) AS r,
       SUM(CAST(io_stall_read_ms AS BIGINT)) AS r_ms,
       SUM(CAST(num_of_writes AS BIGINT)) AS w,
       SUM(CAST(io_stall_write_ms AS BIGINT)) AS w_ms,
       SUM(CAST(num_of_bytes_read AS BIGINT)) AS rb,
       SUM(CAST(num_of_bytes_written AS BIGINT)) AS wb
FROM sys.dm_io_virtual_file_stats(NULL, NULL)
GROUP BY database_id
"""

SQL_TEMPDB = """
SELECT SUM(CAST(user_object_reserved_page_count AS BIGINT))*8/1024.0 AS user_mb,
       SUM(CAST(internal_object_reserved_page_count AS BIGINT))*8/1024.0 AS internal_mb,
       SUM(CAST(version_store_reserved_page_count AS BIGINT))*8/1024.0 AS version_mb,
       SUM(CAST(unallocated_extent_page_count AS BIGINT))*8/1024.0 AS free_mb
FROM tempdb.sys.dm_db_file_space_usage
"""

SQL_SESSIONS = """
SELECT COUNT(*) AS cnt FROM sys.dm_exec_sessions WITH (NOLOCK) WHERE is_user_process = 1
"""

SQL_CPU_RB = """
SELECT TOP 1 CAST(record AS NVARCHAR(2048)) AS rec
FROM sys.dm_os_ring_buffers WITH (NOLOCK)
WHERE ring_buffer_type = N'RING_BUFFER_SCHEDULER_MONITOR'
ORDER BY timestamp DESC
"""


def _f(v):
    try:
        if v is None:
            return None
        return float(v)
    except (TypeError, ValueError):
        return None


def _i(v):
    try:
        if v is None:
            return None
        return int(v)
    except (TypeError, ValueError):
        return None


def _ts_now():
    return int(time.time())


def _head(text, n=400):
    if not text:
        return ""
    t = re.sub(r"\s+", " ", str(text)).strip()
    return t[:n]


# ================================================================
# ЛЁГКИЙ ОПРОС (мониторинг)
# ================================================================
def collect_light_sample(db, prev):
    """Собирает один «тик» мониторинга. prev — состояние прошлого тика."""
    s = {"ts": _ts_now(), "waits": []}
    now = s["ts"]
    dt = None
    if prev:
        dt = float(now - prev["ts"]) or None

    # --- CPU (кольцевой буфер, мгновенное значение) ---
    try:
        row = db.query_one(SQL_CPU_RB)
        if row and row.get("rec"):
            m = re.search(r"<ProcessUtilization>(-?\d+)</ProcessUtilization>", row["rec"])
            m2 = re.search(r"<SystemIdle>(-?\d+)</SystemIdle>", row["rec"])
            if m:
                s["sql_cpu"] = float(m.group(1))
            if m2:
                s["sys_cpu"] = float(100 - int(m2.group(1)))
    except Exception:
        pass

    # --- счётчики производительности ---
    perf = {}
    try:
        for r in db.query(SQL_PERF):
            key = (r.get("object_name") or "", r.get("counter_name") or "",
                   r.get("instance_name") or "")
            perf.setdefault(key, r.get("cntr_value"))
    except Exception:
        perf = {}

    def val(like, counter, inst=None):
        for (obj, cnt, i), v in perf.items():
            if cnt == counter and like in obj and (inst is None or i == inst):
                return _f(v)
        return None

    def delta(cur, old):
        if cur is None or old is None or not dt or dt <= 0:
            return None
        d = cur - old
        return d / dt if d >= 0 else None

    s["ple"] = _i(val("Buffer Manager", "Page life expectancy"))
    mem_t = val("Memory Manager", "Target Server Memory (KB)")
    mem_c = val("Memory Manager", "Total Server Memory (KB)")
    if mem_t is not None:
        s["mem_target_mb"] = round(mem_t / 1024.0, 1)
    if mem_c is not None:
        s["mem_total_mb"] = round(mem_c / 1024.0, 1)
    if prev and dt:
        s["batch_req"] = delta(val("SQL Statistics", "Batch Requests/sec"),
                               prev.get("_c_batch"))
        s["trans_sec"] = delta(val("Databases", "Transactions/sec", "_Total"),
                               prev.get("_c_trans"))
        s["compiles_sec"] = delta(val("SQL Statistics", "SQL Compilations/sec"),
                                  prev.get("_c_comp"))
        s["recompiles_sec"] = delta(val("SQL Statistics", "SQL Re-Compilations/sec"),
                                    prev.get("_c_recomp"))
        s["lazy_writes"] = delta(val("Buffer Manager", "Lazy Writes/sec"),
                                 prev.get("_c_lazy"))
        s["full_scans"] = delta(val("Access Methods", "Full Scans/sec"),
                                prev.get("_c_scan"))
        s["index_searches"] = delta(val("Access Methods", "Index Searches/sec"),
                                    prev.get("_c_isearch"))
        bc = val("Buffer Manager", "Buffer cache hit ratio")
        bcb = val("Buffer Manager", "Buffer cache hit ratio base")
        if bc is not None and bcb is not None and prev.get("_c_bc") is not None:
            dv, db_ = bc - prev["_c_bc"], bcb - prev["_c_bcb"]
            if dv > 0 and db_ > 0:
                s["bchr"] = round(100.0 * dv / db_, 2)
        lw = val("Locks", "Lock Wait Time (ms)", "_Total")
        if lw is not None and prev.get("_c_lockwait") is not None:
            s["lock_wait_ms"] = round(max(0.0, lw - prev["_c_lockwait"]), 1)

    state = {
        "_c_batch": val("SQL Statistics", "Batch Requests/sec"),
        "_c_trans": val("Databases", "Transactions/sec", "_Total"),
        "_c_comp": val("SQL Statistics", "SQL Compilations/sec"),
        "_c_recomp": val("SQL Statistics", "SQL Re-Compilations/sec"),
        "_c_lazy": val("Buffer Manager", "Lazy Writes/sec"),
        "_c_scan": val("Access Methods", "Full Scans/sec"),
        "_c_isearch": val("Access Methods", "Index Searches/sec"),
        "_c_bc": val("Buffer Manager", "Buffer cache hit ratio"),
        "_c_bcb": val("Buffer Manager", "Buffer cache hit ratio base"),
        "_c_lockwait": val("Locks", "Lock Wait Time (ms)", "_Total"),
        "_waits": None, "_io": None, "ts": s["ts"],
    }

    # --- ожидания (дельта с прошлого тика) ---
    try:
        cur_w = {r["wait_type"]: (_f(r["wait_time_ms"]) or 0.0,
                                  _f(r["waiting_tasks_count"]) or 0.0,
                                  _f(r["signal_wait_time_ms"]) or 0.0)
                 for r in db.query(SQL_WAITS)}
        state["_waits"] = cur_w
        if prev and prev.get("_waits") and dt:
            deltas = []
            for wt, (wms, tasks, sig) in cur_w.items():
                if wt in BENIGN_WAITS:
                    continue
                p = prev["_waits"].get(wt)
                if not p:
                    continue
                dw = wms - p[0]
                if dw <= 0:
                    continue
                deltas.append((wt, round(dw / 1000.0, 3), max(0.0, tasks - p[1]),
                               round(max(0.0, sig - p[2]) / 1000.0, 3)))
            deltas.sort(key=lambda x: -x[1])
            s["waits"] = deltas[:15]
    except Exception:
        pass

    # --- ввод-вывод (дельта) ---
    try:
        rows = db.query(SQL_IO)
        if prev and prev.get("_io") and dt:
            pio = prev["_io"]
            r = sum(x["r"] or 0 for x in rows) - sum(x["r"] or 0 for x in pio)
            r_ms = sum(x["r_ms"] or 0 for x in rows) - sum(x["r_ms"] or 0 for x in pio)
            w = sum(x["w"] or 0 for x in rows) - sum(x["w"] or 0 for x in pio)
            w_ms = sum(x["w_ms"] or 0 for x in rows) - sum(x["w_ms"] or 0 for x in pio)
            rb = sum(x["rb"] or 0 for x in rows) - sum(x["rb"] or 0 for x in pio)
            wb = sum(x["wb"] or 0 for x in rows) - sum(x["wb"] or 0 for x in pio)
            if r > 0:
                s["io_rd_lat"] = round(r_ms / r, 2)
            if w > 0:
                s["io_wr_lat"] = round(w_ms / w, 2)
            if dt > 0:
                s["io_rd_mbps"] = round(rb / dt / 1048576.0, 2)
                s["io_wr_mbps"] = round(wb / dt / 1048576.0, 2)
        state["_io"] = rows
    except Exception:
        pass

    # --- tempdb ---
    try:
        t = db.query_one(SQL_TEMPDB)
        if t:
            s["tempdb_used_mb"] = round((t.get("user_mb") or 0) +
                                        (t.get("internal_mb") or 0), 1)
            s["tempdb_version_mb"] = round(t.get("version_mb") or 0, 1)
    except Exception:
        pass

    # --- сессии ---
    try:
        r = db.query_one(SQL_SESSIONS)
        s["sessions"] = _i(r.get("cnt")) if r else None
    except Exception:
        pass

    # --- активные запросы, блокировки, долгие запросы ---
    blocked_rows = []
    try:
        rows = db.query(SQL_REQUESTS)
        bl = 0
        max_wait = 0.0
        for r in rows:
            b = _i(r.get("blocking_session_id")) or 0
            elapsed = (_f(r.get("total_elapsed_time")) or 0) / 1000.0
            if b not in (0, -93, -94, -95, -96):
                bl += 1
                max_wait = max(max_wait, (_f(r.get("wait_time")) or 0) / 1000.0)
                blocked_rows.append({
                    "ts": now, "session_id": r.get("session_id"),
                    "blocking_session_id": b,
                    "wait_type": r.get("wait_type"),
                    "wait_time_s": round((_f(r.get("wait_time")) or 0) / 1000.0, 1),
                    "wait_resource": r.get("wait_resource"),
                    "command": r.get("command"),
                    "login": r.get("login_name"), "host": r.get("host_name"),
                    "program": r.get("program_name"), "db": r.get("db_name"),
                    "elapsed_s": round(elapsed, 1),
                    "text_head": _head(r.get("sql_text"), 500)})
        s["blocked"] = bl
        s["blocked_max_wait"] = round(max_wait, 1) if bl else 0.0
    except Exception:
        pass

    s["_blocked_rows"] = blocked_rows
    return s, state


def long_query_events(db, threshold_s=30.0):
    """Текущие запросы длительнее порога (для журнала долгих запросов)."""
    out = []
    try:
        for r in db.query(SQL_REQUESTS):
            sid = _i(r.get("session_id")) or 0
            if sid < 50:
                continue
            elapsed = (_f(r.get("total_elapsed_time")) or 0) / 1000.0
            if elapsed >= threshold_s:
                out.append({
                    "ts": _ts_now(), "session_id": sid,
                    "login": r.get("login_name"), "program": r.get("program_name"),
                    "db": r.get("db_name"), "elapsed_s": round(elapsed, 1),
                    "cpu_s": round((_f(r.get("cpu_time")) or 0) / 1000.0, 1),
                    "logical_reads": _i(r.get("logical_reads")),
                    "writes": _i(r.get("writes")),
                    "command": r.get("command"),
                    "text_head": _head(r.get("sql_text"), 500),
                    "wait_type": r.get("wait_type")})
    except Exception:
        pass
    return out


# ================================================================
# ГЛУБОКАЯ ДИАГНОСТИКА (для отчёта)
# ================================================================
def collect_deep(db, options=None, progress=None, blocking_events=None,
                 long_queries=None):
    """
    Собирает всю информацию для отчёта.
    options: {'fragmentation':bool,'errorlog':bool,'plan_xml':bool}
    Возвращает словарь data с ключами по проверкам; ошибки — в data['errors'].
    """
    options = options or {}
    data = {"errors": {}}
    data["_server_time"] = None
    try:
        data["_server_time"] = db.server_now()
        data["_server_off"] = db.server_utc_offset_minutes()
    except Exception:
        data["_server_off"] = 0

    def safe(name, fn):
        if progress:
            progress(name)
        try:
            data[name] = fn()
        except Exception as e:
            data["errors"][name] = str(e)[:300]

    safe("server", lambda: _server_info(db))
    safe("configs", lambda: db.query(
        "SELECT name, value_in_use, value, minimum, maximum, is_dynamic, is_advanced "
        "FROM sys.configurations WHERE name IN ("
        "N'max server memory (MB)', N'min server memory (MB)', "
        "N'max degree of parallelism', N'cost threshold for parallelism', "
        "N'optimize for ad hoc workloads', N'max worker threads', "
        "N'clr enabled', N'xp_cmdshell', N'priority boost', N'lightweight pooling', "
        "N'remote query timeout (s)', N'blocked process threshold (s)')"))
    safe("sys_info", lambda: _sys_info(db))
    safe("clerks", lambda: db.query_any([
        "SELECT TOP 15 type, SUM(CAST(pages_kb AS BIGINT)) AS pages_kb "
        "FROM sys.dm_os_memory_clerks GROUP BY type ORDER BY 2 DESC",
        "SELECT TOP 15 type, SUM(CAST(single_pages_kb AS BIGINT)+"
        "CAST(multi_page_kb AS BIGINT)) AS pages_kb "
        "FROM sys.dm_os_memory_clerks GROUP BY type ORDER BY 2 DESC"]))
    safe("plan_cache", lambda: db.query(
        "SELECT objtype, COUNT(*) AS cnt, SUM(CAST(size_in_bytes AS BIGINT)) AS size_b "
        "FROM sys.dm_exec_cached_plans GROUP BY objtype ORDER BY 3 DESC"))
    safe("wait_stats", lambda: _wait_stats(db))
    safe("requests_live", lambda: _requests_live(db))
    safe("qstats", lambda: _qstats(db))
    if options.get("plan_xml", True):
        safe("plan_issues", lambda: _plan_issues(db))
    if options.get("fragmentation", True):
        safe("fragmentation", lambda: _fragmentation(db))
    safe("missing_indexes", lambda: _missing_indexes(db))
    safe("stats", lambda: _stats(db))
    safe("unused_indexes", lambda: _unused_indexes(db))
    safe("databases", lambda: _databases(db))
    safe("io_files", lambda: _io_files(db))
    safe("volumes", lambda: db.query(
        "SELECT DISTINCT vs.volume_mount_point, vs.file_system_type, "
        "CAST(vs.total_bytes/1073741824.0 AS DECIMAL(18,2)) AS total_gb, "
        "CAST(vs.available_bytes/1073741824.0 AS DECIMAL(18,2)) AS free_gb "
        "FROM sys.master_files mf "
        "CROSS APPLY sys.dm_os_volume_stats(mf.database_id, mf.file_id) vs"))
    safe("tempdb", lambda: _tempdb(db))
    safe("trace_flags", lambda: db.query("DBCC TRACESTATUS(-1)"))
    safe("query_store", lambda: _query_store(db))
    safe("backups", lambda: _backups(db))
    safe("jobs_failed", lambda: _jobs_failed(db))
    safe("deadlocks", lambda: _deadlocks(db))
    if options.get("errorlog", True):
        safe("errorlog", lambda: _errorlog(db))
    data["blocking_events"] = blocking_events or []
    data["long_queries"] = long_queries or []
    return data


# ---------------- отдельные проверки ----------------
def _server_info(db):
    out = {}
    try:
        row = db.query_one("SELECT @@SERVERNAME AS srv, @@VERSION AS v, "
                           "SERVERPROPERTY(N'InstanceName') AS inst, "
                           "SERVERPROPERTY(N'Edition') AS edition, "
                           "SERVERPROPERTY(N'ProductVersion') AS ver, "
                           "SERVERPROPERTY(N'ProductLevel') AS level, "
                           "SERVERPROPERTY(N'ProductMajorVersion') AS major, "
                           "SERVERPROPERTY(N'ProductUpdateLevel') AS cu, "
                           "SERVERPROPERTY(N'MachineName') AS machine, "
                           "SERVERPROPERTY(N'IsClustered') AS clustered, "
                           "SERVERPROPERTY(N'Collation') AS collation")
        out.update(row or {})
        v = out.get("v") or ""
        m = re.search(r"on (Windows NT [\d.]+|Linux)[^<]*", str(v))
        out["os_line"] = m.group(1) if m else ""
        out["product_line"] = re.sub(r"\s*\(Build[^)]*\)\s*", " ",
                                     str(v).split("\n")[0]).strip()
    except Exception as e:
        out["error"] = str(e)
    try:
        row = db.query_one("SELECT COUNT(*) AS n FROM sys.dm_hadr_instance_replica_states")
        out["ag_count"] = _i(row.get("n")) if row else 0
    except Exception:
        out["ag_count"] = 0
    return out


def _sys_info(db):
    out = {}
    try:
        row = db.query_any([
            "SELECT cpu_count, hyperthread_ratio, sqlserver_start_time, "
            "CAST(physical_memory_kb AS BIGINT) AS phys_kb, "
            "CAST(committed_memory_kb AS BIGINT) AS committed_kb, "
            "CAST(committed_target_memory_kb AS BIGINT) AS committed_target_kb "
            "FROM sys.dm_os_sys_info",
            "SELECT cpu_count, hyperthread_ratio, sqlserver_start_time, "
            "CAST(physical_memory_in_bytes AS BIGINT)/1024 AS phys_kb, "
            "CAST(committed_memory_kb AS BIGINT) AS committed_kb, "
            "CAST(committed_target_memory_kb AS BIGINT) AS committed_target_kb "
            "FROM sys.dm_os_sys_info"])
        out.update((row or [{}])[0])
    except Exception as e:
        out["error"] = str(e)
    try:
        row = db.query_one("SELECT COUNT(*) AS total, "
                           "SUM(CASE WHEN status = N'VISIBLE ONLINE' THEN 1 ELSE 0 END) AS online, "
                           "COUNT(DISTINCT parent_node_id) AS nodes "
                           "FROM sys.dm_os_schedulers")
        out.update(row or {})
    except Exception:
        pass
    try:
        row = db.query_one("SELECT CAST(physical_memory_in_use_kb AS BIGINT) AS in_use_kb, "
                           "CAST(total_virtual_address_space_kb AS BIGINT) AS vas_kb "
                           "FROM sys.dm_os_process_memory")
        out.update(row or {})
    except Exception:
        pass
    try:
        rows = db.query("SELECT servicename, startup_type_desc, status_desc, service_account "
                        "FROM sys.dm_server_services")
        out["services"] = rows
    except Exception:
        out["services"] = []
    return out


def _wait_stats(db):
    rows = db.query(SQL_WAITS)
    real = []
    total_ms = signal_ms = tasks = 0.0
    for r in rows:
        wt = r.get("wait_type")
        if wt in BENIGN_WAITS:
            continue
        wms = _f(r.get("wait_time_ms")) or 0.0
        real.append({"wait_type": wt, "wait_ms": wms,
                     "tasks": _f(r.get("waiting_tasks_count")) or 0.0,
                     "signal_ms": _f(r.get("signal_wait_time_ms")) or 0.0,
                     "max_ms": _f(r.get("max_wait_time_ms")) or 0.0})
        total_ms += wms
        signal_ms += _f(r.get("signal_wait_time_ms")) or 0.0
        tasks += _f(r.get("waiting_tasks_count")) or 0.0
    real.sort(key=lambda x: -x["wait_ms"])
    return {"rows": real[:40], "total_ms": total_ms,
            "signal_ms": signal_ms, "tasks": tasks}


def _requests_live(db):
    rows = db.query(SQL_REQUESTS)
    blockers = sorted({(_i(r.get("blocking_session_id")) or 0) for r in rows
                       if (_i(r.get("blocking_session_id")) or 0) not in (0, -2)})
    blockers = [b for b in blockers if b and b > 0]
    details = []
    if blockers:
        inlist = ",".join(str(b) for b in blockers)
        details = db.query(
            "SELECT s.session_id, s.login_name, s.host_name, s.program_name, s.status, "
            "DB_NAME(s.database_id) AS db_name, s.open_transaction_count AS tran_count, "
            "t.text AS sql_text, s.wait_type, s.wait_time "
            "FROM sys.dm_exec_sessions s WITH (NOLOCK) "
            "LEFT JOIN sys.dm_exec_connections c WITH (NOLOCK) ON c.session_id = s.session_id "
            "OUTER APPLY sys.dm_exec_sql_text(c.most_recent_sql_handle) t "
            "WHERE s.session_id IN (%s)" % inlist)
    for r in rows:
        r["elapsed_s"] = round((_f(r.get("total_elapsed_time")) or 0) / 1000.0, 1)
        r["cpu_s"] = round((_f(r.get("cpu_time")) or 0) / 1000.0, 1)
        r["text_head"] = _head(r.get("sql_text"), 300)
    return {"requests": rows[:60], "blockers": details}


def _qstats(db):
    base = ("SELECT TOP 15 qs.execution_count, qs.total_worker_time, qs.total_elapsed_time, "
            "qs.total_logical_reads, qs.total_physical_reads, qs.total_logical_writes, "
            "{spills} qs.creation_time, qs.last_execution_time, DB_NAME(st.dbid) AS db_name, "
            "OBJECT_SCHEMA_NAME(st.objectid, st.dbid) AS schema_name, "
            "OBJECT_NAME(st.objectid, st.dbid) AS object_name, st.text AS sql_text "
            "FROM sys.dm_exec_query_stats qs WITH (NOLOCK) "
            "OUTER APPLY sys.dm_exec_sql_text(qs.sql_handle) st {where} "
            "ORDER BY {order} DESC")
    by_cpu = db.query_any([
        base.format(spills="qs.total_spills,", where="", order="qs.total_worker_time"),
        base.format(spills="", where="", order="qs.total_worker_time")])
    by_avg = db.query_any([
        base.format(spills="qs.total_spills,", where="WHERE qs.execution_count >= 10",
                    order="(qs.total_elapsed_time / qs.execution_count)"),
        base.format(spills="", where="WHERE qs.execution_count >= 10",
                    order="(qs.total_elapsed_time / qs.execution_count)")])
    by_reads = db.query_any([
        base.format(spills="qs.total_spills,", where="", order="qs.total_logical_reads"),
        base.format(spills="", where="", order="qs.total_logical_reads")])
    dup = []
    try:
        rows = db.query(
            "SELECT TOP 10 qs.query_hash, COUNT(DISTINCT qs.plan_handle) AS plan_count, "
            "SUM(qs.execution_count) AS execs, "
            "MIN(CAST(st.text AS NVARCHAR(4000))) AS sql_text "
            "FROM sys.dm_exec_query_stats qs WITH (NOLOCK) "
            "OUTER APPLY sys.dm_exec_sql_text(qs.sql_handle) st "
            "GROUP BY qs.query_hash HAVING COUNT(DISTINCT qs.plan_handle) >= 5 "
            "ORDER BY plan_count DESC")
        for r in rows:
            qh = r.get("query_hash")
            r["query_hash"] = qh.hex() if isinstance(qh, bytes) else str(qh)
            dup.append(r)
    except Exception:
        pass
    for lst in (by_cpu, by_avg, by_reads):
        for r in lst:
            ec = (_f(r.get("execution_count")) or 0)
            r["avg_elapsed_ms"] = round((_f(r.get("total_elapsed_time")) or 0) / ec, 1) if ec else 0
            r["avg_worker_ms"] = round((_f(r.get("total_worker_time")) or 0) / ec, 1) if ec else 0
            r["avg_reads"] = int((_f(r.get("total_logical_reads")) or 0) / ec) if ec else 0
            r["text_head"] = _head(r.get("sql_text"), 300)
    return {"by_cpu": by_cpu, "by_avg": by_avg, "by_reads": by_reads, "dup_plans": dup}


SHOWPLAN_NS = "{http://schemas.microsoft.com/sqlserver/2004/07/showplan}"


def _plan_issues(db):
    rows = db.query(
        "SELECT TOP 25 CAST(qp.query_plan AS NVARCHAR(MAX)) AS plan_xml, "
        "st.text AS sql_text FROM ("
        " SELECT TOP 25 qs.plan_handle, qs.sql_handle, qs.total_worker_time "
        " FROM sys.dm_exec_query_stats qs WITH (NOLOCK) "
        " ORDER BY qs.total_worker_time DESC) qs "
        "OUTER APPLY sys.dm_exec_query_plan(qs.plan_handle) qp "
        "OUTER APPLY sys.dm_exec_sql_text(qs.sql_handle) st "
        "WHERE qp.query_plan IS NOT NULL")
    converts, spills, nojoin, missing_idx = [], [], [], []
    for r in rows:
        xml = r.get("plan_xml") or ""
        if not xml or len(xml) > 3_000_000:
            continue
        try:
            if xml.lstrip()[:5] == "<?xml":
                xml = re.sub(r"^<\?xml[^>]*\?>", "", xml.strip())
            root = ET.fromstring(xml)
        except Exception:
            continue
        head = _head(r.get("sql_text"), 200)
        for w in root.iter(SHOWPLAN_NS + "Warnings"):
            for ch in w:
                tag = ch.tag.replace(SHOWPLAN_NS, "")
                if tag == "PlanAffectingConvert":
                    converts.append({"issue": ch.get("ConvertIssue"),
                                     "expr": (ch.get("Expression") or "")[:200],
                                     "sql": head})
                elif tag == "SpillToTempDb":
                    spills.append({"sql": head})
                elif tag == "NoJoinPredicate":
                    nojoin.append({"sql": head})
        for _ in root.iter(SHOWPLAN_NS + "MissingIndexes"):
            missing_idx.append({"sql": head})
    return {"converts": converts[:30], "spills": spills[:15],
            "nojoin": nojoin[:10], "missing_in_plans": len(missing_idx)}


def _user_dbs(db):
    rows = db.query("SELECT name FROM sys.databases "
                    "WHERE state_desc = N'ONLINE' AND database_id > 4 "
                    "AND source_database_id IS NULL ORDER BY name")
    return [r["name"] for r in rows]


def _missing_indexes(db):
    out = []
    for dbname in _user_dbs(db)[:80]:
        try:
            rows = db.query(
                "SELECT d.statement AS obj, d.equality_columns, d.inequality_columns, "
                "d.included_columns, gs.unique_compiles, gs.user_seeks, gs.user_scans, "
                "gs.avg_user_impact, gs.last_user_seek FROM {db}.sys.dm_db_missing_index_details d "
                "JOIN {db}.sys.dm_db_missing_index_groups g ON g.index_handle = d.index_handle "
                "JOIN {db}.sys.dm_db_missing_index_group_stats gs "
                "ON gs.group_handle = g.index_group_handle "
                "ORDER BY gs.user_seeks DESC".format(db=qdb(dbname)))
            for r in rows:
                r["db"] = dbname
                out.append(r)
        except Exception:
            continue
    out.sort(key=lambda r: -((_f(r.get("user_seeks")) or 0) *
                             (_f(r.get("avg_user_impact")) or 0)))
    # эскиз DDL для CREATE INDEX
    for r in out[:30]:
        obj = (r.get("obj") or "").strip()
        cols = (r.get("equality_columns") or "").strip()
        if not cols:
            cols = (r.get("inequality_columns") or "").strip()
        inc = (r.get("included_columns") or "").strip()
        if not obj or not cols:
            r["ddl"] = ""
            continue
        short = re.sub(r"[^\w]", "_", obj.split(".")[-1])[:20]
        r["ddl"] = ("CREATE INDEX [ix_mi_{tbl}_{c}] ON {obj} ({cols}){inc}".
                    format(tbl=short, c=re.sub(r"[^\w]", "", cols)[:12],
                           obj=obj, cols=cols,
                           inc=" INCLUDE (%s)" % inc if inc else ""))
    return out[:30]


def _fragmentation(db):
    out = []
    for dbname in _user_dbs(db)[:60]:
        try:
            rows = db.query(
                "SELECT TOP 30 OBJECT_SCHEMA_NAME(ips.object_id) AS sch, "
                "OBJECT_NAME(ips.object_id) AS tbl, i.name AS ix_name, "
                "ips.avg_fragmentation_in_percent AS frag, ips.page_count, "
                "i.fill_factor, i.type_desc FROM sys.dm_db_index_physical_stats("
                "DB_ID({dbn}), NULL, NULL, NULL, N'LIMITED') ips "
                "JOIN sys.indexes i ON i.object_id = ips.object_id AND i.index_id = ips.index_id "
                "WHERE ips.index_id > 0 AND ips.index_level = 0 AND ips.page_count >= 512 "
                "AND ips.avg_fragmentation_in_percent >= 15 "
                "ORDER BY ips.avg_fragmentation_in_percent DESC".
                format(dbn=sql_str(dbname)))
            for r in rows:
                r["db"] = dbname
                out.append(r)
        except Exception:
            continue
    out.sort(key=lambda r: -(_f(r.get("frag")) or 0))
    return out[:60]


def _stats(db):
    out = []
    for dbname in _user_dbs(db)[:80]:
        try:
            rows = db.query(
                "SELECT OBJECT_SCHEMA_NAME(s.object_id) AS sch, OBJECT_NAME(s.object_id) AS tbl, "
                "s.name AS stat_name, sp.last_updated, sp.rows, sp.rows_sampled, "
                "sp.modification_counter, sp.no_recompute FROM {db}.sys.stats s "
                "JOIN {db}.sys.objects o ON o.object_id = s.object_id "
                "CROSS APPLY {db}.sys.dm_db_stats_properties(s.object_id, s.stats_id) sp "
                "WHERE o.is_ms_shipped = 0 AND sp.rows >= 5000".format(db=qdb(dbname)))
            for r in rows:
                r["db"] = dbname
                out.append(r)
        except Exception:
            continue
    return out


def _unused_indexes(db):
    out = []
    for dbname in _user_dbs(db)[:60]:
        try:
            rows = db.query(
                "SELECT OBJECT_SCHEMA_NAME(i.object_id) AS sch, OBJECT_NAME(i.object_id) AS tbl, "
                "i.name AS ix_name, i.type_desc, "
                "ISNULL(us.user_seeks,0)+ISNULL(us.user_scans,0)+ISNULL(us.user_lookups,0) AS reads, "
                "ISNULL(us.user_updates,0) AS updates, "
                "SUM(ps.used_page_count)*8/1024.0 AS size_mb "
                "FROM {db}.sys.indexes i JOIN {db}.sys.objects o ON o.object_id = i.object_id "
                "LEFT JOIN {db}.sys.dm_db_index_usage_stats us ON us.database_id = DB_ID() "
                "AND us.object_id = i.object_id AND us.index_id = i.index_id "
                "JOIN {db}.sys.dm_db_partition_stats ps ON ps.object_id = i.object_id "
                "AND ps.index_id = i.index_id "
                "WHERE o.is_ms_shipped = 0 AND i.index_id > 0 AND i.is_primary_key = 0 "
                "AND i.is_unique_constraint = 0 AND i.is_unique = 0 "
                "GROUP BY i.object_id, i.index_id, i.name, i.type_desc, us.user_seeks, "
                "us.user_scans, us.user_lookups, us.user_updates "
                "HAVING ISNULL(us.user_seeks,0)+ISNULL(us.user_scans,0)+"
                "ISNULL(us.user_lookups,0) = 0 AND ISNULL(us.user_updates,0) >= 500".
                format(db=qdb(dbname)))
            for r in rows:
                r["db"] = dbname
                out.append(r)
        except Exception:
            continue
    return out[:50]


def _databases(db):
    dbs = db.query(
        "SELECT d.name, d.state_desc, d.recovery_model_desc, d.log_reuse_wait_desc, "
        "d.compatibility_level, d.page_verify_option_desc, "
        "d.is_read_committed_snapshot_on, d.is_auto_shrink_on, d.is_auto_close_on, "
        "d.is_auto_create_stats_on, d.is_auto_update_stats_on, "
        "d.snapshot_isolation_state_desc, d.log_reuse_wait, "
        "CAST(CASE WHEN d.database_id <= 4 THEN 1 ELSE 0 END AS INT) AS is_system "
        "FROM sys.databases d WHERE d.state_desc = N'ONLINE' ORDER BY d.database_id")
    files = db.query(
        "SELECT DB_NAME(f.database_id) AS db, f.name AS logical_name, f.type_desc, "
        "CAST(f.size AS BIGINT)*8/1024.0 AS size_mb, f.growth, f.is_percent_growth, "
        "f.physical_name FROM sys.master_files f ORDER BY f.database_id, f.type_desc")
    sizes = {}
    for f in files:
        d = sizes.setdefault(f["db"], {"data_mb": 0.0, "log_mb": 0.0, "files": []})
        if f.get("type_desc") == "LOG":
            d["log_mb"] += _f(f.get("size_mb")) or 0
        else:
            d["data_mb"] += _f(f.get("size_mb")) or 0
        d["files"].append(f)
    for d in dbs:
        d.update(sizes.get(d["name"], {"data_mb": 0, "log_mb": 0, "files": []}))
        try:
            r = db.query_one("SELECT CAST(total_log_size_in_bytes/1048576.0 AS DECIMAL(18,1)) "
                             "AS log_mb, CAST(used_log_space_in_percent AS DECIMAL(9,1)) "
                             "AS used_pct FROM {db}.sys.dm_db_log_space_usage".
                             format(db=qdb(d["name"])))
            d["log_used_pct"] = _f(r.get("used_pct")) if r else None
        except Exception:
            d["log_used_pct"] = None
        try:
            r = db.query_one("SELECT COUNT(*) AS vlf FROM {db}.sys.dm_db_log_info(DB_ID())".
                             format(db=qdb(d["name"])))
            d["vlf"] = _i(r.get("vlf")) if r else None
        except Exception:
            d["vlf"] = None
    return {"databases": dbs, "files": files}


def _io_files(db):
    return db.query(
        "SELECT DB_NAME(vfs.database_id) AS db, mf.name AS logical_name, "
        "mf.type_desc, vfs.num_of_reads, vfs.io_stall_read_ms, vfs.num_of_writes, "
        "vfs.io_stall_write_ms, vfs.size_on_disk_bytes, mf.physical_name "
        "FROM sys.dm_io_virtual_file_stats(NULL, NULL) vfs "
        "JOIN sys.master_files mf ON mf.database_id = vfs.database_id "
        "AND mf.file_id = vfs.file_id")


def _tempdb(db):
    files = db.query("SELECT name, CAST(size AS BIGINT)*8/1024.0 AS size_mb, "
                     "physical_name, type_desc FROM tempdb.sys.database_files")
    space = db.query_one(SQL_TEMPDB) or {}
    oldest = db.query(
        "SELECT TOP 5 session_id, elapsed_time_seconds, "
        "DB_NAME(database_id) AS db FROM "
        "sys.dm_tran_active_snapshot_database_transactions "
        "ORDER BY elapsed_time_seconds DESC")
    return {"files": files, "space": space, "snapshot_tr": oldest}


def _query_store(db):
    out = []
    for dbname in _user_dbs(db)[:80]:
        try:
            r = db.query_one("SELECT actual_state_desc FROM {db}.sys.database_query_store_options".
                             format(db=qdb(dbname)))
            if r:
                out.append({"db": dbname, "state": r.get("actual_state_desc")})
        except Exception:
            continue
    return out


def _backups(db):
    rows = db.query_any([
        "SELECT d.name AS db, d.recovery_model_desc, bf.last_full, bd.last_diff, bl.last_log "
        "FROM (SELECT name, recovery_model_desc FROM sys.databases "
        "WHERE database_id > 4 AND state_desc = N'ONLINE') d "
        "LEFT JOIN (SELECT database_name, MAX(backup_finish_date) AS last_full "
        "FROM msdb.dbo.backupset WHERE type = 'D' AND is_copy_only = 0 "
        "GROUP BY database_name) bf ON bf.database_name = d.name "
        "LEFT JOIN (SELECT database_name, MAX(backup_finish_date) AS last_diff "
        "FROM msdb.dbo.backupset WHERE type = 'I' AND is_copy_only = 0 "
        "GROUP BY database_name) bd ON bd.database_name = d.name "
        "LEFT JOIN (SELECT database_name, MAX(backup_finish_date) AS last_log "
        "FROM msdb.dbo.backupset WHERE type = 'L' "
        "GROUP BY database_name) bl ON bl.database_name = d.name ORDER BY d.name"])
    for r in rows:
        for k in ("last_full", "last_diff", "last_log"):
            v = r.get(k)
            r[k + "_ago_h"] = None
            if hasattr(v, "timestamp"):
                r[k + "_ago_h"] = (datetime.datetime.now() - v).total_seconds() / 3600.0
    return rows


def _jobs_failed(db):
    rows = db.query(
        "SELECT j.name AS job, h.step_id, h.step_name, h.run_date, h.run_time, "
        "h.run_status, h.message FROM msdb.dbo.sysjobhistory h "
        "JOIN msdb.dbo.sysjobs j ON j.job_id = h.job_id "
        "WHERE h.run_status = 0 AND h.step_id > 0 "
        "AND h.run_date >= CONVERT(INT, CONVERT(VARCHAR(8), DATEADD(DAY, -2, GETDATE()), 112)) "
        "ORDER BY h.run_date DESC, h.run_time DESC")
    for r in rows:
        rd, rt = str(r.get("run_date") or ""), str(r.get("run_time") or "").zfill(6)
        try:
            r["run_dt"] = datetime.datetime.strptime(rd + rt, "%Y%m%d%H%M%S")
        except Exception:
            r["run_dt"] = None
        r["message_head"] = _head(r.get("message"), 300)
    return rows[:40]


def _deadlocks(db):
    """Взаимоблокировки из системной сессии system_health (за последние ~24-48 ч)."""
    rows = db.query(
        "SELECT CAST(xest.target_data AS NVARCHAR(MAX)) AS td "
        "FROM sys.dm_xe_session_targets xest "
        "JOIN sys.dm_xe_sessions xe ON xe.address = xest.event_session_address "
        "WHERE xe.name = N'system_health' AND xest.target_name = N'ring_buffer'")
    out = []
    if not rows:
        return out
    xml = rows[0].get("td") or ""
    if len(xml) > 8_000_000:
        return out
    try:
        root = ET.fromstring(xml)
    except Exception:
        return out
    for ev in root.iter("Event"):
        if ev.get("name") != "xml_deadlock_report":
            continue
        for data in ev.iter("data"):
            if data.get("name") != "xml_report":
                continue
            val = data.find("value")
            if val is None or not val.text:
                continue
            out.append(_parse_deadlock(val.text))
        if len(out) >= 25:
            break
    return out


def _parse_deadlock(graph_xml):
    dl = {"time": None, "victim": "", "processes": [], "objects": []}
    try:
        root = ET.fromstring(graph_xml)
    except Exception:
        return dl
    for v in root.iter("victimProcess"):
        dl["victim"] = v.get("id") or ""
    for p in root.iter("process"):
        ib = p.find("inputbuf")
        dl["processes"].append({
            "id": p.get("id"), "spid": p.get("spid"),
            "hostname": p.get("hostname"), "loginname": p.get("loginname"),
            "clientapp": p.get("clientapp"),
            "isolationlevel": p.get("isolationlevel"),
            "waitresource": p.get("waitresource"),
            "waittime": p.get("waittime"), "logused": p.get("logused"),
            "currentdatabase": p.get("currentdatabase"),
            "lastbatchstarted": p.get("lastbatchstarted"),
            "inputbuf": _head(ib.text if ib is not None else "", 300)})
        if p.get("lastbatchstarted") and not dl["time"]:
            try:
                t = datetime.datetime.strptime(p.get("lastbatchstarted"),
                                               "%Y-%m-%dT%H:%M:%S.%f")
                dl["time"] = t
            except Exception:
                pass
    for o in root.iter("objectname"):
        if o.text:
            dl["objects"].append(o.text)
    dl["objects"] = list(dict.fromkeys(dl["objects"]))[:6]
    return dl


ERRORLOG_PATTERNS = [
    r"taking longer than \d+ seconds",       # 833: медленный ввод-вывод
    r"autogrow of database",                # долгие автоприращения
    r"Error:\s*(1[7-9]|2\d)\d*",            # ошибки уровня 17+
    r"not enough memory",
    r"Failed.*(allocate|reserve)",
    r"Scheduler \d+ appears",
    r"SQL Server has encountered",
    r"corrupt",
    r"suspect",
    r"operating system error",
    r"I/O error",
    r"Warning.*SST",
    r"failed to",
    r"Cannot resolve the collation conflict",
    r"insufficient system memory",
]


def _errorlog(db):
    out = []
    pats = [re.compile(p, re.I) for p in ERRORLOG_PATTERNS]
    try:
        for row in db.query_iter("EXEC sys.sp_readerrorlog 0, 1", cap=60000):
            text = row.get("Text") or ""
            if any(p.search(text) for p in pats):
                out.append({"date": row.get("LogDate"),
                            "process": row.get("ProcessInfo"),
                            "text": _head(text, 400)})
                if len(out) >= 300:
                    break
    except Exception as e:
        raise e
    return out
