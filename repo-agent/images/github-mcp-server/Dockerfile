FROM ghcr.io/github/github-mcp-server as build

FROM debian:bookworm-slim

ENV DEBIAN_FRONTEND="noninteractive"

RUN echo 'Acquire::http::Timeout "60";' > /etc/apt/apt.conf.d/99timeout && \
    echo 'Acquire::https::Timeout "60";' >> /etc/apt/apt.conf.d/99timeout

# Install socat and necessary tools.
RUN for i in 1 2 3 4 5; do apt-get update && break || (echo "apt-get update failed, retrying in 10s... ($i/5)" && sleep 10); done && \
    apt-get install -y --no-install-recommends \
    socat \
    # Needed to find dynamic library dependencies.
    strace \
    # Clean up APT cache to reduce image size.
    && rm -rf /var/lib/apt/lists/*

# Set the working directory
WORKDIR /server
# Copy the binary from the build stage
COPY --from=build /server/github-mcp-server .


EXPOSE 8000

# Set the entrypoint to the server binary
#ENTRYPOINT ["/server/github-mcp-server"]
# Default arguments for ENTRYPOINT
#CMD ["stdio"]

ENTRYPOINT ["/usr/bin/socat"]
# socat TCP-LISTEN:12345,reuseaddr EXEC:'/path/to/your/server/program'
CMD ["TCP-LISTEN:8000,fork", "EXEC:/server/github-mcp-server stdio"]