# Development apps

[`kustomization.yaml`](kustomization.yaml) registers seven AAT services and their dependencies under `aat/`, managed through `../root.yaml`.

Each service has `app.yaml`, `values.yaml`, and `image.yaml`. Workloads run in `sistem-informasi-kebencanaan-bnpb`. Update configuration in Git, then commit/push before `task deploy`.
