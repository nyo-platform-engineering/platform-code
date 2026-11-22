"""Practice-run setup and durable Field Team receipt inspection for P5."""
from contextlib import contextmanager
import hashlib
import json
import socket
import urllib.parse
import uuid
import base64

from common import kubectl, output, parse_time, require

# Parent Applications do not suspend reconciliation of existing child Applications.
PRACTICE_APPS = ("platform", "dev-apps", "aat-dependencies-dev", "aat-auth-dev",
                 "aat-client-dev", "aat-bmkg-dev", "aat-pvmbg-dev",
                 "aat-dashboard-updater-dev", "aat-field-notifier-dev",
                 "aat-aggregator-dev", "aat-sensor-pantai-dev")


def wait_rollouts(run):
    """Observe existing rollouts only; never restart workloads to make setup pass."""
    client = run.cluster.kubectl("get", "deployment", "client", "-o", "json", json_output=True)
    replicas = client["spec"].get("replicas", 1)
    require(replicas >= 3,
            f"Client is configured for {replicas} replica(s), but P5 needs the deployed three-node Client setup; "
            "sync aat-client-dev with replica management enabled before starting P5")
    for name in dict.fromkeys((*run.config.deployments, "auth")):
        run.cluster.kubectl("rollout", "status", "deployment/" + name, "--timeout=180s", timeout=190)
    stores = run.cluster.kubectl("get", "statefulsets", "-o", "json", json_output=True)["items"]
    for store in stores:
        run.cluster.kubectl("rollout", "status", "statefulset/" + store["metadata"]["name"],
                            "--timeout=180s", timeout=190)


# Every deletion is source scoped. Do not reset owners/epochs or any NATS stream:
# a newly ingested valid reading has a new outbox delivery ID (ID + ingested_at).
RESET_STEPS = [
    ("timeouts", "SET LOCAL citus.propagate_set_commands = 'local'"),
    ("timeouts", "SET LOCAL statement_timeout = '90s'"),
    ("timeouts", "SET LOCAL lock_timeout = '5s'"),
    ("timeouts", "SET LOCAL idle_in_transaction_session_timeout = '90s'"),
    ("other_sources_before", "SELECT source, count(*) FROM hazard_events WHERE source <> 'SENSOR_PANTAI' GROUP BY source ORDER BY source"),
    ("ids", "CREATE TEMP TABLE p5_sensor_ids ON COMMIT DROP AS SELECT id FROM hazard_events WHERE source = 'SENSOR_PANTAI'"),
    ("ids", "ANALYZE p5_sensor_ids"),
    ("pending_outboxes_deleted", "DELETE FROM hazard_outboxes WHERE payload->>'source' = 'SENSOR_PANTAI'"),
    ("outboxes_deleted", """DELETE FROM hazard_outboxes o USING p5_sensor_ids h
        WHERE regexp_replace(o.event_key, '-[0-9]{4}-[0-9]{2}-[0-9]{2}T.*$', '') = h.id"""),
    ("hazard_events_deleted", "DELETE FROM hazard_events WHERE source = 'SENSOR_PANTAI'"),
    ("inbox_messages_deleted", "DELETE FROM inbox_messages WHERE source = 'SENSOR_PANTAI'"),
    ("sensor_states_deleted", "DELETE FROM sensor_states WHERE source = 'SENSOR_PANTAI'"),
    ("poll_states_deleted", "DELETE FROM poll_states WHERE source = 'SENSOR_PANTAI'"),
    ("other_sources_after", "SELECT source, count(*) FROM hazard_events WHERE source <> 'SENSOR_PANTAI' GROUP BY source ORDER BY source"),
    # ISO 8601 in UTC: Python < 3.11 cannot parse PostgreSQL's text form ("+00").
    ("reset_at", """SELECT to_char(clock_timestamp() AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"+00:00"')"""),
]

