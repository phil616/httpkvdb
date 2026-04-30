FROM golang:1.26.2-alpine AS build

WORKDIR /src

COPY go.mod ./
RUN go mod download

COPY . .
RUN go test ./...
RUN go build -trimpath -ldflags='-s -w' -o /out/kvhttpd ./cmd/kvhttpd

FROM alpine:3.20

RUN adduser -D -H -s /sbin/nologin kvhttpd \
	&& mkdir -p /data \
	&& chown -R kvhttpd:kvhttpd /data

COPY --from=build /out/kvhttpd /usr/local/bin/kvhttpd

USER kvhttpd
EXPOSE 8080

ENTRYPOINT ["/usr/local/bin/kvhttpd"]
