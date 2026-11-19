#!/bin/sh
set -eu

data="$(mktemp -d /tmp/aat-citus-smoke.XXXXXX)"
trap 'pg_ctl -D "$data" -m fast -w stop >/dev/null 2>&1 || true' EXIT
postgres --version
patroni --version
initdb -D "$data" -U postgres -A trust --no-locale -E UTF8 >/dev/null
pg_ctl -D "$data" -l "$data/server.log" \
  -o "-c shared_preload_libraries=citus -c unix_socket_directories=/tmp -c listen_addresses=127.0.0.1" -w start
psql -h /tmp -U postgres -d postgres -v ON_ERROR_STOP=1 <<'SQL'
CREATE EXTENSION citus;
SELECT citus_version();
SELECT citus_set_coordinator_host('127.0.0.1', 5432);
SELECT citus_set_node_property('127.0.0.1', 5432, 'shouldhaveshards', true);
CREATE TABLE multiarch_smoke (id int PRIMARY KEY, value text);
SELECT create_distributed_table('multiarch_smoke', 'id');
INSERT INTO multiarch_smoke VALUES (1, 'arm64-and-amd64'), (2, 'citus-13');
DO $$ BEGIN
  IF (SELECT count(*) FROM multiarch_smoke) <> 2 THEN
    RAISE EXCEPTION 'distributed write/read failed';
  END IF;
END $$;
SQL
