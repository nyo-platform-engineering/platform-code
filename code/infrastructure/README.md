# Local Kubernetes cluster

Cluster pengembangan lokal menggunakan **k3s pada tiga VM libvirt/KVM**. Setiap node mempunyai kernel dan disk sendiri. Argo CD merekonsiliasi platform serta workload dari Git; Loki, Grafana, Tempo, dan Mimir menyediakan observabilitas bersama.

## Topologi

| Node | Peran | vCPU | RAM | IP default |
| --- | --- | ---: | ---: | --- |
| Control plane | API, scheduler, workload, backend observabilitas | 2 | 6 GiB | 192.168.56.10 |
| Worker 1 | Workload | 2 | 4 GiB | 192.168.56.11 |
| Worker 2 | Workload | 2 | 4 GiB | 192.168.56.12 |

Ketiga VM berada pada satu host fisik, dengan total alokasi guest 6 vCPU dan 14 GiB RAM. Versi k3s default adalah `v1.34.5+k3s1`; box guest `bento/ubuntu-24.04`. Ukuran VM, hostname, subnet, dan pilihan driver tercantum di [Vagrantfile](local/Vagrantfile).

Satu control plane berarti API dan scheduler belum memiliki redundansi. Storage `local-path` terikat pada disk node; perpindahan pod ke node lain tidak mereplikasi datanya. Backend observabilitas ditempatkan pada control plane melalui `nodeSelector`.

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

Controller NetworkPolicy bawaan k3s tetap aktif. Traefik dan ServiceLB bawaan dinonaktifkan; akses dari host memakai NodePort atau port-forward. Loki, Tempo, dan Mimir memakai volume lokal dengan retensi 72 jam; Grafana memakai PVC 1 GiB.

## Setup

Prasyarat: Linux x86_64, akses `/dev/kvm`, QEMU/libvirt, Vagrant dengan plugin `vagrant-libvirt`, Bash, Python 3, Task, kubectl, dan Helm. Docker diperlukan untuk pemeriksaan container opsional. Pengujian konfigurasi memakai PyYAML (`python3-yaml` pada Ubuntu).

```bash
cd code/infrastructure
task doctor
task up
```

`task up` membuat/menyalakan VM, memasang k3s, menunggu node Ready, menyiapkan namespace serta Secret runtime, memasang Argo CD bila belum ada, lalu menerapkan `argo-apps/root.yaml`. Workload berikutnya direkonsiliasi Argo CD secara asinkron. Bootstrap tidak memasang paket host; download box, k3s, chart, dan image memerlukan internet.

Commit/push konfigurasi terlebih dahulu. Application membaca `https://github.com/nyo-platform-engineering/platform-code.git` pada revision `main`. Untuk fork atau branch lain, sesuaikan URL/revision pada root dan child Applications. Repository private membutuhkan kredensial pada Argo CD.

Untuk cluster VM yang sudah tersedia:

```bash
KUBECONFIG=/path/cluster-vm.yaml task deploy
```

Preflight saat ini membutuhkan minimal tiga node VM/native. Sediakan StorageClass `local-path`, metrics-server, serta CNI yang menegakkan NetworkPolicy; sesuaikan `nodeSelector` backend dengan hostname cluster. Kubeconfig lab disimpan di `.state/kubeconfig`, terpisah dari context default host. Konfigurasi resource dan storage berada di values masing-masing chart.

## GitOps

Root menemukan Application secara rekursif pada `argo-apps/platform/`, dengan filter `{**/app.yaml,**/[0-9][0-9]-*.yaml}`. Values, dashboard, dan manifest workload diproses oleh child Application masing-masing. Ekstensi `.yaml.disabled` menonaktifkan registrasi komponen opsional.

`dev-apps` mendaftarkan workload development dari `argo-apps/dev/`. Ubah chart, values, dan image melalui Git. Namespace, dependency, Secret, serta petunjuk operasi khusus aplikasi didokumentasikan bersama source aplikasi di `../apps/`.

## Akses dan operasi

| Layanan | Akses default |
| --- | --- |
| Grafana | http://192.168.56.10:30300; user `admin`, password dari `task password` |
| Argo CD | `task argocd:ui`, lalu http://localhost:8088; user `admin`, password dari `task password APP=argocd` |
| OTLP internal | `otel-collector-cluster.monitoring.svc.cluster.local`, HTTP 4318 / gRPC 4317 |

```bash
task status               # node, Application, dan workload
task vm:status            # status guest Vagrant
task vm:halt              # matikan VM, pertahankan disk
task up                   # hidupkan kembali dan reconcile
task check                # render dan pemeriksaan konfigurasi offline
task check:containers     # render chart dan validasi binary dalam container sementara
task --list               # seluruh perintah
```

`task vm:destroy` menghapus VM beserta disknya dan meminta konfirmasi melalui Vagrant. Token cluster, kubeconfig, dan kredensial lokal tersimpan pada file yang diabaikan Git di `.state/` dan `.env.runtime`; pertahankan kredensial saat menggunakan kembali volume.

## Struktur

```text
local/                    definisi VM dan provisioning node
argo-apps/root.yaml       entry point app-of-apps
argo-apps/platform/       Application dan values komponen platform
argo-apps/dev/            registrasi dan values workload development
charts/                   chart Helm lokal dan reusable
scripts/                  bootstrap, kredensial, dan operasi VM
tests/                    pemeriksaan konfigurasi dan bootstrap
```
