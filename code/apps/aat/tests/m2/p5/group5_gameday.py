"""Group 5 — criterion 6 rehearsal: combined failures, then no intervention.

One scenario (see gameday.scenario) under 4 minutes: faults for CONFIG.fault_window
seconds, then the test only observes until the system is healthy again on its own
(at most CONFIG.recovery_timeout). The exam profile and Tester Agent cannot be
replayed here; this rehearses the same kinds of failure on the practice lab."""
from common import output, require
import gameday


def combined(run):
    result = gameday.play(run, gameday.scenario(run.config), run.config)
    output("RESULT", "Game day rehearsal",
           f"Recovered on its own after {result['recovered_after_s']}s" if result["recovered_after_s"] is not None
           else f"Did not recover within {run.config.recovery_timeout:.0f}s",
           f"Field Team availability {result['slo']['availability']} (target {run.config.field_availability_slo}, "
           f"error budget used {result['slo']['error_budget_used']})",
           f"Unanswered requests: {result['during_faults']['unanswered']} during, {result['after_faults']['unanswered']} after; "
           f"partial answers during faults: {result['during_faults']['partial_answers']}",
           f"BMKG-only p95 during faults: {result['during_faults']['bmkg_only_p95_ms']} ms")
    require(result["passed"], "; ".join(result["failures"]))
    return {"recovered_after_s": result["recovered_after_s"], "availability": result["slo"]["availability"],
            "error_budget_used": result["slo"]["error_budget_used"],
            "field_p95_ms_during_faults": result["during_faults"]["field_p95_ms"]}


def run_group(run):
    if not run.config.gameday:
        run.skip("gameday-combined", "CONFIG.gameday is False")
        return
    story = gameday.scenario(run.config).story
    input(f"Enter → game day rehearsal ({run.config.fault_window:.0f}s of faults: {story}; "
          f"then no intervention for up to {run.config.recovery_timeout:.0f}s): ")
    run.stage("gameday-combined", combined, run)
