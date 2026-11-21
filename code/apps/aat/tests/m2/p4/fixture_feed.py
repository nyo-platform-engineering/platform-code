"""The controlled upstream only. Aggregator, Client and Citus remain deployed services."""
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import os
import threading
import time
from p4_fixtures import CASES, burst_events

LOCK = threading.Condition()
STATE = {"events": list(CASES), "released": 0, "generation": 0, "paused": False, "burst": None,
         "normal": 20, "duration": 10, "before": 2, "after": 2, "connections": 0, "last_from": None}
TOKEN = os.environ["P4_FIXTURE_TOKEN"]


def session():
    burst = STATE["burst"]
    elapsed = time.monotonic() - burst if burst is not None else 0
    speed = 1
    available = STATE["released"]
    if burst is not None:
        normal, duration, before, after = (STATE[k] for k in ("normal", "duration", "before", "after"))
        if elapsed < before:
            n = int(elapsed * normal)
        elif elapsed < before + duration:
            speed = 60
            n = int(before * normal + (elapsed - before) * normal * 60)
        else:
            n = int(before * normal + duration * normal * 60 + min(after, elapsed - before - duration) * normal)
        available = min(len(STATE["events"]), len(CASES) + n)
    return {"profile": "controlled-deployment-verification", "speed": speed, "latest_offset": available - 1,
            "elapsed_seconds": elapsed, "burst_end_elapsed_seconds": STATE["before"] + STATE["duration"],
            "published_count": available, "total_events": len(STATE["events"]),
            "stream_connections": STATE["connections"], "last_from": STATE["last_from"]}


class Handler(BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def reply(self, status, value):
        body = json.dumps(value).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def authorized(self):
        return self.headers.get("Authorization") == "Bearer " + TOKEN

    def do_GET(self):
        if self.path == "/health":
            self.reply(200, {"ok": True}); return
        if not self.authorized():
            self.reply(401, {"error": "unauthorized"}); return
        if self.path == "/v1/session":
            with LOCK:
                self.reply(200, session())
            return
        if not self.path.startswith("/v1/stream?"):
            self.reply(404, {}); return
        from urllib.parse import parse_qs, urlsplit
        try:
            cursor = int(parse_qs(urlsplit(self.path).query).get("from", ["0"])[0])
            if cursor < 0:
                raise ValueError("negative offset")
        except ValueError:
            self.reply(400, {}); return
        with LOCK:
            generation = STATE["generation"]
            if STATE["paused"]:
                self.reply(503, {"error": "verification_checkpoint"}); return
            STATE["connections"] += 1
            STATE["last_from"] = cursor
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.end_headers()
        try:
            while True:
                with LOCK:
                    if generation != STATE["generation"]:
                        return  # Intentional transport disconnect; inclusive replay on reconnect.
                    latest = session()["latest_offset"]
                    events = STATE["events"][max(cursor, 0):latest + 1]
                for kind, payload in events:
                    self.wfile.write(f"id: {cursor}\nevent: {kind}\ndata: {json.dumps(payload)}\n\n".encode())
                    cursor += 1
                self.wfile.write(b": heartbeat\n\n")
                self.wfile.flush()
                with LOCK:
                    LOCK.wait(.02 if events else .25)
        except (BrokenPipeError, ConnectionResetError):
            return

    def do_POST(self):
        if not self.authorized():
            self.reply(401, {}); return
        try:
            size = int(self.headers.get("Content-Length", 0))
            if not 0 <= size <= 4096:
                raise ValueError("body too large")
            body = json.loads(self.rfile.read(size) or b"{}")
            with LOCK:
                if self.path == "/admin/reset":
                    STATE.update(events=list(CASES), released=0, generation=STATE["generation"] + 1,
                                 paused=False, burst=None, connections=0, last_from=None)
                elif self.path == "/admin/publish":
                    target = int(body["count"])
                    if not STATE["released"] <= target <= len(STATE["events"]):
                        raise ValueError("publish must advance within the finite stream")
                    STATE.update(released=target, burst=None, paused=False)
                elif self.path == "/admin/burst":
                    rate, duration = int(body["normal_rate"]), int(body["seconds"])
                    if rate < 1 or duration < 1 or rate * duration * 60 > 1000000:
                        raise ValueError("invalid burst size")
                    if STATE["released"] != len(CASES):
                        raise ValueError("all mapping cases must commit before the burst")
                    count = rate * 4 + rate * 60 * duration
                    if count % 2:
                        raise ValueError("burst must contain complete pairs")
                    STATE.update(events=list(CASES) + list(burst_events(count)), normal=rate, duration=duration,
                                 burst=time.monotonic(), paused=False)
                elif self.path == "/admin/replay":
                    STATE.update(generation=STATE["generation"] + 1)
                elif self.path == "/admin/pause":
                    STATE.update(paused=True, generation=STATE["generation"] + 1)
                elif self.path == "/admin/resume-all":
                    count = int(body["count"])
                    if count < len(CASES) or count > 1000000:
                        raise ValueError("invalid finite replay size")
                    STATE.update(events=list(CASES) + list(burst_events(count - len(CASES))), released=count,
                                 burst=None, paused=False, generation=STATE["generation"] + 1)
                else:
                    self.reply(404, {}); return
                LOCK.notify_all()
                self.reply(200, session())
        except (ValueError, KeyError, TypeError) as error:
            self.reply(400, {"error": str(error)})


if __name__ == "__main__":
    ThreadingHTTPServer(("0.0.0.0", 9000), Handler).serve_forever()
