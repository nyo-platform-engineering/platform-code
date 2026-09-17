FROM golang:1.27.1-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY *.go ./
RUN go test ./... && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /go-demo .

FROM scratch
COPY --from=build /go-demo /go-demo
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/go-demo"]
