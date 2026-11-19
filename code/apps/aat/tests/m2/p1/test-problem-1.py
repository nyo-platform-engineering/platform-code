#!/usr/bin/env python3
"""Configure and run M2 P1; press Enter between the three groups' test steps."""
# Group 1 (group1_distribution.py):
# 1. Muat dataset katalog gempa historis yang diberikan asisten. Tidak ada satu node pun yang menyimpan seluruh data, dan beban penyimpanan maupun penulisan tersebar ke lebih dari satu node. Laporkan sebaran data per node.
# 2. Tunjukkan bagaimana sistem menyikapi ketidakmerataan alami dataset tersebut. Ukur beban tiap node saat dataset dimuat dan saat lonjakan gempa di satu wilayah disimulasikan.
# 3. Sediakan permintaan yang membaca data lintas seluruh lokasi penyimpanan (contoh: "50 event terbaru dari seluruh jenis bahaya", dengan pagination) dan menghasilkan urutan yang benar.
# Group 2 (group2_recovery.py):
# 4. Bila satu node worker dimatikan dan seluruh datanya dihapus, tunjukkan bahwa tidak ada HazardEvent yang hilang (diverifikasi melalui Audit API terhadap checksum asisten). Selama sistem memulihkan diri, setiap permintaan tetap dijawab: lengkap bila datanya tersedia, atau sebagian dengan penanda eksplisit bahwa jawabannya tidak lengkap, bukan error 500 dan bukan permintaan yang menggantung.
# 5. Tambahkan node baru (atau node yang sama dengan disk kosong). Tunjukkan node tersebut ikut menanggung data dan beban tanpa menghentikan penulisan maupun pembacaan.
# 6. Setelah kriteria 4 dan 5, sistem kembali pada kondisi yang mampu bertahan dari kehilangan satu node berikutnya.
# Group 3 (group3_analytics.py):
# 7. Sediakan satu dashboard analitik yang dapat dibuka melalui peramban dan menampilkan agregasi seluruh HazardEvent per jenis bahaya (gempa, tsunami, gunung berapi, kebakaran hutan, dan pesisir). Dashboard ini berbeda dari dashboard metrik pada bagian Observabilitas dan minimal memuat:
#    - line chart jumlah kejadian per jenis bahaya terhadap waktu, dengan pilihan rentang waktu;
#    - jumlah kejadian per tingkat severity;
#    - jumlah kejadian per wilayah (area_name) dalam bentuk chart atau tabel.
# 8. Angka dihitung dari data lintas seluruh lokasi penyimpanan dan harus sama dengan hasil penghitungan langsung dari Audit API. Bila satu node dimatikan, dashboard tetap tampil dan menandai dengan jelas bahwa angkanya belum lengkap, bukan menampilkan angka keliru tanpa penanda.
from dataclasses import dataclass, replace
import datetime as dt
import math
from pathlib import Path
import shutil
import sys
import uuid

import group1_distribution
import group2_recovery
import group3_analytics
from helper.cluster import endpoint, cluster_topology, worker_action_driver
from helper.data import read_catalog, read_reference, timestamp
from helper.monitor import API, EvidenceAPI
from common import Run as CommonRun
from common import output, INFRA, require, utc


