{{- define "aat-citus.config" -}}
metadata-sync.py: |
{{ .Files.Get "files/citus-metadata-sync.py" | indent 2 }}
patroni.yaml: |
  scope: aat-citus
  kubernetes:
    use_endpoints: true
    labels: {app.kubernetes.io/name: aat-citus}
    scope_label: patroni-scope
    role_label: patroni-role
    leader_label_value: primary
    follower_label_value: replica
    ports: [{name: postgres, port: 5432}]
  restapi:
    listen: 0.0.0.0:8008
  bootstrap:
    dcs:
      ttl: 20
      loop_wait: 2
      retry_timeout: 5
      synchronous_mode: true
      synchronous_node_count: 1
      synchronous_mode_strict: false
      check_timeline: true
      postgresql:
        use_pg_rewind: true
        use_slots: true
        parameters:
          shared_buffers: 64MB
          work_mem: 2MB
          maintenance_work_mem: 32MB
          # Aggregator: 5 replicas x DB_MAX_CONNS 7 = 35, plus Patroni/Citus internals.
          # Applied at bootstrap; an existing cluster needs patronictl edit-config.
          max_connections: 50
          max_prepared_transactions: 80
          max_wal_senders: 12
          max_replication_slots: 12
          max_worker_processes: 24
          wal_level: logical
          wal_log_hints: 'on'
          synchronous_commit: 'on'
          statement_timeout: 2000
          idle_in_transaction_session_timeout: 5000
          citus.max_adaptive_executor_pool_size: 4
          citus.max_shared_pool_size: 24
          citus.node_connection_timeout: 1000
    initdb:
    - encoding: UTF8
    - data-checksums
  postgresql:
    listen: 0.0.0.0:5432
    data_dir: /var/lib/postgresql/data/pgdata
    pgpass: /tmp/pgpass
    basebackup:
    - checkpoint: fast
    pg_hba:
    - local all all trust
    - host replication replicator all scram-sha-256
    - host all all all scram-sha-256
    parameters:
      unix_socket_directories: /tmp
      # Local Patroni config applies when a process starts, including existing formations.
      # Bound failure of established Citus sockets, not just new connections.
      citus.node_conninfo: 'sslmode=require connect_timeout=2 tcp_user_timeout=5000 keepalives=1 keepalives_idle=5 keepalives_interval=2 keepalives_count=2'
  tags:
    nosync: false
members.json: |
  [
  {{- $comma := dict "used" false }}
  {{- range $group := .Values.citus.groups }}
  {{- if gt (int $group.id) 0 }}
  {{- range $node := $group.nodes }}
  {{- $unit := printf "citus-g%v-%s-0" $group.id $node }}
  {{- if $comma.used }},{{ end }}
  {"unit":{{ $unit | quote }},"group":{{ $group.id }},"node":{{ $node | quote }},"host":"{{ $unit }}.aat-citus","api":"http://{{ $unit }}.aat-citus:8008"}
  {{- $_ := set $comma "used" true }}
  {{- end }}
  {{- end }}
  {{- end }}
  ]
{{- end -}}