# Use individual commands inside one transaction. Citus does not support wrapping
# the distributed DELETE in a data-modifying CTE just to count affected rows.
RESET_PROGRAM = r'''import json,os,sys,psycopg
result = {}
step = "connect"
try:
 with psycopg.connect(host="/tmp", user=os.environ["PATRONI_SUPERUSER_USERNAME"],
                      dbname=os.environ["PATRONI_CITUS_DATABASE"]) as conn:
  with conn.cursor() as cur:
   for step, statement in json.loads(sys.argv[1]):
    cur.execute(statement)
    if cur.description:
     result[step] = cur.fetchall()
    elif step.endswith("_deleted"):
     result[step] = cur.rowcount
   if result["other_sources_before"] != result["other_sources_after"]:
    raise ValueError("another source changed during P5 reset")
   result["other_sources_preserved"] = True
   result["reset_at"] = result["reset_at"][0][0]
except Exception as error:
 print("P5 reset failed at " + step + ": " + str(error)[:180], file=sys.stderr)
 sys.exit(1)
print(json.dumps(result))
'''


def reset_database(run):
    text = run.cluster.kubectl('exec', coordinator(run), '--', 'python3', '-c',
                              RESET_PROGRAM, json.dumps(RESET_STEPS), timeout=150)
    return json.loads(text)


def coordinator(run):
    pods = run.cluster.kubectl('get', 'pods', '-l',
        'app.kubernetes.io/name=aat-citus,citus-group=0,patroni-role=primary',
        '-o', 'json', json_output=True)['items']
    require(len(pods) == 1, 'need exactly one primary Citus coordinator')
    return pods[0]['metadata']['name']


def sql(run, statement):
    # Credentials stay in the coordinator environment; local socket auth is trust.
    script = '''import os,subprocess,sys
p = subprocess.run(["psql", "-XqAt", "-v", "ON_ERROR_STOP=1", "-h", "/tmp",
 "-U", os.environ["PATRONI_SUPERUSER_USERNAME"],
 "-d", os.environ["PATRONI_CITUS_DATABASE"], "-c", sys.argv[1]], capture_output=True, text=True)
if p.returncode:
 print("P5 SQL failed: " + p.stderr[-300:], file=sys.stderr)
 sys.exit(p.returncode)
print(p.stdout, end="")
'''
    text = run.cluster.kubectl('exec', coordinator(run), '--', 'python3', '-c', script, statement, timeout=150)
    return [json.loads(line) for line in text.splitlines() if line.strip()]


def argo(run, *args, **kwargs):
    return kubectl(run.config, *args, namespace='argocd', **kwargs)


def scale(run, name, count):
    run.cluster.kubectl('scale', 'deployment/' + name, f'--replicas={count}')
    if count:
        run.cluster.kubectl('rollout', 'status', 'deployment/' + name, '--timeout=180s', timeout=200)
    else:
        # Wait for actual termination, not just a Deployment status update.
        run.wait(name + ' pods to terminate', lambda: not run.cluster.kubectl(
            'get', 'pods', '-l', f'app.kubernetes.io/instance={name}', '-o', 'json',
            json_output=True)['items'], 180)


