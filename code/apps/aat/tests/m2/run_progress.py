"""Periodic wait messages and a persistent progress trail for guided runs."""
from contextlib import contextmanager
import datetime as dt
import json
from pathlib import Path
import threading
import time


class Progress:
    def __init__(self, folder, stage, clean=lambda value: value, interval=5, writer=None):
        self.path = Path(folder) / "progress.jsonl"
        self.stage, self.clean, self.interval = stage, clean, interval
        self.writer = writer or (lambda message: print("[WAIT] " + message, flush=True))
        self.started = time.monotonic()
        self.state = {"phase": "executing; awaiting next checkpoint", "started": self.started,
                      "timeout": None, "detail": "", "quiet": False}
        self.lock, self.stopped = threading.RLock(), threading.Event()
        self.thread = threading.Thread(target=self.watch, daemon=True)
        self.write_error = None

    def __enter__(self):
        self.thread.start()
        return self

    def __exit__(self, *_):
        self.stopped.set()
        self.thread.join(timeout=2)

    def watch(self):
        while not self.stopped.wait(self.interval):
            self.pulse()

    def pulse(self):
        with self.lock:
            state = dict(self.state)
            if state["quiet"]:
                return
            elapsed = max(0, time.monotonic() - state["started"])
            budget = f" / {state['timeout']:.0f}s" if state["timeout"] is not None else ""
            message = f"{self.stage} | {state['phase']} | {elapsed:.0f}s{budget}"
            if state["detail"]:
                message += " | " + state["detail"]
            message = self.clean(message)
            self.writer(message)
            record = {"at": dt.datetime.now(dt.timezone.utc).isoformat(), "stage": self.stage,
                      "phase": self.clean(state["phase"]), "elapsed_seconds": round(elapsed, 2),
                      "timeout_seconds": state["timeout"], "detail": self.clean(state["detail"])}
            try:
                with self.path.open("a") as stream:
                    stream.write(json.dumps(record) + "\n")
            except OSError:
                if self.write_error is None:
                    self.writer("Cannot write progress.jsonl; check free space and permissions")
                self.write_error = True

    def detail(self, message):
        with self.lock:
            self.state["detail"] = str(message)

    @contextmanager
    def phase(self, description, timeout=None, quiet=False, announce=True):
        with self.lock:
            previous = self.state
            self.state = {"phase": description, "started": time.monotonic(),
                          "timeout": timeout, "detail": "", "quiet": quiet}
            if announce:
                self.pulse()
        try:
            yield
        finally:
            with self.lock:
                self.state = previous
