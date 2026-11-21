"""Reconcile Citus worker metadata independently of application startup DDL."""
import json
import os
import time

import psycopg


def reconcile(conn):
    with conn.transaction():
        conn.execute("SET LOCAL statement_timeout = '15s'")
        conn.execute("SET LOCAL lock_timeout = '1s'")
        if conn.execute("SELECT pg_is_in_recovery()").fetchone()[0]:
            return
        if not conn.execute("SELECT pg_try_advisory_xact_lock(403105)").fetchone()[0]:
            return
        pending = conn.execute("""SELECT nodename, nodeport FROM pg_dist_node
            WHERE groupid > 0 AND noderole = 'primary' AND isactive
              AND hasmetadata AND NOT metadatasynced ORDER BY nodeid""").fetchall()
        if not pending:
            return
        # Use the supported sync API; never mark unsynchronized metadata as valid.
        conn.execute("SELECT start_metadata_sync_to_all_nodes()")
    print(json.dumps({"message": "Citus metadata synchronized", "nodes": pending}), flush=True)


def main():
    while True:
        try:
            with psycopg.connect(host="/tmp", user=os.environ["PATRONI_SUPERUSER_USERNAME"],
                                 dbname=os.environ["PATRONI_CITUS_DATABASE"], connect_timeout=2,
                                 application_name="citus-metadata-reconciler", autocommit=True) as conn:
                reconcile(conn)
        except psycopg.Error as error:
            # SQLSTATE is sufficient for diagnosis and does not expose credentials.
            print(json.dumps({"message": "Citus metadata sync retry", "sqlstate": error.sqlstate}), flush=True)
        time.sleep(5)


if __name__ == "__main__":
    main()
