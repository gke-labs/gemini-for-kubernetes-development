#!/bin/bash
set -e

# Enable IP forwarding
echo 1 > /proc/sys/net/ipv4/ip_forward

# Start dockerd
echo "Starting dockerd..."
exec dockerd -D "$@"

