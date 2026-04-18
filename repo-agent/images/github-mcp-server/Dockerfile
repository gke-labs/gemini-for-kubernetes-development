FROM ghcr.io/github/github-mcp-server as build

FROM debian:bookworm-slim

# Switch to a more reliable mirror
RUN find /etc/apt/ -type f -name "*.list" -o -name "*.sources" | xargs sed -i 's/archive.ubuntu.com/mirrors.edge.kernel.org/g' || true
RUN find /etc/apt/ -type f -name "*.list" -o -name "*.sources" | xargs sed -i 's/security.ubuntu.com/mirrors.edge.kernel.org/g' || true
RUN find /etc/apt/ -type f -name "*.list" -o -name "*.sources" | xargs sed -i 's/deb.debian.org/mirrors.edge.kernel.org/g' || true

# Add apt retries and timeouts
RUN echo 'Acquire::Retries "10"; Acquire::http::Timeout "60"; Acquire::https::Timeout "60";' > /etc/apt/apt.conf.d/80-retries

# Install socat and necessary tools.
RUN (apt-get update || (sleep 10 && apt-get update) || (sleep 20 && apt-get update)) && \
    (apt-get install -y --no-install-recommends socat strace || (sleep 10 && apt-get install -y --no-install-recommends socat strace)) && \
    rm -rf /var/lib/apt/lists/*

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