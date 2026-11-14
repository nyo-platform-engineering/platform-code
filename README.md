# Platform code

Repositori untuk cluster Kubernetes lokal dan aplikasi yang berjalan di atasnya. Cluster memakai k3s pada tiga VM libvirt/KVM, Argo CD untuk deployment dari Git, dan LGTM untuk observabilitas.

| Bagian | Fungsi |
| --- | --- |
| Compute | Satu control plane yang juga menjalankan workload, dua worker, kernel dan disk terpisah per VM |
| Deployment | Argo CD dengan satu root app-of-apps dan chart Helm |
| Observabilitas | Loki untuk log, Grafana untuk visualisasi, Tempo untuk trace, Mimir untuk metrik |
| Storage | Persistent volume lokal melalui StorageClass `local-path` |
| Jaringan | Jaringan private VM, service internal Kubernetes, NodePort dan port-forward untuk akses host |

```bash
cd code/infrastructure
task doctor
task up
```

Siapkan Linux, libvirt/KVM, Vagrant, plugin `vagrant-libvirt`, Task, kubectl, Helm, dan Python 3 terlebih dahulu. Commit/push konfigurasi sebelum deployment karena Argo CD membaca Git.

[README infrastructure](code/infrastructure/README.md) menjelaskan topologi, setup, dan operasi cluster. Source serta dokumentasi masing-masing aplikasi berada di `code/apps/`.

```text
code/apps/                       source dan dokumentasi aplikasi
code/infrastructure/local/       definisi VM dan provisioning k3s
code/infrastructure/argo-apps/   registrasi platform dan aplikasi
code/infrastructure/charts/      chart Helm
code/infrastructure/scripts/     bootstrap dan operasi cluster
```