@dataclass
class TestConfig:
    # Development data based on our BMKG mock; replace with official inputs later.
    catalog: Path = Path(__file__).resolve().parent / "data/mock-catalog.ndjson"
    reference: Path = Path(__file__).resolve().parent / "data/mock-reference.json"

    # Criteria 4–6: lose one worker, rebuild it, then lose the other worker.
    worker: str = "aat-worker-1"
    second_worker: str = "aat-worker-2"

    # Enter triggers local worker storage erasure/power loss, then empty-volume rebuild.
    automatic_worker_actions: bool = True

    # Connection: None opens a temporary port-forward to a surviving Aggregator.
    url: str | None = None
    env_file: Path = INFRA / ".env.runtime"
    kubeconfig: Path = INFRA / ".state/kubeconfig"

    # Criteria 1–3: sustained catalog/regional load and latest-50 pagination.
    batch_size: int = 100
    surge_events: int = 100
    load_seconds: float = 20

    # Export in larger pages; the separate latest-50 check still uses exactly 50.
    page_size: int = 200
    max_pages: int = 10000

    # Criterion 7: UTC calendar buckets covering the full catalog time range.
    interval: str = "month"

    # None ends the exact chart comparison after the historical catalog's last day.
    # Set an RFC3339 end date to check another window; changing data is retried.
    analytics_end: str | None = None

    # Per-request bounds and sampling; replica/recovery waits continue until ready.
    request_timeout: float = 8
    max_read_seconds: float = 7
    sample_interval: float = 2
    metrics_interval: float = 5
    min_window_seconds: float = 20

    # None creates a fresh evidence/m2-p1-<timestamp>-<id> folder beside this file.
    output_dir: Path | None = None


CONFIG = TestConfig()


CASE_GUIDE = {
    "catalog-import": ("1 dan 2", "Katalog historis dan sebaran awal",
        "Dataset harus tersebar ke beberapa node; seluruh katalog tidak boleh berada di satu node.",
        "Validasi input, bandingkan data sebelum/sesudah impor, ukur CPU/memori, lalu periksa lokasi setiap event katalog.",
        "catalog-manifest.json, catalog-placement.json, catalog-load-monitor.json, baseline.json"),
    "regional-surge": ("2", "Lonjakan gempa pada satu wilayah",
        "Beban geografis yang timpang tidak boleh hanya dinilai dari rata-rata cluster.",
        "Kirim event baru di Jawa, ukur tiap node selama penulisan, lalu bandingkan penempatan event lonjakan dengan katalog awal.",
        "after-surge.json, regional-surge-monitor.json, regional-surge-metrics.jsonl"),
    "global-reads": ("3", "50 event terbaru lintas penyimpanan",
        "Urutan benar pada satu node belum membuktikan hasil global benar dan lengkap.",
        "Ikuti cursor dengan halaman berisi maksimal 50 event, periksa urutan dan duplikasi, lalu cocokkan dengan Audit API sebelum/sesudah pembacaan.",
        "global-reads.json dan http.jsonl"),
    "first-loss": ("4", "Worker mati dan data lokal dihapus",
        "Replika harus menjaga event yang sudah ada dan penulisan yang telah diakui berhasil.",
        "Hapus volume store, matikan worker, pantau request selama gangguan, lalu cocokkan identitas event dan checksum katalog.",
        "first-loss.json, first-loss-monitor.json, first-loss-storage-erase.json"),
    "first-worker-rejoin": ("5", "Worker bergabung dengan disk kosong",
        "Status Ready saja belum membuktikan worker ikut menyimpan data atau penulisan tetap berjalan.",
        "Nyalakan worker, teruskan probe baca/tulis, tunggu replika pulih, dan verifikasi volume PostgreSQL terbangun kembali.",
        "first-worker-rejoin.json dan first-worker-rejoin-monitor.json"),
    "restored-resilience": ("6", "Bertahan dari kehilangan worker berikutnya",
        "Sesudah pemulihan, sistem harus kembali memiliki cadangan untuk kehilangan satu node lagi.",
        "Setelah rejoin pertama memverifikasi replika, matikan worker yang lain, ulangi pemeriksaan data, lalu pulihkan redundansi lagi.",
        "second-loss.json dan final-rejoin.json"),
    "second-loss": ("6", "Kehilangan worker kedua",
        "Kita menguji redundansi hasil pemulihan pertama, bukan memakai worker yang masih belum pulih.",
        "Matikan worker lain dan periksa kembali data serta respons lengkap/parsial pada probe yang berjalan.",
        "second-loss.json dan second-loss-monitor.json"),
    "final-rejoin": ("6", "Pulihkan redundansi setelah kehilangan kedua",
        "Demo harus berakhir dengan replika yang siap menanggung gangguan berikutnya.",
        "Bangun kembali worker kedua, pastikan ia menyimpan data, dan periksa penulisan yang diakui sampai monitor berhenti.",
        "final-rejoin.json dan final-rejoin-monitor.json"),
    "dashboard-analytics": ("7", "Dashboard analitik dan kecocokan Audit API",
        "Angka harus mencakup penyimpanan lintas node dan tidak boleh mengaku lengkap saat data tidak tersedia.",
        "Hitung ulang lima kategori bahaya, severity, wilayah, dan bucket waktu dari Audit API; bandingkan dengan API yang dipakai dashboard.",
        "dashboard-analytics.json; chart, filter waktu, dan banner parsial diperiksa di peramban"),
}


