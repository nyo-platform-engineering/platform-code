"""Group 3 — overload: criterion 5 (Field Team served while Media is shed)."""
from concurrent.futures import ThreadPoolExecutor
import threading
import time

from common import output, require


def shedding(run):
    media, responder = run.login("public"), run.login("responder")
    stop = threading.Event()
    media_codes, field_codes = [], []

    def flood():
        while not stop.is_set():
            try:
                status, _, _ = run.call("client", "/hazards?limit=1000", token=media,
                                        expected=(200, 429, 503), timeout=30, record=False)
            except RuntimeError:
                status = 0
            media_codes.append(status)

    with ThreadPoolExecutor(run.config.media_workers) as pool:
        for _ in range(run.config.media_workers):
            pool.submit(flood)
        time.sleep(2)
        for _ in range(run.config.field_requests):
            status, _, _ = run.call("client", "/hazards?limit=5", token=responder, expected=(200, 429, 503), timeout=30)
            field_codes.append(status)
        stop.set()
    shed, served = media_codes.count(429), field_codes.count(200)
    require(shed > 0, "Media was never shed: raise CONFIG.media_workers above CLIENT_MAX_CONCURRENT")
    require(served == len(field_codes), f"Field Team requests failed under overload: {field_codes}")
    return {"media_requests": len(media_codes), "media_shed_429": shed, "field_requests": len(field_codes),
            "field_served": served}


def run_group(run):
    input(f"Enter → flood the Client API with {run.config.media_workers} Media clients and send Field Team requests: ")
    r = run.stage("media-shed-field-team-served", shedding, run)
    output("RESULT", "Overload", f"Media: {r['media_shed_429']}/{r['media_requests']} shed with 429",
           f"Field Team: {r['field_served']}/{r['field_requests']} served")
