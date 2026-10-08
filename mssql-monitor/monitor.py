# -*- coding: utf-8 -*-
"""
monitor.py — фоновый движок мониторинга.
Периодически опрашивает сервер (лёгкий тик), пишет данные в SQLite,
фиксирует блокировки и долгие запросы. По завершении — автоотчёт.
"""
import logging
import threading
import time

import checks

log = logging.getLogger("monitor")


class MonitorEngine(threading.Thread):
    """Поток мониторинга. db — объект Database (уже подключённый)."""

    def __init__(self, db, storage, interval=60, duration_s=86400,
                 long_query_s=30, on_sample=None, on_log=None,
                 on_done=None, name="MS SQL Мониторинг"):
        super(MonitorEngine, self).__init__(name=name, daemon=True)
        self.db = db
        self.storage = storage
        self.interval = max(5, int(interval))
        self.duration_s = int(duration_s)
        self.long_query_s = float(long_query_s)
        self.on_sample = on_sample
        self.on_log = on_log
        self.on_done = on_done
        self._stop_event = threading.Event()
        self.state = {"samples": 0, "started": time.time(), "errors": 0}
        self._prev = None
        self._last_long = {}   # session_id -> (elapsed, ts)

    # --------------------------------------------------------------
    def stop(self):
        self._stop_event.set()

    def _log(self, msg):
        log.info(msg)
        if self.on_log:
            self.on_log(msg)

    def _tick(self):
        sample, state = checks.collect_light_sample(self.db, self._prev)
        if self._stop_event.is_set():
            return
        self._prev = state
        if sample.get("dt") is None and self.state["samples"] == 0:
            sample["dt"] = None
        self.storage.save_sample(sample)
        self.state["samples"] += 1

        blocked_rows = sample.pop("_blocked_rows", [])
        # пишем события блокировок (не чаще раза в тик)
        if blocked_rows:
            self.storage.save_blocking(blocked_rows)

        # долгие запросы: фиксируем при первом появлении и каждые +60 сек
        try:
            longs = checks.long_query_events(self.db, self.long_query_s)
        except Exception:
            longs = []
        new_long = []
        for q in longs:
            key = q["session_id"]
            prev_elapsed, _ts = self._last_long.get(key, (0, 0))
            if q["elapsed_s"] >= prev_elapsed + 60 or key not in self._last_long:
                new_long.append(q)
                self._last_long[key] = (q["elapsed_s"], q["ts"])
        if new_long:
            self.storage.save_long_queries(new_long)

        if self.on_sample:
            self.on_sample(sample)
        return sample

    # --------------------------------------------------------------
    def run(self):
        self._log("Мониторинг запущен: интервал %d с, длительность %s, сервер: %s"
                  % (self.interval, _fmt_dur(self.duration_s), getattr(self.db, "server", "?")))
        try:
            self.storage.start_run(getattr(self.db, "server", "?"), self.interval)
        except Exception:
            pass
        end_time = time.time() + self.duration_s
        try:
            while not self._stop_event.is_set():
                try:
                    self._tick()
                except Exception as e:
                    self.state["errors"] += 1
                    self._log("Ошибка опроса: %s" % e)
                    log.exception("tick failed")
                if time.time() >= end_time:
                    break
                self._stop_event.wait(max(0, min(self.interval, end_time - time.time())))
        finally:
            try:
                self.storage.finish_run()
            except Exception:
                pass
            reason = "остановлен пользователем" if self._stop_event.is_set() else "время истекло"
            self._log("Мониторинг завершён (%s). Собрано тиков: %d"
                      % (reason, self.state["samples"]))
            if self.on_done:
                try:
                    self.on_done(self)
                except Exception:
                    log.exception("on_done failed")


def _fmt_dur(sec):
    h, rem = divmod(int(sec), 3600)
    m = rem // 60
    if h and m:
        return "%d ч %d мин" % (h, m)
    if h:
        return "%d ч" % h
    return "%d мин" % m