@contextmanager
def practice_run(run):
    """Hold reconciliation for the suite and restore replicas/policies on all exits."""
    capability = run.aggregator("/internal/satellite/capabilities")
    require(not capability.get("verification_schema"),
            "P4 controlled verification is active; finish/restore P4 before starting P5")
    config = run.cluster.kubectl('get', 'configmap', 'sensor-pantai', '-o', 'json', json_output=True)['data']
    require(config.get('PANTAI_PROFILE') == 'practice' and config.get('PANTAI_SEED') == '42'
            and config.get('PANTAI_SPEED') == '60',
            'P5 reset expects the practice scenario (seed 42, speed 60); never reset the exam game day')
    deployments = {}
    for name in ('aggregator', 'sensor-pantai'):
        dep = run.cluster.kubectl('get', 'deployment', name, '-o', 'json', json_output=True)
        deployments[name] = dep['spec'].get('replicas', 1)
        require(deployments[name] > 0, f'{name} must be running before P5')
    hpas = run.cluster.kubectl('get', 'hpa', '-o', 'json', json_output=True)['items']
    require(not any(h['spec']['scaleTargetRef'].get('name') in deployments for h in hpas),
            'disable Aggregator/Sensor Pantai autoscaling before the P5 practice reset')
    run.cluster.kubectl('rollout', 'status', 'deployment/field-notifier', '--timeout=180s', timeout=200)
    apps, errors = [], []
    failure = None
    try:
        for name in PRACTICE_APPS:
            app = argo(run, 'get', 'application', name, '-o', 'json', json_output=True)
            baseline = {'name': name, 'automated': app.get('spec', {}).get('syncPolicy', {}).get('automated'),
                        'skip': app['metadata'].get('annotations', {}).get('argocd.argoproj.io/skip-reconcile')}
            apps.append(baseline)
            run.save('setup-baseline.json', {'replicas': deployments, 'applications': apps})
            argo(run, 'patch', 'application', name, '--type=merge', '-p',
                 json.dumps({'spec': {'syncPolicy': {'automated': None}}}))
            def idle(name=name):
                current = argo(run, 'get', 'application', name, '-o', 'json', json_output=True)
                return not current.get('operation') and current.get('status', {}).get(
                    'operationState', {}).get('phase') not in ('Running', 'Terminating')
            run.wait('Argo sync to finish: ' + name, idle, 180)
            argo(run, 'annotate', 'application', name, 'argocd.argoproj.io/skip-reconcile=true', '--overwrite')
        output('WAIT', 'Practice setup', 'Waiting for application and storage rollouts before resetting sensor data')
        wait_rollouts(run)
        endpoint = run.url('client')
        require('client' not in run.forwards.processes,
                'P5 requires the public Client endpoint before resetting data or measuring availability')
        run.save('setup-ready.json', {'applications_held': [a['name'] for a in apps],
                                     'rollouts_complete': True, 'client_endpoint': endpoint})
        yield deployments
    except BaseException as error:
        failure = error
        raise
    finally:
        # Restore workloads before enabling reconciliation, even after setup failure.
        for name, count in deployments.items():
            try:
                scale(run, name, count)
            except Exception as error:
                errors.append(f'{name}: {error}')
        for app in reversed(apps):
            try:
                argo(run, 'patch', 'application', app['name'], '--type=merge', '-p', json.dumps({
                    'metadata': {'annotations': {'argocd.argoproj.io/skip-reconcile': app['skip']}},
                    'spec': {'syncPolicy': {'automated': app['automated']}}}))
            except Exception as error:
                errors.append(f"{app['name']}: {error}")
        run.save('cleanup.json', {'restored': not errors, 'errors': errors,
                                  'scenario_error': run.clean(str(failure)) if failure else None})
        if errors and failure is not None:
            output('WARN', 'P5 cleanup incomplete', *errors)
        else:
            require(not errors, 'P5 cleanup incomplete: ' + '; '.join(errors))


def reset_sensor(run, replicas):
    output('WAIT', 'Practice reset', 'Stopping Sensor Pantai and Aggregator before clearing source state')
    scale(run, 'sensor-pantai', 0)
    scale(run, 'aggregator', 0)
    output('WAIT', 'Practice reset', 'Writers stopped; clearing only SENSOR_PANTAI state in one transaction')
    baseline = reset_database(run)
    run.sensor_reset_at = baseline['reset_at']
    run.save('sensor-reset.json', baseline)
    # Start processing before the sensor's 30/90/150-second critical readings.
    scale(run, 'aggregator', replicas['aggregator'])
    run.forwards.close('aggregator')
    scale(run, 'sensor-pantai', replicas['sensor-pantai'])
    output('RESULT', 'Sensor Pantai reset',
           f"Deleted {baseline['hazard_events_deleted']} sensor events and reset its inbox/outbox/cursor.",
           'Other sources and notification history preserved; new deliveries use new IDs.')
    return baseline


