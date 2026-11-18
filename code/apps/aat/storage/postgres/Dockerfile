FROM citusdata/citus:13.0.1@sha256:d2474464858e7b9120b906f5e4e8901a6fc1392cab0f79ae87bdfc0424fa49bb
RUN apt-get update \
    && apt-get install -y --no-install-recommends python3-venv ca-certificates \
    && python3 -m venv /opt/patroni \
    && /opt/patroni/bin/pip install --no-cache-dir 'patroni[kubernetes]==4.1.0' 'psycopg[binary]==3.2.10' \
    && rm -rf /var/lib/apt/lists/*
ENV PATH="/opt/patroni/bin:$PATH"
USER postgres
ENTRYPOINT ["patroni"]
CMD ["/etc/patroni/patroni.yaml"]
