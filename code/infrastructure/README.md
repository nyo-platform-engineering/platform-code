# Local Kubernetes cluster

Cluster pengembangan lokal menggunakan **k3s pada tiga VM libvirt/KVM**. Setiap node mempunyai kernel dan disk sendiri. Argo CD merekonsiliasi platform serta workload dari Git; Loki, Grafana, Tempo, dan Mimir menyediakan observabilitas bersama.

## Topologi

| Node | Peran | vCPU | RAM | IP default |
| --- | --- | ---: | ---: | --- |
| Control plane | API, scheduler, workload, backend observabilitas | 4 | 6 GiB | 192.168.56.10 |
| Worker 1 | Workload dan dua anggota Citus | 2 | 3 GiB | 192.168.56.11 |
| Worker 2 | Workload dan dua anggota Citus | 2 | 3 GiB | 192.168.56.12 |

Ketiga VM berada pada satu host fisik, dengan total alokasi guest 8 vCPU dan 12 GiB RAM. Versi k3s default adalah `v1.34.5+k3s1`; box guest `bento/ubuntu-24.04`. Ukuran VM, hostname, subnet, dan pilihan driver tercantum di [Vagrantfile](local/Vagrantfile).

Default CPU control plane dinaikkan menjadi 4 vCPU setelah snapshot dua node menunjukkan penggunaan 1381m pada control plane dan 1245m pada worker. `AAT_CONTROL_CPUS` dan `AAT_WORKER_CPUS` mengganti CPU per peran; `AAT_VM_CPUS` tetap dapat mengganti default keduanya. Terapkan perubahan CPU pada guest yang sudah ada dengan `task down` lalu `task up`; perubahan file tidak mengubah CPU VM yang sedang berjalan.

Untuk menerapkan perubahan RAM pada VM yang sudah ada, jalankan `task down` lalu `task up`. Provider libvirt memperbarui alokasi RAM ketika menyalakan guest yang sudah berhenti; disk dan data cluster tetap dipakai. Tidak perlu `task destroy`. Environment variable `AAT_CONTROL_MEMORY` dan `AAT_WORKER_MEMORY`, jika disetel, menggantikan default RAM tersebut (dalam MiB).

Satu control plane berarti API dan scheduler belum memiliki redundansi. Storage `local-path` terikat pada disk node; perpindahan pod ke node lain tidak mereplikasi datanya. Backend observabilitas ditempatkan pada control plane melalui `nodeSelector`.

Tiga pod Aggregator tersebar satu per node. Storage memakai Citus dengan tiga kelompok shard dan dua anggota PostgreSQL per kelompok, ditambah satu coordinator pada control plane. Tiap node menyimpan dua dari tiga kelompok sehingga tidak memiliki seluruh dataset. Patroni mengelola streaming replication dan failover; buffer PostgreSQL 64 MiB serta pool koneksi kecil membatasi overhead. Ketujuh instance storage tetap meminta 512 MiB dan dibatasi 1 GiB sesuai Lampiran B. Budget 12 GiB memberi ruang bagi k3s, aplikasi, dan observabilitas; ini alokasi awal, bukan hasil pengukuran beban akhir.

Uji kehilangan node hanya menargetkan `aat-worker-1` atau `aat-worker-2`. Coordinator dan control plane masih tunggal; topologi ini belum menjamin pemulihan kehilangan control plane.

## Komponen platform

| Komponen | Fungsi |
| --- | --- |
| k3s | Kubernetes, containerd, jaringan Flannel, metrics-server, dan local-path provisioner |
| Argo CD | Deployment deklaratif dan rekonsiliasi dari Git, namespace `argocd` |
| Loki / Tempo / Mimir | Penyimpanan log / trace / metrik, namespace `monitoring` |
| Grafana | Datasource dan dashboard yang diprovisikan dari Git |
| OpenTelemetry cluster collector | Menerima OTLP dan meneruskan setiap sinyal ke backend yang sesuai |
| OpenTelemetry daemon collector | Membaca log pod dan metrik cAdvisor pada setiap node |
| kube-state-metrics | Metrik status resource Kubernetes |
| Traefik / Gateway API | Gateway HTTP bersama, routing berdasarkan hostname `.localhost` |
| Radar | UI untuk melihat resource, relasi, dan status cluster Kubernetes |

