#!/usr/bin/env python3
"""Manage a background loopback TCP proxy to the VM's Traefik NodePort."""
import argparse
import json
import os
from pathlib import Path
import selectors
import signal
import socket
import socketserver
import sys
import threading
import time

ROOT = Path(__file__).resolve().parents[2]
STATE = ROOT / ".state"
PID_FILE = STATE / "gateway.json"
LOG_FILE = STATE / "gateway.log"


def process_start_time(pid):
    """Identify the process even if Linux later reuses its numeric PID."""
    try:
        fields = Path(f"/proc/{pid}/stat").read_text().rsplit(") ", 1)[1].split()
        if fields[0] == "Z":
            return None
        return fields[19]
    except (OSError, IndexError):
        return None


def running_gateway():
    if not PID_FILE.exists():
        return None
    state = json.loads(PID_FILE.read_text())
    if process_start_time(state["pid"]) != state["start_time"]:
        return None
    return state


def drop_sudo_privileges():
    """Keep root only long enough to bind the requested low port."""
    if os.geteuid() == 0 and "SUDO_UID" in os.environ:
        os.setgroups([])
        os.setgid(int(os.environ["SUDO_GID"]))
        os.setuid(int(os.environ["SUDO_UID"]))


class ProxyConnection(socketserver.BaseRequestHandler):
    def handle(self):
        try:
            with socket.create_connection(self.server.target, timeout=10) as upstream:
                upstream.settimeout(None)
                # Forward bytes unchanged, including HTTP Host and WebSocket traffic.
                # Half-close the other socket on EOF so pending replies can finish.
                with selectors.DefaultSelector() as ready:
                    ready.register(self.request, selectors.EVENT_READ, upstream)
                    ready.register(upstream, selectors.EVENT_READ, self.request)
                    while ready.get_map():
                        for event, _ in ready.select():
                            source, destination = event.fileobj, event.data
                            data = source.recv(65536)
                            if data:
                                destination.sendall(data)
                            else:
                                ready.unregister(source)
                                destination.shutdown(socket.SHUT_WR)
        except OSError as error:
            print(f"Gateway connection failed: {error}", file=sys.stderr, flush=True)


class ProxyServer(socketserver.ThreadingTCPServer):
    daemon_threads = True
    allow_reuse_address = True


class IPv6ProxyServer(ProxyServer):
    address_family = socket.AF_INET6


def serve(servers, state, ready_pipe):
    """Detach from the terminal and serve until the recorded process is stopped."""
    os.setsid()
    with open(os.devnull) as stdin, LOG_FILE.open("a", buffering=1) as log:
        os.dup2(stdin.fileno(), 0)
        os.dup2(log.fileno(), 1)
        os.dup2(log.fileno(), 2)

    stopped = threading.Event()
    signal.signal(signal.SIGTERM, lambda *_: stopped.set())
    signal.signal(signal.SIGINT, lambda *_: stopped.set())
    state["pid"] = os.getpid()
    state["start_time"] = process_start_time(state["pid"])
    PID_FILE.write_text(json.dumps(state) + "\n")

    try:
        for server in servers:
            threading.Thread(target=server.serve_forever, daemon=True).start()
        print(f"Gateway listening on loopback:{state['port']} -> {state['host']}:{state['node_port']}", flush=True)
        os.write(ready_pipe, b"ready")
        os.close(ready_pipe)
        stopped.wait()
    finally:
        for server in servers:
            server.shutdown()
            server.server_close()
        PID_FILE.unlink(missing_ok=True)


def start(port, host, node_port):
    current = running_gateway()
    if current:
        if (current["port"], current["host"], current["node_port"]) != (port, host, node_port):
            raise ValueError("A gateway with different settings is running. Run task down before changing gateway settings.")
        print(f"Gateway already running on port {port} (PID {current['pid']}).")
        return

    # Bind before forking so an occupied port fails immediately in the terminal.
    servers = [ProxyServer(("127.0.0.1", port), ProxyConnection)]
    try:
        servers.append(IPv6ProxyServer(("::1", port), ProxyConnection))
    except OSError as error:
        # IPv4 still works on hosts where IPv6 loopback is unavailable.
        print(f"IPv6 loopback unavailable: {error}; using IPv4.", file=sys.stderr)
    for server in servers:
        server.target = (host, node_port)

    drop_sudo_privileges()
    STATE.mkdir(mode=0o700, exist_ok=True)
    os.chmod(STATE, 0o700)
    os.umask(0o077)
    ready_read, ready_write = os.pipe()
    pid = os.fork()
    if pid == 0:
        os.close(ready_read)
        try:
            serve(servers, {"port": port, "host": host, "node_port": node_port}, ready_write)
        finally:
            os._exit(0)

    os.close(ready_write)
    for server in servers:
        server.server_close()
    with os.fdopen(ready_read, "rb") as pipe:
        with selectors.DefaultSelector() as ready:
            ready.register(pipe, selectors.EVENT_READ)
            if not ready.select(timeout=10) or pipe.read(5) != b"ready":
                os.kill(pid, signal.SIGTERM)
                raise RuntimeError(f"Gateway failed to start. See {LOG_FILE}.")
    print(f"Gateway started on port {port} (PID {pid}). Stop with task down.")


def stop():
    current = running_gateway()
    if not current:
        PID_FILE.unlink(missing_ok=True)
        print("Gateway is stopped.")
        return
    # The listener dropped sudo privileges, so its owner can stop it without sudo.
    os.kill(current["pid"], signal.SIGTERM)
    for _ in range(50):
        if process_start_time(current["pid"]) != current["start_time"]:
            PID_FILE.unlink(missing_ok=True)
            print("Gateway stopped.")
            return
        time.sleep(0.1)
    raise RuntimeError(f"Gateway did not stop. See {LOG_FILE}.")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=("start", "stop", "status"))
    parser.add_argument("--port", type=int, default=80)
    parser.add_argument("--host", default="192.168.56.10")
    parser.add_argument("--node-port", type=int, default=30088)
    args = parser.parse_args()
    if args.action == "start":
        if not 1 <= args.port <= 65535 or not 1 <= args.node_port <= 65535:
            parser.error("Ports must be 1..65535")
        start(args.port, args.host, args.node_port)
    elif args.action == "stop":
        stop()
    else:
        current = running_gateway()
        if current:
            print(f"Gateway running on port {current['port']} (PID {current['pid']}). Logs: {LOG_FILE}")
        else:
            print("Gateway is stopped.")


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError, RuntimeError) as error:
        print(str(error), file=sys.stderr)
        sys.exit(1)
