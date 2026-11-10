# syntax=docker/dockerfile:1
FROM golang:1.27-alpine AS build
# Which Halo the demo plays: a tag, a commit, or "main".
ARG HALO_VERSION=v0.1.0-beta.1
ENV CGO_ENABLED=0 GOFLAGS=-trimpath GOWORK=off
RUN go install github.com/three-agent-problem/halo/cmd/halo@${HALO_VERSION}
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
RUN go build -ldflags="-s -w" -o /out/halo-demo .

FROM alpine:3.22
RUN adduser -D -u 10001 demo && mkdir /data && chown demo /data
COPY --from=build /go/bin/halo /usr/local/bin/halo
COPY --from=build /out/halo-demo /usr/local/bin/halo-demo
USER demo
ENV HALO_DEMO_ADDR=:2222 HALO_DEMO_HOST_KEY=/data/host_ed25519
VOLUME /data
EXPOSE 2222
ENTRYPOINT ["/usr/local/bin/halo-demo"]