Controller NetworkPolicy bawaan k3s tetap aktif. Traefik dan ServiceLB bawaan dinonaktifkan; Argo CD memasang Traefik terpisah dengan Gateway API. Akses dari host memakai tunnel loopback ke gateway atau NodePort. Loki, Tempo, dan Mimir memakai volume lokal dengan retensi 72 jam; Grafana memakai PVC 1 GiB.

Grafana Traces Drilldown membutuhkan TraceQL metrics. Tempo 2.9 menjalankan metrics-generator dengan processor `local-blocks`, mencakup seluruh jenis span dan menyimpan blok metrics pada volume Tempo. Data untuk grafik mulai tersedia dari trace yang masuk setelah processor diaktifkan; trace lama tetap dapat dicari, tetapi tidak otomatis diolah ulang menjadi metrics. Processor ini berbeda dari `span-metrics`/`service-graphs` yang menghasilkan seri Prometheus.

Request CPU platform memakai `1,4 × rata-rata teramati`, sedangkan memory memakai `1,4 × peak teramati`, dibulatkan ke atas ke millicore dan Mi. Angka request ditulis langsung di masing-masing `values.yaml` yang dipakai Argo CD. CPU memakai rata-rata seluruh sampel rate satu menit yang tersedia dalam jendela enam jam, dievaluasi setiap 30 detik dan dibobot berdasarkan jumlah sampel antar-replika serta container lama; memory memakai nilai terbesar antara high-water container saat ini dan histori working set. Histori belum mencakup seluruh startup sebelum collector aktif, sehingga angka ini perlu diukur ulang setelah startup dan beban yang representatif. Pengukuran dilakukan pada 17 November 2026; cakupan CPU mengikuti histori yang tersedia dalam jendela tersebut; request tidak berubah otomatis. Memory limit tetap menyediakan ruang lonjakan; resource AAT tetap mengikuti ketentuan tugas.

Dashboard Grafana **Node utilization** (`/d/node-utilization`, folder AAT) menampilkan CPU average/peak, RAM utilization average/peak, pemakaian root disk, disk/network throughput, serta load average per VM. Pilih node dan rentang waktu untuk membandingkan kondisi idle, startup, dan beban tugas sebelum mengecilkan VM. CPU memakai `system.cpu.utilization`, menjumlahkan state selain idle per CPU lalu merata-ratakan seluruh vCPU. RAM memakai `system.memory.utilization{state="used"}`, dengan cache dan buffer dilaporkan terpisah oleh OTel; definisinya berbeda dari `MemTotal − MemAvailable` dan working set Kubernetes. Grafik CPU/RAM baru terisi setelah metrik opt-in ini di-deploy; histori sebelumnya tidak diisi ulang.

OTel daemon mengumpulkan metrik OS dengan receiver `hostmetrics` setiap 30 detik, memberi label `node`, lalu mengirim lewat cluster collector ke Mimir. Agent membaca root filesystem VM melalui mount read-only `/hostfs`; `hostNetwork` memastikan metrik network berasal dari interface VM. Tidak ada collector tambahan. Root disk saja yang diukur untuk filesystem; interface virtual pod dan loopback dikecualikan dari network. Histori mulai tersedia setelah konfigurasi tersinkron oleh Argo CD; perlu beberapa scrape sebelum grafik rate terisi, dan histori sebelum itu tidak diisi ulang. Nilai request VM/container tidak diubah otomatis oleh dashboard.

## Setup

Prasyarat: Linux x86_64, akses `/dev/kvm`, QEMU/libvirt, Vagrant dengan plugin `vagrant-libvirt`, Bash, Python 3, Task, kubectl, dan Helm. Docker diperlukan untuk pemeriksaan container opsional. Pengujian konfigurasi memakai PyYAML (`python3-yaml` pada Ubuntu).

