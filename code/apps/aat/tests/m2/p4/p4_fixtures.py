"""Controlled satellite input and hand-authored expectations; no production mapper imports."""
DATE = "2026-11-12"


def detection(satellite, clock, lat, lon, confidence="h", frp=5, date=DATE):
    return {"detection": {"satellite": satellite, "acq_date": date, "acq_time": clock,
                          "latitude": lat, "longitude": lon, "confidence": confidence, "frp": frp}}


def key(satellite, clock, lat, lon, date=DATE):
    return f"{satellite}|{date}|{clock}|{lat}|{lon}"


A = key("N20", 1200, "0.00000", "110.00000")
B = key("N21", 1201, "0.00100", "110.00000")
C = key("N20", 1202, "0.00200", "110.00000")
CASES = [
    ("detection", detection("N20", 1200, "0.00000", "110.00000")),
    ("detection", detection("N21", 1201, "0.00100", "110.00000", "n", 6)),
    ("detection", detection("N20", 1202, "0.00200", "110.00000", "h", 7)),
    ("update", {"key": A, "changes": {"frp": 100}}),
    ("update", {"key": C, "changes": {"confidence": "l"}}),
    ("retract", {"key": B}),
    ("update", {"key": C, "changes": {"confidence": "h"}}),
    ("retract", {"key": C}),
    ("detection", detection("N20", 1800, "5.00000", "110.00000")),
    # Inclusive six-hour boundary, then one minute beyond it.
    ("detection", detection("N21", 1200, "0.00000", "110.00200", "h", 8)),
    ("detection", detection("N21", 1159, "0.00000", "110.00300", "h", 8)),
    ("detection", detection("N21", 1800, "5.00100", "110.00000", "l", 80)),
]
# offset -> (severity, membership count); None means no HazardEvent should exist.
EXPECTED = {0: None, 1: ("WASPADA", 2), 2: ("WASPADA", 3), 3: ("AWAS", 3),
            4: ("AWAS", 2), 5: ("NORMAL", 1), 6: ("AWAS", 2), 7: ("NORMAL", 1),
            8: ("NORMAL", 1), 9: ("AWAS", 2), 10: ("AWAS", 2), 11: ("AWAS", 2)}


def burst_events(count):
    """One fire per separated pair; two satellites, no low confidence or lateness."""
    for i in range(count):
        pair = i // 2
        lat = f"{-3 + (pair % 100) * .03 + (i % 2) * .001:.5f}"
        lon = f"{100 + (pair // 100) * .03:.5f}"
        yield "detection", detection("N20" if i % 2 == 0 else "N21", 1200, lat, lon, date="2026-11-13")
