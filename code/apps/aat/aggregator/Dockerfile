FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY internal/httpkit ./internal/httpkit
COPY aggregator ./aggregator
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /service ./aggregator/cmd/server
FROM alpine:3.22
RUN adduser -D -u 10001 app
USER app
COPY --from=build /service /service
ENTRYPOINT ["/service"]