```bash
cd code/infrastructure
task doctor
task up
```

`task up` membuat/menyalakan VM, memasang k3s, menunggu node Ready, menyiapkan namespace serta Secret runtime, memasang Argo CD bila belum ada, lalu menerapkan `argo-apps/root.yaml`. Workload berikutnya direkonsiliasi Argo CD secara asinkron. Bootstrap tidak memasang paket host; download box, k3s, chart, dan image memerlukan internet.

Taskfile memanggil entry point `main.sh` pada folder masing-masing:

| Task | Entry point |
| --- | --- |
| `doctor` | `scripts/doctor/main.sh` |
| `up` | `scripts/up/main.sh` |
| `status` | `scripts/status/main.sh` |
| `links` | `scripts/links/main.sh` |
| `password` | `scripts/password/main.sh` |
| `context` | `scripts/context/main.sh` |
| `k9s` | `scripts/k9s/main.sh` |
| `vm:manager` | `scripts/vm/main.sh manager` |
| `verify:storage-isolation` | `scripts/verify/storage-isolation.py` |
| `tester:*` | `scripts/tester/main.sh` dengan aksi install/list/practice |
| `down` (alias `vm:down`, `vm:halt`) | `scripts/vm/main.sh halt` |
| `destroy` (alias `vm:destroy`) | `scripts/vm/main.sh destroy` |

Startup dikoordinasikan oleh `scripts/up/main.sh`, dengan langkah terpisah di
`scripts/up/`:

| Script | Tanggung jawab |
| --- | --- |
| `1-vms.sh` | Menjalankan Vagrant dan mengekspor kubeconfig langsung dari VM |
| `2-cluster.sh` | Menunggu node, memeriksa topologi, menyiapkan namespace dan Secret |
| `3-argocd.sh` | Memasang Argo CD bila belum ada dan menunggu rollout |
| `4-apps.sh` | Mendaftarkan root Application, menunggu gateway, dan menjalankan listener background `.localhost` |

Setiap script memiliki `preflight_check()` sendiri: langkah VM memeriksa tooling
Vagrant/libvirt/KVM, langkah cluster memastikan ketiga node VM tersedia dan Ready,
langkah Argo CD memastikan API Kubernetes siap, dan langkah aplikasi menunggu CRD
Application tersedia. `task doctor` hanya menjalankan preflight VM tanpa menyalakan
atau mengubah guest.

`common.sh` menyediakan environment, kubeconfig, dan helper bersama. Helper Python
untuk topologi dan kredensial (`topology.py` dan `runtime.py`) juga berada di
`scripts/up/`. Script startup tidak memanggil script di luar direktori tersebut;
`scripts/vm/main.sh` menyediakan perintah operasi VM dan meneruskan startup ke `1-vms.sh`.
`task up` menjalankan langkah 1–4. Untuk diagnosis, setiap langkah dapat
dipanggil langsung dengan `bash`; nomor menunjukkan urutan tanggung jawab,
bukan glob eksekusi.

Commit/push konfigurasi terlebih dahulu. Application membaca `https://github.com/nyo-platform-engineering/platform-code.git` pada revision `main`. Untuk fork atau branch lain, sesuaikan URL/revision pada root dan child Applications. Repository private membutuhkan kredensial pada Argo CD.

`task up` selalu menggunakan tiga VM Vagrant/libvirt dan kubeconfig lab di
`.state/kubeconfig`. Tidak ada mode startup tanpa VM.

Untuk hanya menjalankan deployment ke cluster yang sudah tersedia:

```bash
export KUBECONFIG=/path/cluster-vm.yaml
bash scripts/up/2-cluster.sh
bash scripts/up/3-argocd.sh
bash scripts/up/4-apps.sh
```

Preflight saat ini membutuhkan minimal tiga node VM/native. Sediakan StorageClass `local-path`, metrics-server, serta CNI yang menegakkan NetworkPolicy; sesuaikan `nodeSelector` backend dengan hostname cluster. Kubeconfig lab disimpan di `.state/kubeconfig`, terpisah dari context default host. Konfigurasi resource dan storage berada di values masing-masing chart.