def print_result(name, result):
    """Keep useful measurements visible; detailed requests stay in evidence files."""
    if not isinstance(result, dict):
        return

    # Each stage returns different measurements; print only the available ones.
    if "records" in result:
        output(
            'RESULT',
            'Catalog',
            f"Records: {result['records']}",
            f"Already stored: {result['already_present']}; changed: {result['changed_records']}",
        )

    if "events" in result:
        output(
            'RESULT',
            'Audit',
            f"Events: {result['events']}",
            'Previous event identities preserved',
        )

    if "records" in result:
        if result["already_present"]:
            output("BATASAN", "Kriteria 1 — impor ulang",
                   f"{result['already_present']} event sudah ada; changed={result['changed_records']}.",
                   "Penempatan dan kelangsungan data tetap diperiksa, tetapi run ini bukan bukti "
                   "bahwa semua event katalog baru ditulis dan bebannya tersebar saat impor awal.")
        areas = result.get("catalog_by_area", {})
        output("DATASET", "Kriteria 2 — ketidakmerataan alami katalog",
               *(f"{area}: {count} record ({count / result['records']:.1%})" for area, count in areas.items()),
               "Distribusi ini dibaca dari katalog asli, bukan dibuat merata untuk mempermudah tes.")

    reference = result.get("reference")
    if reference:
        output(
            'RESULT',
            'Reference',
            f"Events matched: {reference['count']}",
            f"SHA-256: {reference['checksum']}",
        )

    reads = result.get("global_reads", result if "global_order_verified" in result else None)
    if reads:
        output(
            'RESULT',
            'Latest 50 + pagination',
            'Correct global order',
            f"Stable events compared: {reads['stable_events_compared']}",
            f"Live additions/updates: {reads['live_records_added_or_updated']}",
        )

    if reads:
        output("PENJELASAN", "Kriteria 3 — arti hasil pembacaan",
               f"Event hasil pagination: {reads.get('recent_events', '?')}; ukuran halaman: 50.",
               "Urutan global memakai occurred_at terbaru lalu hazard_id sebagai pemecah waktu yang sama.",
               "Perbandingan seluruh dataset identik: " + ("ya" if reads['exact_whole_dataset_comparison'] else
               "tidak; sumber masih menulis. Yang dicocokkan tepat adalah event stabil di antara dua ekspor Audit."))

    for label, placement in (("Placement", result.get("placement", result)),
                             ("Regional surge", result.get("surge_distribution"))):
        if not placement or "copies_per_node" not in placement:
            continue
        rows = ["Node | primary events | stored copies"]
        rows.extend(f"{node} | {placement['primary_events_per_node'].get(node, 0)} | {count}"
                    for node, count in sorted(placement["copies_per_node"].items()))
        output("RESULT", label, *rows)
        scope = placement.get("events", result.get("catalog_events"))
        if scope:
            output("PENJELASAN", f"Sebaran {label} — {scope} event yang diperiksa",
                   f"Primary tersebar di {sum(count > 0 for count in placement['primary_events_per_node'].values())} node; "
                   f"salinan terbanyak pada satu node: {max(placement['copies_per_node'].values())}/{scope} event.",
                   "Kolom stored copies mencakup primary dan replika, jadi jumlahnya dapat melebihi jumlah event unik.",
                   "Primary=0 berarti node hanya memegang replika dalam sampel ini; "
                   "jumlah event bukan ukuran throughput penulisan. Bandingkan CPU/memori dan disk/write IO juga.")

    chart = result.get("analytics", result if "selected_events" in result else None)
    if chart:
        output(
            'RESULT',
            'Analytics',
            f"Events matched against Audit API: {chart['selected_events']}",
            f"Time buckets: {chart['buckets']}; areas: {chart['areas']}",
            'Types: ' + ', '.join((f'{kind}={count}' for kind, count in chart['by_type'].items())),
        )

    if chart:
        window = result.get("analytics_range")
        if window:
            output("CAKUPAN", "Kriteria 7 — rentang angka dashboard",
                   f"{window['from']} sampai sebelum {window['to']}; interval={window['interval']}.",
                   "Angka merupakan data lintas sumber dalam rentang ini; bukan seluruh histori di luar filter waktu.")
        output("RESULT", "Jumlah per severity",
               *(f"{severity}: {count}" for severity, count in chart['by_severity'].items()))
        output("RESULT", "Jumlah per area_name",
               *(f"{row['key']}: {row['count']}" for row in result.get("analytics_by_area", [])))
        labels = {"SEISMIC": "Gempa", "TSUNAMI": "Tsunami", "VOLCANIC": "Gunung berapi",
                  "WILDFIRE": "Kebakaran hutan", "COASTAL": "Pesisir"}
        output("RESULT", "Jumlah per jenis bahaya",
               *(f"{labels.get(kind, kind)}: {count}" for kind, count in chart["by_type"].items()),
               "Tsunami dihitung dari peringatan pada event gempa; satu event dapat berkontribusi "
               "ke Gempa dan Tsunami, sehingga jumlah kategori tidak selalu sama dengan total event unik.")
        empty_types = [kind for kind, count in chart["by_type"].items() if count == 0]
        if empty_types:
            output("BATASAN", "Kategori tanpa kejadian di rentang ini", ", ".join(empty_types),
                   "Nol dapat benar menurut Audit, tetapi belum menunjukkan contoh kejadian untuk semua kategori.")

    monitor = result.get("monitor")
    if monitor:
        output("KETERSEDIAAN", f"Probe selama {name}",
               f"Baca berhasil: {monitor['read_successes']}; parsial eksplisit: {monitor['partial_responses']}; "
               f"latensi terlama: {monitor['max_read_seconds']:.3f}s; error: {len(monitor['errors'])}.",
               f"Tulis diakui pada tahap ini: {monitor['writes_acknowledged']}; "
               f"ditolak/hasil tidak diketahui: {monitor['writes_rejected_or_unknown']}.",
               "Probe memeriksa batas waktu dan metadata partial/unavailable_units. "
               "Ini sampel request selama gangguan, bukan bukti bahwa setiap request semua klien diamati.")
        if monitor.get("tunnel_reconnections"):
            output("CONNECTION", "Gangguan tunnel selama tahap ini",
                   f"Reconnect: {monitor['tunnel_reconnections']}; lihat transport_error/read_retried pada http.jsonl.")
        if name in ("first-loss", "second-loss"):
            output("PENJELASAN", "Tulis saat worker hilang",
                   "Tulis yang ditolak atau belum diketahui hasilnya dihitung terpisah; "
                   "hanya yang diakui berhasil wajib ditemukan kembali. Respons baca gagal tetap menggagalkan tes.")
        else:
            output("PENJELASAN", "Rejoin tanpa interupsi tulis",
                   "Tahap rejoin mewajibkan tidak ada probe tulis gagal; "
                   "node Ready juga harus memiliki salinan data dan volume yang telah dibangun kembali.")

    storage = result.get("storage_loss")
    if storage:
        status = "erased and verified empty" if storage["operation"] == "erase" else "rebuilt PostgreSQL data"
        output(
            'RESULT',
            'Storage',
            f"Worker: {storage['worker']}",
            f"Volumes: {len(storage['volumes'])}; {status}",
        )

    if "disk_erasure_verified" in result and not result["disk_erasure_verified"]:
        output("MANUAL", "Penghapusan disk belum diverifikasi otomatis",
               "Mode manual memerlukan bukti Tester Agent; status Ready/NotReady bukan bukti isi disk dihapus.")

    if "acknowledged_writes_preserved" in result:
        output(
            'RESULT',
            'Durability',
            f"All {result['acknowledged_writes_preserved']} acknowledged test writes are present",
        )


