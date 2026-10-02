# -*- coding: utf-8 -*-
"""
storage.py — локальное хранение результатов мониторинга (SQLite).
Ничего не меняет на SQL Server: только локальные файлы.
"""
import os
import sqlite3
import threading
import time

_LOCK = threading.Lock()

SCHEMA = """
CREATE TABLE IF NOT EXISTS samples(
  ts INTEGER PRIMARY KEY,
  dt REAL, sql_cpu REAL, sys_cpu REAL,
  batch_req REAL, trans_sec REAL, compiles_sec REAL, recompiles_sec REAL,
  lazy_writes REAL, ple INTEGER, bchr REAL, full_scans REAL, index_searches REAL,
  mem_target_mb REAL, mem_total_mb REAL,
  io_rd_lat REAL, io_wr_lat REAL, io_rd_mbps REAL, io_wr_mbps REAL,
  tempdb_used_mb REAL, tempdb_version_mb REAL, sessions INTEGER,
  blocked INTEGER, blocked_max_wait REAL
);
CREATE TABLE IF NOT EXISTS sample_waits(
  ts INTEGER, wait_type TEXT, wait_ms REAL, tasks REAL, signal_ms REAL
);
CREATE INDEX IF NOT EXISTS ix_sample_waits_ts ON sample_waits(ts);
CREATE TABLE IF NOT EXISTS blocking_events(
  ts INTEGER, session_id INTEGER, blocking_session_id INTEGER, wait_type TEXT,
  wait_time_s REAL, wait_resource TEXT, command TEXT, login TEXT, host TEXT,
  program TEXT, db TEXT, elapsed_s REAL, text_head TEXT
);
CREATE INDEX IF NOT EXISTS ix_blocking_ts ON blocking_events(ts);
CREATE TABLE IF NOT EXISTS long_queries(
  ts INTEGER, session_id INTEGER, login TEXT, program TEXT, db TEXT,
  elapsed_s REAL, cpu_s REAL, logical_reads INTEGER, writes INTEGER,
  command TEXT, text_head TEXT, wait_type TEXT
);
CREATE INDEX IF NOT EXISTS ix_long_ts ON long_queries(ts);
CREATE TABLE IF NOT EXISTS runs(
  id INTEGER PRIMARY KEY AUTOINCREMENT, started INTEGER, finished INTEGER,
  interval_s INTEGER, server TEXT, note TEXT
);
"""

SAMPLE_COLS = ["ts", "dt", "sql_cpu", "sys_cpu", "batch_req", "trans_sec",
               "compiles_sec", "recompiles_sec", "lazy_writes", "ple", "bchr",
               "full_scans", "index_searches", "mem_target_mb", "mem_total_mb",
               "io_rd_lat", "io_wr_lat", "io_rd_mbps", "io_wr_mbps",
               "tempdb_used_mb", "tempdb_version_mb", "sessions",
               "blocked", "blocked_max_wait"]