### Verifikasi isolasi storage

Setelah rollout selesai dan seluruh anggota Citus Ready, jalankan:

```bash
task verify:storage-isolation
```

Pemeriksaan memakai pod aplikasi yang sedang berjalan, tanpa mengubah policy
atau data. Seluruh Aggregator dan anggota Citus harus dapat membuka koneksi ke
port PostgreSQL `5432` serta Patroni `8008` pada setiap anggota, termasuk replica.
Pod aplikasi lain harus ditolak. Service coordinator `postgres:5432` ikut diuji.
Health endpoint tiap Aggregator juga harus berhasil melakukan ping database
dengan koneksi aplikasi yang sudah dikonfigurasi.

Setiap pod terlebih dahulu diuji ke port Aggregator yang terbuka sebagai kontrol
positif. Pod yang tidak dapat menjalankan probe atau terganggu rollout membuat
hasil gagal/inconclusive, bukan dianggap berhasil diblokir oleh NetworkPolicy.
Kegagalan expectation membuat command keluar nonzero. Bukti JSON, label/UID pod,
node, alamat target, dan policy tersimpan di
`.state/storage-networkpolicy/results.json` untuk laporan.
Perubahan pod, policy, atau Service selama pengujian juga menggagalkan hasil;
ulangi setelah rollout stabil agar penolakan tidak berasal dari target yang hilang.

Pengecualian namespace resmi `tester-agent` tetap dipertahankan pada policy;
command ini menguji pod aplikasi dan tidak membuat atau menyamar sebagai Tester Agent.

## GitOps

Root menemukan Application secara rekursif pada `argo-apps/platform/`, dengan filter `{**/app.yaml,**/[0-9][0-9]-*.yaml}`. Values, dashboard, dan manifest workload diproses oleh child Application masing-masing. Ekstensi `.yaml.disabled` menonaktifkan registrasi komponen opsional.

`dev-apps` mendaftarkan workload development dari `argo-apps/dev/`. Ubah chart, values, dan image melalui Git. Namespace, dependency, Secret, serta petunjuk operasi khusus aplikasi didokumentasikan bersama source aplikasi di `../apps/`.

## Akses dan operasi

Untuk memakai cluster VM dari kubectl/K9s dan melihat guest melalui GUI:

```bash
task context       # import/refresh context aat-local ke ~/.kube/config
task k9s           # refresh context, lalu buka K9s pada semua namespace
task vm:manager    # buka Virtual Machine Manager pada qemu:///system
```

Import menyimpan cluster, user, dan context dengan nama `aat-local`, sehingga
tidak bertabrakan dengan nama `default` milik k3s. Context lain dan pilihan aktif
yang sudah ada dipertahankan; jika belum ada pilihan aktif, `aat-local` dipilih.
K9s selalu memilih `aat-local` secara eksplisit. Kubeconfig tujuan dan backup
`~/.kube/config.before-aat-local` disimpan dengan permission 0600.
Jalankan ulang `task context` setelah kubeconfig lab berubah atau cluster dibuat
ulang. Gunakan `KUBE_CONTEXT_FILE` atau `KUBE_CONTEXT_NAME` bila ingin lokasi/nama
lain. Perintah membutuhkan kubectl/Python; K9s dan virt-manager harus terpasang
untuk UI masing-masing. `task up` tetap menggunakan `.state/kubeconfig` milik lab.

`task up` menunggu Traefik dan Gateway siap, lalu menjalankan listener `.localhost`
di background. Terminal boleh ditutup; listener tetap hidup sampai dihentikan,
prosesnya berakhir, atau laptop dimatikan. Menjalankan ulang startup dengan
konfigurasi yang sama memakai proses yang sudah ada.

```bash
task up                   # start/reuse VM, aplikasi, dan listener
task status               # status cluster dan listener
task down              # hentikan listener dan matikan VM, pertahankan disk
```