MANUAL_EVIDENCE = [
    "Official assistant dataset/reference provenance and Tester Agent exam logs",
    "VM/native kernels and physical host/topology evidence (container nodes do not qualify)",
    "Browser dashboard: five categories, date filter, severity, area, and partial/stale UI during failure",
    "Per-node disk/write IO and load interpretation from Grafana during import and regional surge",
    "Report: placement, alternatives, consistency guarantees, and contribution explanation",
]


class Run(CommonRun):
    """P1 state with shared M2 stage bookkeeping and evidence serialization."""

    def __init__(self, config, folder):
        # Keep P1's shared state while common.Run handles saving and stage status.
        self.config = config
        self.folder = folder
        self.secrets = set()
        self.api = EvidenceAPI(config, folder)
        self.run_id = folder.name

        # Checkpoints establish what must still exist after the next disruption.
        self.previous_events = []
        self.catalog_identities = set()
        self.acknowledged_writes = set()
        self.surge_refs = set()
        self.storage_plans = {}

        # A successful automated run still needs the manual demo evidence.
        self.report = {
            "problem": "M2 P1",
            "started_at": utc(),
            "mode": "guided",
            "automated_checks": {},
            "manual_evidence_required": list(MANUAL_EVIDENCE),
            "full_demo_certified": False,
        }

    def stage(self, name, operation, *args, **kwargs):
        criterion, title, purpose, steps, evidence = CASE_GUIDE[name]
        output("KRITERIA", f"{criterion} — {title}",
               f"Tujuan: {purpose}", f"Yang dilakukan: {steps}",
               f"Bukti di {self.folder}: {evidence}")
        try:
            result = super().stage(name, operation, *args, **kwargs)
        except (Exception, KeyboardInterrupt):
            output("BELUM TERBUKTI", f"Kriteria {criterion} — {title}",
                   "Tahap berhenti sebelum seluruh pemeriksaannya lolos. "
                   "Lihat error dan http.jsonl; hasil tahap sebelumnya tetap tersimpan.")
            raise
        print_result(name, result)
        output("KESIMPULAN", f"Kriteria {criterion} — pemeriksaan otomatis tahap ini lolos",
               "Kesimpulan berlaku untuk data dan request yang diperiksa; "
               "bukti manual serta batasan yang dicetak tetap berlaku.")
        return result


