FROM --platform=$BUILDPLATFORM golang:1.25.0-alpine3.22@sha256:f18a072054848d87a8077455f0ac8a25886f2397f88bfdd222d6fafbb5bba440 AS build

WORKDIR /source

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG TARGETOS
ARG TARGETARCH
ARG COMMAND
ARG VERSION
ARG COMMIT
ARG BUILD_DATE

RUN case "$COMMAND" in \
      cluster-agent-manager) package=./cmd/cluster-agent/cluster-agent-manager.go ;; \
      containerd-config-reconciler) package=./cmd/containerd-config-reconciler/containerd-config-reconciler.go ;; \
      *) echo "unsupported release command: $COMMAND" >&2; exit 1 ;; \
    esac && \
    CGO_ENABLED=0 GOOS="$TARGETOS" GOARCH="$TARGETARCH" \
      go build -trimpath \
      -ldflags="-s -w -X stackdome.io/cluster-agent/internal/version.Version=$VERSION -X stackdome.io/cluster-agent/internal/version.Commit=$COMMIT -X stackdome.io/cluster-agent/internal/version.BuildDate=$BUILD_DATE" \
      -o "/out/$COMMAND" "$package"

FROM alpine:3.21.3@sha256:a8560b36e8b8210634f77d9f7f9efd7ffa463e380b75e2e74aff4511df3ef88c

ARG COMMAND

COPY --from=build "/out/$COMMAND" /usr/local/bin/release-command

USER 65532:65532

ENTRYPOINT ["/usr/local/bin/release-command"]
