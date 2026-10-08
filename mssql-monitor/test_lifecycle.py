import threading
import unittest
from unittest.mock import patch
from db import Database, DBError
from lifecycle import BackgroundTasks
from monitor import MonitorEngine


class LifecycleTests(unittest.TestCase):
    def test_monitor_stop_can_join(self):
        # Regression: Thread._stop must not be shadowed by an Event.
        class Store:
            def start_run(self, *args): pass
            def finish_run(self): pass
        eng = MonitorEngine(object(), Store())
        eng.stop()
        eng.start()
        eng.join(2)
        self.assertFalse(eng.is_alive())

    def test_cancelled_tick_not_persisted(self):
        class Store:
            def save_sample(self, sample): raise AssertionError("cancelled tick saved")
        eng = MonitorEngine(object(), Store())
        def collect(*args):
            eng.stop()
            return {}, {}
        with patch('monitor.checks.collect_light_sample', collect):
            eng._tick()
        self.assertEqual(eng.state['samples'], 0)

    def test_shutdown_waits_and_rejects_new_work(self):
        tasks = BackgroundTasks()
        started, cancelled, finished = threading.Event(), threading.Event(), threading.Event()
        class DB:
            closed = False
            def cancel(self): cancelled.set()
            def close(self): self.closed = True
        db = tasks.register(DB())
        def work():
            started.set()
            cancelled.wait(2)
            finished.set()
        self.assertTrue(tasks.start(work))
        self.assertTrue(started.wait(2))
        tasks.stop()
        tasks.stop()
        self.assertFalse(tasks.start(lambda: None))
        self.assertTrue(finished.wait(2))
        # Join registered threads without timing-dependent sleeps.
        for thread in list(tasks._threads): thread.join(2)
        self.assertFalse(tasks.alive())
        self.assertFalse(db.closed)
        tasks.close_connections()
        self.assertTrue(db.closed)
        with self.assertRaises(RuntimeError): tasks.register(DB())

    def test_driver_cancellation_and_cursor_cleanup(self):
        entered, cancelled = threading.Event(), threading.Event()
        class Cursor:
            closed = False
            description = None
            def execute(self, sql):
                entered.set()
                self_wait = cancelled.wait(2)
                if not self_wait: raise AssertionError('cursor was not cancelled')
            def cancel(self): cancelled.set()
            def close(self): self.closed = True
        class Conn:
            def cursor(self): return cursor
        cursor = Cursor()
        db = Database('fake')
        db._conn = Conn()
        task = threading.Thread(target=lambda: db.query('SELECT 1'))
        task.start()
        self.assertTrue(entered.wait(2))
        db.cancel()
        task.join(2)
        self.assertFalse(task.is_alive())
        self.assertTrue(cursor.closed)
        with self.assertRaises(DBError): db.query('SELECT 2')
        with self.assertRaises(DBError): db.connect()


if __name__ == '__main__': unittest.main()
