FROM alpine:3.21.3

RUN apk add --no-cache ca-certificates

WORKDIR /

COPY cluster-agent-manager /usr/local/bin/

USER 65532:65532

ENTRYPOINT ["/usr/local/bin/cluster-agent-manager"]
