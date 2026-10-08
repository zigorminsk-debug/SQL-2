# -*- coding: utf-8 -*-
"""
db.py — слой доступа к MS SQL Server.
Поддерживаются два драйвера (выбираются автоматически):
  * pyodbc  — нужен системный "ODBC Driver 17/18 for SQL Server";
  * pymssql — встроенный FreeTDS, внешние зависимости не нужны.
Оба дают одинаковый интерфейс query()/query_one().
Инструмент выполняет только SELECT по системным представлениям (только чтение).
"""

import threading

ODBC_DRIVERS = [
    "ODBC Driver 18 for SQL Server",
    "ODBC Driver 17 for SQL Server",
    "ODBC Driver 13.1 for SQL Server",
    "ODBC Driver 13 for SQL Server",
    "SQL Server Native Client 11.0",
    "SQL Server Native Client 10.0",
    "SQL Server",
]


class DBError(Exception):
    pass


def qdb(name):
    """Экранирование имени базы для [db].sys.view."""
    return "[" + str(name).replace("]", "]]") + "]"


def sql_str(v):
    """Безопасное строковое значение в SQL (N'...')."""
    return "N'" + str(v).replace("'", "''") + "'"


def _norm(v):
    """bytes -> str, прочие значения как есть."""
    if isinstance(v, bytes):
        try:
            return v.decode("utf-8", "replace")
        except Exception:
            return repr(v)
    return v


def rows_to_dicts(cursor):
    desc = cursor.description
    if not desc:
        return []
    names = [d[0] for d in desc]
    out = []
    for row in cursor.fetchall():
        out.append({n: _norm(v) for n, v in zip(names, row)})
    return out


