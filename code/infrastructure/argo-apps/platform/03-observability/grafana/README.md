# Grafana

`values.yaml` provisions Mimir, Loki, and Tempo datasources. The AAT dashboard comes from `manifests/aat.json` via Kustomize.

Run `task gateway` from `code/infrastructure`, then access Grafana at `http://grafana.localhost`. Direct access remains available at `http://192.168.56.10:30300`. Run `task password` for the admin password. Edit provisioned dashboards in Git.