Port default `80` meminta sudo untuk bind, lalu helper Python melepas privilege
root dan menjalankan listener sebagai user pemanggil. Stop/status tidak memerlukan
sudo atau koneksi Kubernetes. Gunakan `task up PORT=8080` untuk port tinggi, lalu tambahkan `:8080` pada URL.
Untuk mengganti port/tujuan listener yang sudah hidup, jalankan `task down`
lalu `task up` dengan konfigurasi baru. PID dan identitas proses disimpan di `.state/gateway.json`;
log berada di `.state/gateway.log`. Listener lama yang sudah mati dapat dimulai ulang.

`task down` menghentikan listener sebelum mematikan VM. `task destroy` menghentikan
listener setelah perintah penghancuran VM selesai; konfirmasi Vagrant tetap berlaku.

### Waktu awal mock BMKG/PVMBG

Atur satu nilai bersama di `.env.runtime`:

```dotenv
START_TIME=2026-11-18T07:00:00+07:00
```

Gunakan RFC3339 dengan zona waktu, tanpa tanda kutip. Nilai eksplisit mengatur
ConfigMap `aat-clock` dan `.state/mock-start-time`. Bila kosong/belum ada,
bootstrap mengambil waktu awal yang sudah tersimpan, kemudian menuliskannya ke
`.env.runtime`; lab baru memakai waktu saat bootstrap pertama. Nilai itu tetap
dipakai saat restart berikutnya.

Setelah mengedit, terapkan konfigurasi dan restart kedua mock agar env baru dibaca:

```bash
python3 scripts/up/runtime.py
kubectl --kubeconfig .state/kubeconfig -n sistem-informasi-kebencanaan-bnpb \
  rollout restart deployment/bmkg deployment/pvmbg
```

`task up` juga menerapkan konfigurasi tersebut; pod yang sudah berjalan tetap
memerlukan restart untuk membaca perubahan env. Mengubah waktu awal mock tidak
menghapus data atau cursor ingestion yang sudah tersimpan.

### Sinkronisasi waktu VM

Chrony dijalankan saat boot dan provisioner `clock` diperiksa setiap `task up`,
termasuk pada VM yang sudah pernah diprovision. Control plane memakai NTP Ubuntu;
worker mengutamakan control plane melalui jaringan lokal dan tetap memiliki NTP
upstream sebagai fallback. Referensi bersama mengurangi perbedaan timestamp
antar-VM yang membuat child span HTTP tampak berada di luar parent pada waterfall.

```bash
task clock:sync     # terapkan konfigurasi pada VM yang sedang berjalan
task clock:status   # ringkasan sumber waktu, sisa koreksi, dan umur sampel tiap VM
task clock:status -- --verbose  # laporan chrony lengkap
```

Ringkasan menampilkan `SYNCED` bila sumber terpilih sehat, sampelnya belum lama,
dan sisa koreksi paling besar 1 ms. Status `CHECK` menyertakan alasan, misalnya
worker memakai fallback atau clock masih mengejar koreksi; `ERROR` berarti
laporan VM tidak dapat dibaca. `CORRECTION LEFT` adalah koreksi lokal yang masih
tertunda. Estimasi ketidakpastian UTC ditampilkan terpisah karena tidak sama
dengan selisih jam worker terhadap control plane.

Provisioner menunggu sinkronisasi dan sisa koreksi clock di bawah 1 ms, maksimal
60 detik tiap pemeriksaan. Worker juga menunggu referensi control plane terpilih
hingga 60 detik, lalu memeriksa koreksinya lagi; kegagalan menghentikan startup.
Angka tersebut adalah sisa koreksi lokal, bukan jaminan akurasi absolut UTC
atau batas skew antar-VM.
Koreksi normal dilakukan bertahap; hanya selisih di atas satu detik pada tiga
update awal daemon yang boleh melompat. Tidak ada loop `makestep` atau hook
resume yang melompati gangguan jam Tester Agent. Setelah gangguan/freeze selesai,
periksa lagi sumber dan offset sebelum mengambil bukti trace baru. Trace lama
tetap memakai timestamp yang direkam saat itu.

