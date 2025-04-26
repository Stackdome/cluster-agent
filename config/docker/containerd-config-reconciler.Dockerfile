FROM alpine:3.21.3

WORKDIR /


COPY containerd-config-reconciler /usr/local/bin/

# Create directory for mounting configs
RUN mkdir -p /config

# Expose port for health checks
EXPOSE 8080

USER 65532:65532

# Set the entrypoint
ENTRYPOINT ["/usr/local/bin/containerd-config-reconciler"]