FROM alpine:3.20


WORKDIR /app

# Copy the pre-built binary from your host
COPY containerd-config-reconciler .

# Create directory for mounting configs
RUN mkdir -p /config

# Expose port for health checks
EXPOSE 8080

# Set the entrypoint
ENTRYPOINT ["/app/containerd-config-reconciler"]