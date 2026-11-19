"""Temporary PVMBG substitute, using the Python already in the Citus image."""
import base64
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import os
import threading

payload = b"[]"
lock = threading.Lock()


class Handler(BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def reply(self, status, body):
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def authorized(self):
        return self.headers.get("Authorization") == "Bearer " + os.environ["PVMBG_TOKEN"]

    def do_GET(self):
        if self.path == "/health":
            self.reply(200, b'{"ok":true}')
        elif not self.authorized():
            self.reply(401, b'{}')
        elif self.path.split("?")[0] == "/volcanic-reports":
            with lock:
                raw = payload
            self.reply(200, raw)
        else:
            self.reply(404, b'{}')

    def do_POST(self):
        global payload
        if not self.authorized():
            self.reply(401, b'{}')
            return
        if self.path != "/state":
            self.reply(404, b'{}')
            return
        try:
            length = int(self.headers.get("Content-Length", "0"))
            if not 0 < length <= 12 << 20:
                raise ValueError("invalid length")
            raw = base64.b64decode(json.loads(self.rfile.read(length))["raw_base64"], validate=True)
            if len(raw) > 8 << 20:
                raise ValueError("capture limit")
            with lock:
                payload = raw
            self.reply(200, b'{"ok":true}')
        except (ValueError, KeyError, TypeError):
            self.reply(400, b'{}')


if __name__ == "__main__":
    ThreadingHTTPServer(("0.0.0.0", 8082), Handler).serve_forever()