def verify_environment(run):
    # Check application and audit credentials before contacting the cluster.
    credentials = API(run.config.url or "http://127.0.0.1", run.config.env_file).credentials
    require(
        all((credentials.get(key) for key in ('AGGREGATOR_TOKEN', 'AUDIT_TOKEN'))),
        'AGGREGATOR_TOKEN and AUDIT_TOKEN are required',
    )
    require(
        credentials['AGGREGATOR_TOKEN'] != credentials['AUDIT_TOKEN'],
        'audit token must be distinct from application token',
    )

    # Register secrets so saved reports can redact them.
    run.secrets.update(value for key, value in credentials.items()
                       if len(value) >= 16 and any(word in key for word in ("TOKEN", "SECRET", "PASSWORD", "KEY")))

    # All nodes must be healthy before starting a loss/recovery demonstration.
    topology = cluster_topology(run.config)
    run.save("initial-topology.json", topology)
    nodes = topology["nodes"]
    require(
        len(nodes) >= 3 and all((node['ready'] for node in nodes)),
        'need at least three Ready nodes for this Citus topology',
    )

    # Track the controlled catalog separately from the live mock feeds.
    records = read_catalog(run.config.catalog)
    run.catalog_identities = {("BMKG", record["event_id"]) for record in records}

    # Default to a historical window that live feeds are unlikely to change.
    if run.config.analytics_end is None:
        latest = max(timestamp(record["occurred_at"]) for record in records)
        end = latest.replace(hour=0, minute=0, second=0, microsecond=0) + dt.timedelta(days=1)
        run.config.analytics_end = end.isoformat()
    run.report["analytics_end"] = run.config.analytics_end

    # Record whether this is the development fixture or an external reference.
    reference = read_reference(run.config.reference)
    origin = reference.get("dataset_origin", "external_unverified")
    run.report["dataset_origin"] = origin
    run.report["reference_scope"] = reference.get("scope", "source")
    print(f"Dataset: {origin}; reference scope: {run.report['reference_scope']}", flush=True)
    if origin == "development_mock":
        print(
            'Using our development dataset. Official assistant-dataset proof is still pending.',
            flush=True,
        )

    # Both loss targets must be distinct workers, never control-plane nodes.
    workers = {node["name"] for node in nodes if not node["control_plane"]}
    require(
        run.config.worker != run.config.second_worker and {run.config.worker, run.config.second_worker} <= workers,
        'configure two distinct existing non-control-plane workers in test-problem-1.py',
    )

    # Disk erasure is restricted to the known local VM lab.
    if run.config.automatic_worker_actions:
        for worker in (run.config.worker, run.config.second_worker):
            require(
                worker_action_driver(run.config, worker) == 'vagrant-libvirt',
                'automatic store erasure requires the local Vagrant VM lab',
            )

    run.report["automatic_worker_actions"] = run.config.automatic_worker_actions
    run.report["disk_erasure_verified"] = False
    print("\nM2 P1 — press Enter to continue between test steps.", flush=True)


