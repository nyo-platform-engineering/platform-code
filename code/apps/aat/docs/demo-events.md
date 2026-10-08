# Problem 5

Run commands from `code/apps/aat`. Start the base stack; it includes PostgreSQL, both source mocks, Aggregator, NATS JetStream, dashboard updater, and field notifier.

```sh
docker compose -p aat-part1 up -d --build
docker compose -p aat-part1 ps
```

The Aggregator polls every three seconds by default. New mock records are generated every ten seconds. On first startup, the Aggregator imports seed records and writes an outbox row for each newly stored canonical hazard. The outbox relay publishes each row to `HAZARDS_STREAM` on `hazards.created.v1` and marks it published after JetStream confirms persistence.

Watch the two independent subscribers:

```sh
docker compose -p aat-part1 logs -f dashboard-updater field-notifier
```

Dashboard updater logs every hazard. Field notifier logs a simulated alert for `SIAGA` and `AWAS` hazards. Each consumer has a separate durable pull consumer and a separate processed-delivery KV bucket.

## Stop And Resume One Consumer

Stop only the dashboard updater:

```sh
docker compose -p aat-part1 stop dashboard-updater
```

During this period, new hazards continue to be stored and published. The field notifier continues receiving its own copy because it has a different durable consumer. Check its logs while the dashboard updater is stopped:

```sh
docker compose -p aat-part1 logs -f field-notifier
```

Restart the dashboard updater:

```sh
docker compose -p aat-part1 start dashboard-updater
docker compose -p aat-part1 logs -f dashboard-updater
```

Its durable consumer resumes at its last acknowledged message and receives the hazards published while it was stopped. The stream retains messages for seven days; a longer outage can exceed retention and lose older events. The KV dedupe bucket retains processed delivery keys for 30 days.

## Add A Third Subscriber

The producer publishes to one stable stream subject and contains no consumer names or consumer addresses. A new service can subscribe to the same subject with a new durable name:

```sh
docker compose -p aat-part1 -f compose.yaml -f compose.consumer.yaml up -d --no-deps test-consumer
```

The new subscriber uses `HAZARDS_STREAM`, `hazards.created.v1`, explicit acknowledgements, and its own durable KV idempotency bucket. Starting a durable consumer with a new name begins at the first retained stream message. No Aggregator code change is needed.

Delivery bersifat at-least-once. Kunci deduplikasi adalah hash `Nats-Msg-Id`
(event key versi outbox), bukan hazard ID saja, agar pembaruan warning/severity
tetap diterima. Handler log merupakan side effect demo; crash setelah handler
namun sebelum KV disimpan masih dapat menggandakan log. Side effect produksi
perlu transaksi/idempotency key sendiri. Header `X-Correlation-ID` diteruskan
dari poll/ingest melalui outbox dan broker ke log kedua consumer.
