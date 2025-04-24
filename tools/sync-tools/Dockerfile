# Use the alpine image as the base
FROM alpine:latest

# Set the working directory to /app
WORKDIR /app

# Install wget and unzip
RUN apk add --no-cache wget unzip

# Download, unzip, and make the binary executable
RUN wget -O /tmp/linux-x64.zip https://github.com/clevert-app/clevert/releases/download/asset_rsync_3.3.0_9173891612/linux-x64.zip \
    && unzip /tmp/linux-x64.zip -d /app \
    && rm /tmp/linux-x64.zip \
    && chmod +x /app/rsync


RUN /bin/busybox --help
# Download and install the statically linked BusyBox binary
RUN wget -O /tmp/busybox https://busybox.net/downloads/binaries/1.31.0-i686-uclibc/busybox \
&& chmod +x /tmp/busybox \
&& mv /tmp/busybox /bin/busybox