def validate_config(config):
    require(sys.stdin.isatty(), "run task m2:p1 in an interactive terminal")

    # Resolve both input paths before creating any cluster workload.
    for name in ("catalog", "reference"):
        value = getattr(config, name)
        require(value is not None, f"set CONFIG.{name} in test-problem-1.py to the dataset file")
        path = Path(value).expanduser().resolve()
        require(path.is_file(), f"CONFIG.{name} must point to an existing file")
        setattr(config, name, path)

    # Pagination and load sizes must stay within the API's accepted limits.
    require(1 <= config.page_size <= 1000, "CONFIG.page_size must be 1..1000")
    require(1 <= config.batch_size <= 200, "CONFIG.batch_size must be 1..200")
    require(config.max_pages > 0, "CONFIG.max_pages must be positive")
    require(config.surge_events >= 51, "CONFIG.surge_events must be at least 51")
    require(config.interval in ("hour", "day", "week", "month", "year"), "invalid CONFIG.interval")

    # Reject zero, negative, or non-finite timing settings.
    for name in (
        "request_timeout", "max_read_seconds", "sample_interval",
        "metrics_interval", "min_window_seconds", "load_seconds",
    ):
        duration = getattr(config, name)
        require(
            math.isfinite(duration) and duration > 0,
            f'CONFIG.{name} must be finite and positive',
        )


def create_evidence_folder(config):
    require(sys.stdin.isatty(), "run task m2:p1 in an interactive terminal")
    evidence_root = Path(__file__).resolve().parent / "evidence"
    require(not evidence_root.is_symlink(), "evidence directory must not be a symlink")

    # Only offer to remove previous P1 run folders, never other files or symlinks.
    older_runs = sorted(
        folder for folder in evidence_root.glob("m2-p1-*")
        if folder.is_dir() and not folder.is_symlink()
    )
    for folder in older_runs:
        print(f"  {folder.name}", flush=True)

    answer = input(
        f"Delete {len(older_runs)} older P1 evidence folders in {evidence_root}? [y/N]: "
    ).strip().lower()
    if answer in ("y", "yes"):
        for folder in older_runs:
            shutil.rmtree(folder)

    # Create this run only after the cleanup decision, keeping evidence by default.
    timestamp = dt.datetime.now(dt.timezone.utc).strftime("%Y%m%dT%H%M%S")
    run_name = f"m2-p1-{timestamp}-{uuid.uuid4().hex[:8]}"
    folder = (config.output_dir or evidence_root / run_name).resolve()
    folder.mkdir(parents=True, exist_ok=False)
    return folder


