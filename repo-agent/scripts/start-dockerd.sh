#!/bin/bash
set -e

# 1. Mount tmpfs on /var/lib/docker if not already tmpfs
# gVisor only supports tmpfs as an upper layer for overlay.
if [ "$(stat -f -c %T /var/lib/docker 2>/dev/null)" != "tmpfs" ]; then
    echo "Mounting tmpfs on /var/lib/docker"
    mkdir -p /var/lib/docker
    mount -t tmpfs -o size=2G tmpfs /var/lib/docker
fi

# 2. Enable IP forwarding
echo 1 > /proc/sys/net/ipv4/ip_forward

# 3. Setup NAT rules
# Find default route interface and its IP address
DEV=$(ip route show default | sed 's/.*[[:space:]]dev[[:space:]]\([^[:space:]]*\)[[:space:]].*$/\1/')
if [ -n "$DEV" ]; then
    ADDR=$(ip addr show dev "$DEV" | grep -w inet | sed 's/^[[:space:]]*inet[[:space:]]\([^[:space:]]*\)\/.*$/\1/')
    if [ -n "$ADDR" ]; then
        IPTABLES_CMD="iptables"
        if command -v iptables-legacy >/dev/null 2>&1; then
            IPTABLES_CMD="iptables-legacy"
        fi
        echo "Setting up iptables NAT rules: dev=$DEV, addr=$ADDR, cmd=$IPTABLES_CMD"
        "$IPTABLES_CMD" -t nat -A POSTROUTING -o "$DEV" -j SNAT --to-source "$ADDR" -p tcp
        "$IPTABLES_CMD" -t nat -A POSTROUTING -o "$DEV" -j SNAT --to-source "$ADDR" -p udp
    fi
fi

# 4. Start dockerd with flags to disable its own iptables management
echo "Starting dockerd..."
exec dockerd --iptables=false --ip6tables=false -D "$@"