def critical_deliveries(run):
    # Published outboxes deliberately erase their payload. Join the durable key
    # back to the stored critical event, including its exact ingestion version.
    hazards, outboxes = sql(run, """SELECT coalesce(json_agg(json_build_object(
      'hazard_id', id, 'ingested_at', ingested_at)), '[]'::json)
      FROM hazard_events WHERE source = 'SENSOR_PANTAI' AND attributes->>'critical' = 'true';
      SELECT coalesce(json_agg(event_key), '[]'::json) FROM hazard_outboxes WHERE lane = 'critical';""")
    deliveries = []
    for h in hazards:
        prefix = h['hazard_id'] + '-'
        keys = [k for k in outboxes if k.startswith(prefix)
                and parse_time(k[len(prefix):]) == parse_time(h['ingested_at'])]
        require(len(keys) <= 1, 'critical event has multiple outbox deliveries')
        if keys:
            deliveries.append(dict(h, event_key=keys[0]))
    return deliveries


def nats_request(address, subject, payload):
    """One read-only JetStream request using NATS framing; no external CLI needed."""
    inbox = '_INBOX.p5.' + uuid.uuid4().hex
    body = json.dumps(payload).encode()
    with socket.create_connection(address, timeout=5) as sock:
        sock.settimeout(5)
        with sock.makefile('rb') as stream:
            require(stream.readline(8192).startswith(b'INFO '), 'NATS handshake missing')
            sock.sendall(b'CONNECT {"verbose":false,"pedantic":false}\r\n' +
                         f'SUB {inbox} 1\r\nUNSUB 1 1\r\nPUB {subject} {inbox} {len(body)}\r\n'.encode() + body + b'\r\n')
            for _ in range(20):
                line = stream.readline(8192)
                if line == b'PING\r\n':
                    sock.sendall(b'PONG\r\n')
                elif line.startswith(b'MSG '):
                    size = int(line.split()[-1])
                    require(0 < size <= 8 << 20, 'invalid NATS response size')
                    raw = stream.read(size)
                    require(len(raw) == size and stream.read(2) == b'\r\n', 'truncated NATS response')
                    return json.loads(raw)
                else:
                    require(line and not line.startswith((b'-ERR', b'HMSG')), 'NATS request rejected')
    raise ValueError('NATS response missing')


def receipt(run, delivery):
    # publisher.go gives the critical subject its own JetStream message ID.
    key = hashlib.sha256((delivery['event_key'] + ':critical').encode()).hexdigest()
    address = urllib.parse.urlsplit(run.url('nats'))
    response = nats_request((address.hostname, address.port),
        '$JS.API.STREAM.MSG.GET.KV_FIELD_NOTIFIER_CRITICAL_EFFECTS',
        {'last_by_subj': '$KV.FIELD_NOTIFIER_CRITICAL_EFFECTS.' + key})
    if response.get('error', {}).get('err_code') in (10037, 10059):
        return None  # no receipt yet / bucket has not been created yet
    require('error' not in response, 'cannot inspect durable Field Team receipt')
    msg = response['message']
    effect = json.loads(base64.b64decode(msg['data']))
    require(effect['delivery_id'] == key and effect['kind'] == 'critical_field_alert'
            and effect['hazard']['source'] == 'SENSOR_PANTAI'
            and effect['hazard']['hazard_id'] == delivery['hazard_id']
            and parse_time(effect['hazard']['ingested_at']) == parse_time(delivery['ingested_at']),
            'durable notification does not match this sensor delivery')
    return {'delivery_id': key, 'hazard_id': delivery['hazard_id'],
            'ingested_at': delivery['ingested_at'], 'received_at': msg['time']}
