"""Group 3 — dashboard aggregates and truthful partial results (criterion 7)."""
# 7. Sediakan satu dashboard analitik yang dapat dibuka melalui peramban dan menampilkan agregasi seluruh HazardEvent per jenis bahaya (gempa, tsunami, gunung berapi, kebakaran hutan, dan pesisir). Dashboard ini berbeda dari dashboard metrik pada bagian Observabilitas dan minimal memuat:
#    - line chart jumlah kejadian per jenis bahaya terhadap waktu, dengan pilihan rentang waktu;
#    - jumlah kejadian per tingkat severity;
#    - jumlah kejadian per wilayah (area_name) dalam bentuk chart atau tabel.
# 8. Angka dihitung dari data lintas seluruh lokasi penyimpanan dan harus sama dengan hasil penghitungan langsung dari Audit API. Bila satu node dimatikan, dashboard tetap tampil dan menandai dengan jelas bahwa angkanya belum lengkap, bukan menampilkan angka keliru tanpa penanda.
from helper.data import read_live_view
from common import output


# 7. Sediakan satu dashboard analitik yang dapat dibuka melalui peramban dan menampilkan agregasi seluruh HazardEvent per jenis bahaya (gempa, tsunami, gunung berapi, kebakaran hutan, dan pesisir). Dashboard ini berbeda dari dashboard metrik pada bagian Observabilitas dan minimal memuat:
# 8. Angka dihitung dari data lintas seluruh lokasi penyimpanan dan harus sama dengan hasil penghitungan langsung dari Audit API. Bila satu node dimatikan, dashboard tetap tampil dan menandai dengan jelas bahwa angkanya belum lengkap, bukan menampilkan angka keliru tanpa penanda.
def dashboard(run):
    # Recompute the displayed aggregates from Audit API data.
    view = read_live_view(run)

    # The API comparison is automated; charts and the partial banner need a browser.
    run.save("dashboard-analytics.json", dict(view, browser_evidence_pending=True))
    output(
        "MANUAL",
        "Manual evidence",
        "Show the browser charts, date filters, and partial/stale banner. "
        "Group 2's monitors check the analytics API during node loss.",
    )

    return view["reconciliation"]


def run_group(run):
    print("\nGroup 3: analytics dashboard — criterion 7", flush=True)

    # 7. Sediakan satu dashboard analitik yang dapat dibuka melalui peramban dan menampilkan agregasi seluruh HazardEvent per jenis bahaya (gempa, tsunami, gunung berapi, kebakaran hutan, dan pesisir). Dashboard ini berbeda dari dashboard metrik pada bagian Observabilitas dan minimal memuat:
    #    - line chart jumlah kejadian per jenis bahaya terhadap waktu, dengan pilihan rentang waktu;
    #    - jumlah kejadian per tingkat severity;
    #    - jumlah kejadian per wilayah (area_name) dalam bentuk chart atau tabel.
    input("Open the analytics dashboard. Enter → compare all charts with Audit API: ")

    # 8. Angka dihitung dari data lintas seluruh lokasi penyimpanan dan harus sama dengan hasil penghitungan langsung dari Audit API. Bila satu node dimatikan, dashboard tetap tampil dan menandai dengan jelas bahwa angkanya belum lengkap, bukan menampilkan angka keliru tanpa penanda.
    run.stage("dashboard-analytics", dashboard, run)
