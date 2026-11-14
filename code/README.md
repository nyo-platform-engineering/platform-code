# Application and infrastructure code

`apps/` berisi source, Dockerfile, test, dan dokumentasi masing-masing aplikasi. `infrastructure/` berisi cluster k3s lokal pada tiga VM, deployment melalui Argo CD, dan observabilitas LGTM.

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
