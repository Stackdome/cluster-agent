FROM alpine:latest

RUN apk add --no-cache ca-certificates

WORKDIR /app/

COPY ./bin/stackdome-operator-manager .

USER 65532:65532

ENTRYPOINT ["./stackdome-operator-manager"]