class Storage(object):
    """Потокобезопасное локальное хранилище мониторинга."""

    def __init__(self, path):
        self.path = path
        os.makedirs(os.path.dirname(path), exist_ok=True)
        self._conn = sqlite3.connect(path, check_same_thread=False)
        self._conn.execute("PRAGMA journal_mode=WAL")
        self._conn.execute("PRAGMA synchronous=NORMAL")
        self._conn.executescript(SCHEMA)
        self._conn.commit()

    # ---------- запись ----------
    def start_run(self, server, interval_s, note=""):
        with _LOCK:
            self._conn.execute(
                "INSERT INTO runs(started, finished, interval_s, server, note) "
                "VALUES(?,NULL,?,?,?)", (int(time.time()), interval_s, server, note))
            self._conn.commit()

    def finish_run(self):
        with _LOCK:
            self._conn.execute(
                "UPDATE runs SET finished=? WHERE id=(SELECT MAX(id) FROM runs)",
                (int(time.time()),))
            self._conn.commit()

    def save_sample(self, s):
        with _LOCK:
            row = [s.get(c) for c in SAMPLE_COLS]
            self._conn.execute(
                "INSERT OR REPLACE INTO samples(%s) VALUES(%s)" %
                (",".join(SAMPLE_COLS), ",".join("?" * len(SAMPLE_COLS))), row)
            for w in s.get("waits", []) or []:
                self._conn.execute(
                    "INSERT INTO sample_waits(ts,wait_type,wait_ms,tasks,signal_ms) "
                    "VALUES(?,?,?,?,?)",
                    (s["ts"], w[0], w[1], w[2], w[3]))
            self._conn.commit()

    def save_blocking(self, events):
        if not events:
            return
        with _LOCK:
            for e in events:
                self._conn.execute(
                    "INSERT INTO blocking_events(ts,session_id,blocking_session_id,"
                    "wait_type,wait_time_s,wait_resource,command,login,host,program,"
                    "db,elapsed_s,text_head) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)",
                    (e["ts"], e.get("session_id"), e.get("blocking_session_id"),
                     e.get("wait_type"), e.get("wait_time_s"), e.get("wait_resource"),
                     e.get("command"), e.get("login"), e.get("host"), e.get("program"),
                     e.get("db"), e.get("elapsed_s"), e.get("text_head")))
            self._conn.commit()

    def save_long_queries(self, events):
        if not events:
            return
        with _LOCK:
            for e in events:
                self._conn.execute(
                    "INSERT INTO long_queries(ts,session_id,login,program,db,elapsed_s,"
                    "cpu_s,logical_reads,writes,command,text_head,wait_type) "
                    "VALUES(?,?,?,?,?,?,?,?,?,?,?,?)",
                    (e["ts"], e.get("session_id"), e.get("login"), e.get("program"),
                     e.get("db"), e.get("elapsed_s"), e.get("cpu_s"),
                     e.get("logical_reads"), e.get("writes"), e.get("command"),
                     e.get("text_head"), e.get("wait_type")))
            self._conn.commit()

    # ---------- чтение ----------
    def load_samples(self, t0=None, t1=None, with_waits=True):
        q = "SELECT %s FROM samples" % ",".join(SAMPLE_COLS)
        conds, args = [], []
        if t0 is not None:
            conds.append("ts>=?"); args.append(int(t0))
        if t1 is not None:
            conds.append("ts<=?"); args.append(int(t1))
        if conds:
            q += " WHERE " + " AND ".join(conds)
        q += " ORDER BY ts"
        out = []
        with _LOCK:
            for row in self._conn.execute(q, args):
                s = dict(zip(SAMPLE_COLS, row))
                s["waits"] = []
                out.append(s)
            if with_waits and out:
                wq = ("SELECT ts,wait_type,wait_ms,tasks,signal_ms FROM sample_waits "
                      "WHERE ts BETWEEN ? AND ? ORDER BY ts")
                cur = self._conn.execute(wq, (out[0]["ts"], out[-1]["ts"]))
                idx = {s["ts"]: s for s in out}
                for ts, wt, wms, tasks, sig in cur.fetchall():
                    if ts in idx:
                        idx[ts].setdefault("waits", []).append(
                            (wt, wms or 0.0, tasks or 0.0, sig or 0.0))
        return out

    def load_blocking(self, t0=None, t1=None):
        return self._load_events("blocking_events", t0, t1)

    def load_long_queries(self, t0=None, t1=None):
        return self._load_events("long_queries", t0, t1)

    def _load_events(self, table, t0, t1):
        q = "SELECT * FROM %s" % table
        conds, args = [], []
        if t0 is not None:
            conds.append("ts>=?"); args.append(int(t0))
        if t1 is not None:
            conds.append("ts<=?"); args.append(int(t1))
        if conds:
            q += " WHERE " + " AND ".join(conds)
        q += " ORDER BY ts"
        with _LOCK:
            cur = self._conn.execute(q, args)
            cols = [d[0] for d in cur.description]
            return [dict(zip(cols, r)) for r in cur.fetchall()]

    def sample_count(self, t0=None, t1=None):
        conds, args = [], []
        if t0 is not None:
            conds.append("ts>=?"); args.append(int(t0))
        if t1 is not None:
            conds.append("ts<=?"); args.append(int(t1))
        q = "SELECT COUNT(*), MIN(ts), MAX(ts) FROM samples"
        if conds:
            q += " WHERE " + " AND ".join(conds)
        with _LOCK:
            return self._conn.execute(q, args).fetchone()

    def purge(self, keep_days=14):
        cutoff = int(time.time() - keep_days * 86400)
        with _LOCK:
            for t in ("samples", "sample_waits", "blocking_events", "long_queries"):
                self._conn.execute("DELETE FROM %s WHERE ts<?" % t, (cutoff,))
            self._conn.commit()

    def close(self):
        try:
            self._conn.close()
        except Exception:
            pass
