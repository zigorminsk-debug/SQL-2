"""Track GUI workers and connections; storage is closed only after all finish."""
import threading


class BackgroundTasks:
    def __init__(self):
        self.stopping = threading.Event()
        self._lock = threading.Lock()
        self._threads = []
        self._databases = []

    def start(self, work):
        with self._lock:
            if self.stopping.is_set():
                return False
            self._threads = [t for t in self._threads if t.is_alive()]
            thread = threading.Thread(target=work, daemon=True)
            self._threads.append(thread)
            thread.start()
            return True

    def register(self, db):
        with self._lock:
            if self.stopping.is_set():
                raise RuntimeError("Программа завершается")
            self._databases.append(db)
        return db

    def stop(self):
        with self._lock:
            if self.stopping.is_set():
                return
            self.stopping.set()
            databases = list(self._databases)

            def cancel():
                for db in databases:
                    try:
                        if hasattr(db, "cancel"):
                            db.cancel()
                    except Exception:
                        pass
            # Drivers may take time to acknowledge cancellation; don't block Tk.
            thread = threading.Thread(target=cancel, daemon=True)
            self._threads.append(thread)
            thread.start()

    def alive(self):
        with self._lock:
            return any(t.is_alive() for t in self._threads)

    def close_connections(self):
        if self.alive():
            raise RuntimeError("Фоновые задачи ещё работают")
        with self._lock:
            databases, self._databases = self._databases, []
        for db in databases:
            db.close()
