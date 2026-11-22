# Autoscaling Aggregator

Default: **OFF**, 3 replika tetap. Argo memasang operator KEDA; ScaledObject
baru dibuat ketika `task autoscaling:on` dijalankan. Grafana tetap menjadi
pengamat, bukan target autoscaling.

Jalankan dari `code/infrastructure` setelah perubahan sudah tersedia di Git dan
Argo Application `keda` selesai sync serta Healthy:

```sh
task autoscaling:status
task autoscaling:on
# Selesai eksperimen, sebelum menjalankan suite M2:
task autoscaling:off
kubectl --kubeconfig .state/kubeconfig -n sistem-informasi-kebencanaan-bnpb \
  rollout status deployment/aggregator --timeout=120s
```

`on`/`off` menolak perubahan ketika aplikasi Argo terkait dijeda atau sedang sync,
P4 verification aktif, atau HPA lain mengontrol Aggregator. Selesaikan dan pulihkan
suite M2 yang sedang berjalan terlebih dahulu. `off` menghapus ScaledObject dan
menunggu HPA hilang sebelum menetapkan kembali 3 replika. `status` hanya membaca.
Kubeconfig dapat diganti lewat `KUBECONFIG`.

## Mekanisme

KEDA membuat HPA untuk Deployment Aggregator dengan target CPU **60% dari request
100m per container** (sekitar 60m), minimum 3, maksimum 5 replika. CPU tidak dibatasi
oleh limit. Metrics-server harus menyediakan sampel CPU; metrik ini tidak
memerlukan jalur OTel. Scale-down memakai stabilization window 120 detik dan
maksimum satu replika turun per 30 detik. Aktivitas feed/background juga
mengonsumsi CPU sehingga dapat mempertahankan jumlah replika saat traffic client
sudah turun. Lima replika memakai maksimum 35 koneksi pool Aggregator dari batas
50 koneksi coordinator yang dikonfigurasi saat ini.

Argo Aggregator sudah mengabaikan `/spec/replicas` dengan
`RespectIgnoreDifferences=true`, sehingga tidak melawan HPA. Native Helm HPA tetap
`autoscaling.enabled: false`; jangan aktifkan bersamaan. Manifest ScaledObject
sengaja berada di luar discovery app-of-apps agar Argo tidak membuatnya kembali
setelah toggle OFF. Keadaan ON bersifat operasional, bukan perubahan nilai Helm.

## Bukti naik dan turun otomatis

Pastikan semua suite M2 berhenti, autoscaling OFF, dan 3 replika Ready. Uji ini
mengirim request baca nyata lewat Client deployment dan memakai kredensial
`RESPONDER_CLIENT_SECRET` dari `.env.runtime`; tidak membuat pod pembakar CPU.
Memerlukan `kubectl`, Python 3, dan `k6` di host.

```sh
task autoscaling:test
# Opsional: URL publik berbeda atau beban lebih lama/besar
task autoscaling:test -- --client-url http://192.168.56.10:30080 \
  --auth-url http://192.168.56.10:30084 --vus 64 --hold 240 --recovery 360
```

Runner mengaktifkan KEDA, menaikkan beban selama 30 detik, menahan 48 VU selama
180 detik, lalu menurunkannya selama 15 detik. Sampel desired/ready, metrik HPA,
hasil HTTP, dan verdict tersimpan di `.state/autoscaling/<timestamp>/`.
Lulus hanya jika replika desired **dan Ready** naik di atas 3 selama beban, lalu
kembali menjadi 3 otomatis setelah beban berakhir, serta pemeriksaan HTTP lulus.
Setelah verdict dicatat, cleanup mematikan autoscaling dan mengembalikan 3 replika;
langkah manual ini tidak dihitung sebagai scale-down otomatis.

Respons 429/503 tercatat sebagai penolakan terkontrol; respons tak terduga atau
kegagalan jaringan menggagalkan uji. Jika CPU tidak mencapai target atau pekerjaan
background mempertahankan beban tinggi, uji gagal membuktikan naik/turun; jangan
menganggapnya bukti autoscaling yang berhasil. Periksa metrik sebelum mengubah
beban/durasi. Ctrl+C menghentikan generator dan mencoba cleanup; jika proses
terpaksa dimatikan, jalankan `task autoscaling:off` sebelum tes M2 berikutnya.

Buka dashboard Grafana AAT yang sudah dideploy: tampilkan panel CPU Aggregator
bersama panel replika available dan rentang waktu uji. Simpan tangkapan layar
kenaikan dan penurunan beserta evidence runner. Instalasi operator dan uji terhadap
cluster harus dijalankan setelah merge; konfigurasi di repo saja belum merupakan
bukti bahwa target bonus telah tercapai.

Rujukan: [KEDA CPU scaler](https://keda.sh/docs/2.21/scalers/cpu/) dan
[KEDA ScaledObject / HPA behavior](https://keda.sh/docs/2.21/reference/scaledobject-spec/).