Rujukan konfigurasi: [chrony](https://chrony-project.org/doc/4.5/chrony.conf.html).

Nama `.localhost` mengarah ke loopback laptop, bukan IP VM. Browser modern
umumnya menanganinya langsung; bila resolver lokal tidak mendukung subdomain ini,
tambahkan hostname yang dipakai ke `/etc/hosts` dengan alamat `127.0.0.1`.
Tidak perlu mengubah DNS jaringan atau mengarahkan `.localhost` ke IP VM.

Alur request: browser → loopback laptop → proxy TCP ke NodePort VM → Traefik → HTTPRoute
berdasarkan hostname → Service → Pod. Gateway `local` memiliki listener HTTP
`web` pada port container 8000, yang diekspos Service pada port 80 dan NodePort
30088. Route berada pada namespace backend masing-masing; listener mengizinkan
route lintas namespace. Radar mengelola HTTPRoute melalui chart-nya. Dashboard
Traefik memakai IngressRoute untuk service internal `api@internal`.

| Layanan | Akses default |
| --- | --- |
| Dashboard analitik dan Client API | http://client.localhost; login analyst, kunci dari `task password` |
| Auth | http://auth.localhost |
| Radar | http://radar.localhost |
| Grafana | http://grafana.localhost; user `admin`, password dari `task password` |
| Argo CD | http://argocd.localhost; user `admin`, password dari `task password` |
| Traefik dashboard | http://traefik.localhost/dashboard/ |
| OTLP internal | `otel-collector-cluster.monitoring.svc.cluster.local`, HTTP 4318 / gRPC 4317 |

NodePort lama tetap tersedia: Client `http://192.168.56.10:30080`, Auth
`http://192.168.56.10:30084`, dan Grafana `http://192.168.56.10:30300`.
Untuk tunnel Argo CD terpisah, jalankan
`kubectl --kubeconfig .state/kubeconfig port-forward -n argocd service/argocd-server 8088:80`
dan buka `http://localhost:8088`.
Untuk menguji gateway langsung tanpa listener lokal:

```bash
curl -H 'Host: radar.localhost' http://192.168.56.10:30088/
```

Gateway API CRD versi `v1.5.1` dipasang oleh Application `gateway-api` dari repo
upstream. Application tersebut tidak melakukan auto-prune CRD bersama. Application
Traefik, Radar, dan gateway memakai retry agar instalasi pertama dapat pulih saat
CRD atau namespace backend belum tersedia. Periksa `task status` dan, untuk routing:

```bash
kubectl --kubeconfig .state/kubeconfig get gateway -n traefik
kubectl --kubeconfig .state/kubeconfig get httproute -A
```

```bash
task status               # node, Application, dan workload
task down              # matikan VM, pertahankan disk
task up                   # hidupkan kembali dan reconcile
task --list               # seluruh perintah
```

Pemeriksaan development dapat dijalankan langsung tanpa task tambahan:

```bash
/usr/bin/python3 -m unittest discover -s tests -p 'test_local_*.py' -v
/usr/bin/python3 scripts/check-lgtm.py  # opsional, membutuhkan Docker
```

`task destroy` menghapus VM beserta disknya dan meminta konfirmasi melalui Vagrant. Token cluster, kubeconfig, dan kredensial lokal tersimpan pada file yang diabaikan Git di `.state/` dan `.env.runtime`; pertahankan kredensial saat menggunakan kembali volume.

## Struktur

```text
local/                    definisi VM dan provisioning node
argo-apps/root.yaml       entry point app-of-apps
argo-apps/platform/       Application dan values komponen platform
argo-apps/dev/            registrasi dan values workload development
charts/                   chart Helm lokal dan reusable
scripts/                  status, links, password, tester, dan operasi VM
scripts/up/               main.sh, langkah startup, kredensial, dan gateway
tests/                    pemeriksaan konfigurasi dan bootstrap
```
