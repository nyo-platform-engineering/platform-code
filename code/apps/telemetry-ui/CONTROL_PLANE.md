# PostgreSQL control plane

PostgreSQL is the application's control plane. It owns login sessions,
organization access, permissions, and the mapping from an organization to the
ClickHouse datasource used for each telemetry signal.

## Authorization tables

- `telemetry_organizations`: organization ID, display name, and immutable
  `telemetry_scope`. Queries bind this scope to
  `ResourceAttributes['tenant.id']`; clients cannot choose it.
- `telemetry_access_grants`: verified Google/GitHub subject, Google email, or
  Google domain mapped to one organization.
- `telemetry_access_grant_permissions`: permissions attached to a grant.
- `telemetry_sessions` and `telemetry_login_attempts`: rotating browser sessions
  and one-use OAuth state.

Helm's `organizations` list is bootstrap data for organization metadata, identity
mappings, permissions, and datasource assignments. The chart mounts it as
`organizations.json`. Non-Helm development can use the smaller
`auth.grants.json` format with explicit `organizationId` fields. Grant rows marked `managed_by=config` are reconciled at
startup. Rows marked `managed_by=database` are preserved and take precedence over
a bootstrap selector.

## Datasource tables

- `telemetry_data_sources`: ClickHouse address, database, username, TLS flag, and
  the name of a password environment variable.
- `telemetry_organization_data_sources`: zero or more datasource assignments for
  each `(organization_id, signal)` pair, where signal is `traces` or `logs`.

Passwords are not stored in PostgreSQL. `password_env` must start with
`CLICKHOUSE_`; Helm should populate that environment variable with
`valueFrom.secretKeyRef`. Changing a database-managed assignment takes effect on
the next query. Connection pools are reused for identical configurations.
The browser selects exactly one assigned datasource independently for traces and
logs. The backend verifies the selected ID against the active organization and
signal. A missing selection is accepted only when exactly one assignment exists;
queries are never fanned out or silently failed over.

## Admin inventory

Users granted `observability:admin:read` see an **Admin** sidebar entry. Its
read-only summary is restricted to the user's active organization and shows
organization scope, access-grant count, datasource metadata, and signal
assignments. Selector identities, password values, and password environment
references are not returned to the browser. Mutations remain managed through
database migrations or GitOps.

Helm groups reusable datasource definitions under `unified`, `traces`, and `logs`.
Each organization selects datasource IDs using the same groups, then startup
bootstraps `managed_by=config` rows. Unified selections expand to both signals.
Database-managed `(organization_id, signal)` assignment sets override bootstrap
assignment sets. The backend retains direct
`CLICKHOUSE_*`, `CLICKHOUSE_TRACES_*`, and `CLICKHOUSE_LOGS_*` compatibility for
non-Helm development.

Example database-managed log datasource (the referenced environment variable is
injected through Helm `extraEnv`):

```sql
INSERT INTO telemetry_data_sources
  (id, address, database, username, password_env, secure, managed_by)
VALUES
  ('acme-logs', 'logs.acme.internal:9440', 'otel', 'reader',
   'CLICKHOUSE_DATASOURCE_ACME_LOGS_PASSWORD', true, 'database')
ON CONFLICT (id) DO UPDATE SET
  address = EXCLUDED.address,
  database = EXCLUDED.database,
  username = EXCLUDED.username,
  password_env = EXCLUDED.password_env,
  secure = EXCLUDED.secure;

INSERT INTO telemetry_organization_data_sources
  (organization_id, signal, data_source_id, managed_by)
VALUES ('acme', 'logs', 'acme-logs', 'database')
ON CONFLICT (organization_id, signal, data_source_id) DO UPDATE SET
  managed_by = 'database';
```

## Request boundary

For every protected request the backend:

1. validates and optionally rotates the PostgreSQL session;
2. resolves the matching grant, organization, and permissions from PostgreSQL;
3. validates and selects one assigned trace or log datasource;
4. binds the organization's telemetry scope into the ClickHouse query.

Failure at any boundary fails closed. A missing membership returns 401/403, a
missing datasource or secret returns 503, and no request header can override the
organization or datasource.
