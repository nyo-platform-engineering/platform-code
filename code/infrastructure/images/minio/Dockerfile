FROM alpine:3.23 AS base
RUN apk add --no-cache ca-certificates

FROM base AS minio
COPY minio /usr/local/bin/minio
ENTRYPOINT ["minio"]

FROM base AS mc
COPY mc /usr/local/bin/mc
ENTRYPOINT ["mc"]
