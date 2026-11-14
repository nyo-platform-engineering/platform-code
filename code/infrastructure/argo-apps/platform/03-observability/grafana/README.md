# Grafana

`values.yaml` provisions Mimir, Loki, and Tempo datasources. The AAT dashboard comes from `manifests/aat.json` via Kustomize.

Access Grafana at `http://192.168.56.10:30300`; run `task password` from `code/infrastructure` for the admin password. Edit provisioned dashboards in Git.