class Database(object):
    """Подключение к MS SQL Server (только чтение)."""

    def __init__(self, server, database="master", windows_auth=True,
                 user="", password="", port=1433, login_timeout=10,
                 query_timeout=60, encrypt=False, trust_cert=True,
                 preferred="auto"):
        self.server = (server or "").strip()
        self.database = database or "master"
        self.windows_auth = windows_auth
        self.user = user or ""
        self.password = password or ""
        self.port = int(port or 1433)
        self.login_timeout = login_timeout
        self.query_timeout = query_timeout
        self.encrypt = encrypt
        self.trust_cert = trust_cert
        self.preferred = preferred
        self.driver_name = None
        self._conn = None
        self._cancelled = threading.Event()
        self._query_lock = threading.Lock()
        self._cursor_lock = threading.Lock()
        self._active_cursor = None

    # ---------------- подключение ----------------
    def connect(self):
        errs = []
        order = []
        if self.preferred == "pyodbc":
            order = ["pyodbc", "pymssql"]
        elif self.preferred == "pymssql":
            order = ["pymssql", "pyodbc"]
        else:
            order = ["pyodbc", "pymssql"]
        for drv in order:
            self.check_cancelled()
            try:
                if drv == "pyodbc":
                    self._connect_pyodbc()
                else:
                    self._connect_pymssql()
                if self._cancelled.is_set():
                    self.close()
                    self.check_cancelled()
                return self
            except DBError as e:
                errs.append(str(e))
            except ImportError:
                errs.append("драйвер %s не установлен" % drv)
            except Exception as e:  # неожиданные ошибки драйверов
                errs.append("%s: %s" % (drv, e))
        raise DBError(
            "Не удалось подключиться к %s. Причины:\n  - %s\n"
            "Подсказки: проверьте имя сервера/порт; убедитесь, что TCP/IP включён; "
            "для pyodbc установите «ODBC Driver 17/18 for SQL Server» "
            "(https://learn.microsoft.com/sql/connect/odbc/download-odbc-driver-for-sql-server)."
            % (self.server, "\n  - ".join(errs)))

    def _srv(self):
        r"""SERVER= для ODBC: host\instance (инстанс) либо host,port."""
        s = self.server
        if "\\" in s:                      # именованный инстанс
            return s
        if self.port and self.port != 1433:
            return "%s,%d" % (s, self.port)
        return s

    def _connect_pyodbc(self):
        import pyodbc
        drivers = [d for d in ODBC_DRIVERS]
        found = getattr(pyodbc, "drivers", lambda: [])()
        avail = [d for d in drivers if d in found]
        if not avail:
            raise DBError("pyodbc: не найден ODBC-драйвер SQL Server в системе")
        last_err = None
        for drv in avail:
            self.check_cancelled()
            cs = ["DRIVER={%s}" % drv, "SERVER=%s" % self._srv(),
                  "DATABASE=%s" % self.database]
            if self.windows_auth:
                cs.append("Trusted_Connection=yes")
            else:
                cs.append("UID=%s" % self.user)
                cs.append("PWD={%s}" % self.password.replace("}", "}}"))
            if drv.startswith("ODBC Driver 1"):
                cs.append("Encrypt=%s" % ("yes" if self.encrypt else "no"))
                if self.encrypt or self.trust_cert:
                    cs.append("TrustServerCertificate=%s" %
                              ("yes" if self.trust_cert else "no"))
            elif self.encrypt:
                cs.append("Encrypt=yes")
            try:
                conn = pyodbc.connect(";".join(cs), timeout=self.login_timeout)
                conn.timeout = self.query_timeout
                self._conn = conn
                self.driver_name = "pyodbc: %s" % drv
                return
            except Exception as e:
                last_err = e
        raise DBError("pyodbc: %s" % last_err)

    def _connect_pymssql(self):
        import pymssql
        kw = dict(server=self.server, port=self.port,
                  database=self.database, login_timeout=self.login_timeout,
                  timeout=self.query_timeout, charset="utf8")
        if self.windows_auth:
            kw["user"] = None
            kw["password"] = None
        else:
            kw["user"] = self.user
            kw["password"] = self.password
        conn = pymssql.connect(**kw)
        self._conn = conn
        self.driver_name = "pymssql (FreeTDS)"
        return

    def close(self):
        if self._conn is not None:
            try:
                self._conn.close()
            except Exception:
                pass
            self._conn = None

    # ---------------- запросы ----------------
    def check_cancelled(self):
        if self._cancelled.is_set():
            raise DBError("Диагностика отменена: программа завершается")

    def cancel(self):
        self._cancelled.set()
        with self._cursor_lock:
            cur = self._active_cursor
            # pyodbc supports cursor.cancel; pymssql is bounded by query_timeout.
            if cur is not None and hasattr(cur, "cancel"):
                cur.cancel()

    def _cursor(self):
        self.check_cancelled()
        if self._conn is None:
            raise DBError("Нет активного подключения")
        return self._conn.cursor()

    def query(self, sql):
        with self._query_lock:
            with self._cursor_lock:
                cur = self._cursor()
                self._active_cursor = cur
            try:
                self.check_cancelled()
                cur.execute(sql)
                if cur.description is None:
                    return []
                return rows_to_dicts(cur)
            finally:
                with self._cursor_lock:
                    self._active_cursor = None
                    cur.close()

    def query_any(self, sqls):
        """Пробует несколько вариантов SQL (совместимость версий)."""
        last = None
        for sql in sqls:
            try:
                return self.query(sql)
            except Exception as e:
                last = e
        raise last if last else DBError("пустой запрос")

    def query_one(self, sql):
        rows = self.query(sql)
        return rows[0] if rows else None

    def query_iter(self, sql, cap=50000, batch=2000):
        """Итерация больших результатов (журнал ошибок) с ограничением."""
        with self._query_lock:
            with self._cursor_lock:
                cur = self._cursor()
                self._active_cursor = cur
            try:
                self.check_cancelled()
                cur.execute(sql)
                if cur.description is None:
                    return
                names = [d[0] for d in cur.description]
                n = 0
                while n < cap:
                    self.check_cancelled()
                    rows = cur.fetchmany(batch)
                    if not rows:
                        break
                    for row in rows:
                        yield dict(zip(names, [_norm(v) for v in row]))
                        n += 1
                        if n >= cap:
                            break
            finally:
                with self._cursor_lock:
                    self._active_cursor = None
                    cur.close()

    def server_now(self):
        row = self.query_one("SELECT GETDATE() AS now")
        return row["now"] if row and row.get("now") else None

    def server_utc_offset_minutes(self):
        row = self.query_one(
            "SELECT DATEDIFF(MINUTE, GETUTCDATE(), GETDATE()) AS off")
        try:
            return int(row["off"]) if row else 0
        except Exception:
            return 0

    def test(self):
        """Быстрая проверка: версия сервера."""
        row = self.query_one("SELECT @@SERVERNAME AS srv, @@VERSION AS v")
        return row or {}
