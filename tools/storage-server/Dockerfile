FROM ubuntu:latest

# Install OpenSSH server and inotify-tools
RUN apt-get update && \
    apt-get install -y openssh-server inotify-tools

# Generate SSH host keys
RUN ssh-keygen -A

# Configure SSH for non-root user
RUN sed -i 's/#PermitRootLogin prohibit-password/PermitRootLogin no/' /etc/ssh/sshd_config && \
    sed -i 's/#PasswordAuthentication yes/PasswordAuthentication no/' /etc/ssh/sshd_config

# Create a non-root user and group
RUN groupadd -r stackdomeuser && useradd -r -g stackdomeuser -m -d /home/stackdomeuser -s /bin/bash stackdomeuser

# Change ownership of SSH host keys to stackdomeuser
RUN chown -R stackdomeuser:stackdomeuser /etc/ssh/*_key /etc/ssh/*_key.pub

# Set up SSH directory and adjust permissions
RUN mkdir -p /home/stackdomeuser/.ssh && \
    chown stackdomeuser:stackdomeuser /home/stackdomeuser/.ssh && \
    chmod 700 /home/stackdomeuser/.ssh

# Create the sshd directory if it doesn't exist
RUN mkdir -p /var/run/sshd && \
    chown -R stackdomeuser:stackdomeuser /var/run/sshd

# Expose SSH port
EXPOSE 22

# Add a script to switch between root and non-root user
RUN echo '#!/bin/bash\n\
if [ "$(id -u)" -ne 0 ]; then\n\
    exec sudo -u root /bin/bash\n\
else\n\
    exec su -l stackdomeuser\n\
fi' > /usr/local/bin/switch_user && \
    chmod +x /usr/local/bin/switch_user

# Install sudo and configure it for stackdomeuser
RUN apt-get install -y sudo && \
    echo "stackdomeuser ALL=(ALL) NOPASSWD: ALL" >> /etc/sudoers

# Switch to non-root user
USER stackdomeuser

# Start the SSH service
CMD ["/usr/sbin/sshd", "-D", "-e", "-p", "2222"]