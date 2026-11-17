"""Exercise the background listener using loopback sockets and temporary state."""
import json
from pathlib import Path
import shutil
import socket
import socketserver
import subprocess
import sys
import tempfile
import threading
import unittest

ROOT = Path(__file__).resolve().parents[1]


class EchoConnection(socketserver.BaseRequestHandler):
    def handle(self):
        while data := self.request.recv(65536):
            self.request.sendall(data)


class GatewayTests(unittest.TestCase):
    def setUp(self):
        self.folder = tempfile.TemporaryDirectory()
        self.addCleanup(self.folder.cleanup)
        self.root = Path(self.folder.name)
        self.script = self.root / "scripts/up/gateway.py"
        self.script.parent.mkdir(parents=True)
        shutil.copy(ROOT / "scripts/up/gateway.py", self.script)
        try:
            self.backend = socketserver.ThreadingTCPServer(("127.0.0.1", 0), EchoConnection)
        except PermissionError:
            self.skipTest("Loopback sockets are restricted in this environment")
        threading.Thread(target=self.backend.serve_forever, daemon=True).start()
        self.addCleanup(self.backend.server_close)
        self.addCleanup(self.backend.shutdown)
        self.addCleanup(lambda: self.command("stop"))
        with socket.socket() as free_port:
            free_port.bind(("127.0.0.1", 0))
            self.port = free_port.getsockname()[1]

    def command(self, *args, check=True):
        return subprocess.run(
            [sys.executable, str(self.script), *args],
            capture_output=True, text=True, check=check, timeout=15,
        )

    def start(self):
        return self.command(
            "start", "--port", str(self.port), "--host", "127.0.0.1",
            "--node-port", str(self.backend.server_address[1]),
        )

    def test_background_listener_reuses_process_forwards_and_stops(self):
        self.start()
        state_file = self.root / ".state/gateway.json"
        first = json.loads(state_file.read_text())
        self.assertIn("already running", self.start().stdout)
        self.assertEqual(json.loads(state_file.read_text())["pid"], first["pid"])
        self.assertIn("running on port", self.command("status").stdout)

        payload = b"Host: radar.localhost\r\nUpgrade: websocket\r\n" * 2000
        with socket.create_connection(("127.0.0.1", self.port), timeout=5) as client:
            client.sendall(payload)
            client.shutdown(socket.SHUT_WR)
            response = bytearray()
            while data := client.recv(65536):
                response.extend(data)
        self.assertEqual(response, payload)

        self.command("stop")
        self.assertFalse(state_file.exists())
        self.assertIn("stopped", self.command("status").stdout)
        with self.assertRaises(OSError):
            socket.create_connection(("127.0.0.1", self.port), timeout=1)

    def test_occupied_port_fails_without_recording_success(self):
        with socket.socket() as occupied:
            occupied.bind(("127.0.0.1", self.port))
            occupied.listen()
            result = self.command("start", "--port", str(self.port), check=False)
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse((self.root / ".state/gateway.json").exists())

    def test_stale_state_does_not_signal_an_unrelated_process(self):
        unrelated = subprocess.Popen([sys.executable, "-c", "import time; time.sleep(30)"])
        self.addCleanup(unrelated.wait)
        self.addCleanup(unrelated.terminate)
        state = self.root / ".state"
        state.mkdir()
        (state / "gateway.json").write_text(json.dumps({
            "pid": unrelated.pid, "start_time": "stale", "port": self.port,
        }))
        self.command("stop")
        self.assertIsNone(unrelated.poll())
        self.assertFalse((state / "gateway.json").exists())


if __name__ == "__main__":
    unittest.main()
