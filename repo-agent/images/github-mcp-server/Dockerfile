FROM ghcr.io/github/github-mcp-server as build

FROM debian:bookworm-slim

# Install socat and necessary tools.
RUN apt-get update && \
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