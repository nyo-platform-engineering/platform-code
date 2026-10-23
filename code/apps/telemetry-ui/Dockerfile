FROM node:24-alpine AS web-build
WORKDIR /src
RUN npm install --global pnpm@12.6.0
COPY frontend/package.json frontend/pnpm-lock.yaml frontend/.npmrc ./
RUN pnpm install --frozen-lockfile
COPY frontend/ ./
RUN pnpm build

FROM golang:1.27.1-alpine AS go-build
RUN apk add --no-cache build-base
WORKDIR /src
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ ./
RUN CGO_ENABLED=1 go test ./... && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /telemetry-ui .

FROM alpine:3.22
RUN apk add --no-cache ca-certificates
COPY --from=go-build /telemetry-ui /telemetry-ui
COPY --from=web-build /src/dist /web
ENV PORT=8080 WEB_DIST_DIR=/web
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/telemetry-ui"]
