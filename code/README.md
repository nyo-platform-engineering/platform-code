# Application and infrastructure code

`apps/` berisi source, Dockerfile, test, dan dokumentasi masing-masing aplikasi. `infrastructure/` berisi cluster k3s lokal pada dua VM, deployment melalui Argo CD, dan observabilitas LGTM.

```text
apps/                            aplikasi dan panduan penggunaannya
infrastructure/local/            definisi VM dan provisioning node
infrastructure/argo-apps/root.yaml  root app-of-apps
infrastructure/argo-apps/platform/  Application komponen platform
infrastructure/argo-apps/dev/       registrasi aplikasi dan Helm values
infrastructure/charts/           chart Helm
infrastructure/scripts/          bootstrap dan operasi
infrastructure/tests/            pemeriksaan konfigurasi infrastructure
```

Setup dan operasi cluster dijelaskan di [README infrastructure](infrastructure/README.md). Kontrak service, image, dependency, dan pengujian aplikasi dijelaskan pada README aplikasi masing-masing di `apps/`.

| Aplikasi | Dokumentasi |
| --- | --- |
| AAT | [Sistem informasi kebencanaan](apps/aat/README.md) |
| Go demo | [Service contoh dengan OpenTelemetry](apps/go-demo/README.md) |
| Signal Deck | [UI trace dan log](apps/telemetry-ui/README.md) |

Go demo dan Signal Deck dijalankan melalui [workflow development lokal](apps/dev/README.md), terpisah dari deployment infrastructure. `task up` di infrastructure memasang AAT dan LGTM; kedua aplikasi ini tidak didaftarkan ke Argo CD.
