# Platform apps

[`../root.yaml`](../root.yaml) recursively discovers `app.yaml` and numbered Application YAML files. It registers Argo CD, LGTM, both OpenTelemetry collectors, kube-state-metrics, and `dev-apps`.

Values and nested workload manifests are excluded by the filename filter. Rename inactive Applications to `.yaml.disabled`. Commit/push changes before `task deploy`; Argo CD reads Git.