def main():
    if len(sys.argv) > 1:
        print(
            'This test has no command-line options. Edit CONFIG in test-problem-1.py.',
            file=sys.stderr,
        )
        return 2

    # Use a fresh config copy and evidence folder for this run.
    config = replace(CONFIG)
    folder = create_evidence_folder(config)
    run = Run(config, folder)
    exit_code = 1

    try:
        validate_config(config)
        verify_environment(run)

        # Keep the Aggregator endpoint reachable while worker nodes are disrupted.
        with endpoint(config, folder):
            group1_distribution.run_group(run)  # Criteria 1–3: data, load, global reads.

            group2_recovery.run_group(run)      # Criteria 4–6: loss, rejoin, another loss.

            group3_analytics.run_group(run)     # Dashboard correctness and partial results.

        run.report["status"] = "automated_checks_passed_manual_evidence_pending"
        exit_code = 0

    except (Exception, KeyboardInterrupt) as error:
        # Save failures and operator interruptions as unsuccessful runs.
        run.report["status"] = "interrupted" if isinstance(error, KeyboardInterrupt) else "failed"
        message = str(error) if isinstance(error, (ValueError, RuntimeError)) else type(error).__name__
        run.report["error"] = message
        output("FAIL", "P1 check failed", message)

    finally:
        # Always save the outcome, including failed and interrupted runs.
        checks = run.report["automated_checks"]
        run.report["disk_erasure_verified"] = all(
            checks.get(name, {}).get("result", {}).get("disk_erasure_verified", False)
            for name in ("first-loss", "first-worker-rejoin", "second-loss", "final-rejoin")
        )

        run.report["finished_at"] = utc()
        run.report["endpoint_reconnections"] = getattr(config, "endpoint_reconnections", [])
        run.save("result.json", run.report)

        # Show stage outcomes and the evidence directory at the end.
        output(
            'RESULT',
            'Stage summary',
            *(f"{check['status'].upper()}: {name}" for name, check in run.report['automated_checks'].items()),
        )

        if exit_code == 0:
            output("PASS", "All automated P1 checks passed")
        output("INFO", "Evidence", str(folder))
        reconnects = getattr(config, "endpoint_reconnections", [])
        output("CONNECTION", "Ringkasan tunnel pengujian", f"Reconnect selama run: {len(reconnects)}.",
               "Riwayat pod tujuan tersimpan di endpoint_reconnections pada result.json; "
               "retry GET, jika ada, tetap tercatat bersama kegagalan transport awal.")
        for criterion, stages, limitation in (
            (1, ("catalog-import",), "Katalog/checksum resmi dan bukti fresh load tetap diperlukan."),
            (2, ("catalog-import", "regional-surge"), "Bandingkan metrik per node; tambahkan disk/write IO dan penjelasan ketimpangan."),
            (3, ("global-reads",), "Urutan dan kelengkapan dibandingkan pada data stabil di antara dua ekspor Audit."),
            (4, ("first-loss",), "Bukti penghapusan disk otomatis/manual dibedakan; request dipantau dengan probe."),
            (5, ("first-worker-rejoin",), "Ready, penempatan replika, rebuild volume, dan kelangsungan probe diperiksa."),
            (6, ("second-loss", "final-rejoin"), "Kehilangan kedua diuji setelah pemulihan pertama; redundansi akhir diverifikasi."),
            (7, ("dashboard-analytics",), "Chart, filter waktu, lima kategori, dan banner tidak lengkap tetap perlu bukti peramban."),
        ):
            passed = all(checks.get(stage, {}).get("status") == "pass" for stage in stages)
            output("RINGKASAN", f"Kriteria {criterion}: " + ("cek otomatis lolos" if passed else "belum tuntas"), limitation)
        output("MANUAL", "Bukti penyelesaian yang masih diperlukan", *MANUAL_EVIDENCE)
        output(
            'MANUAL',
            'Pending evidence',
            'Official dataset/Tester Agent, browser demo, and report/IO measurements',
        )

    return exit_code


if __name__ == "__main__":
    sys.exit(main())
