#!/bin/bash
set -e

if [ "$DIND_SUPPORT" == "gvisor" ]; then
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
    dev=$(ip route show default | sed 's/.*\sdev\s\(\S*\)\s.*$/\1/')
    addr=$(ip addr show dev "$dev"  | grep -w inet | sed 's/^\s*inet\s\(\S*\)\/.*$/\1/')

    iptables-legacy -t nat -A POSTROUTING -o "$dev" -j SNAT --to-source "$addr" -p tcp
    iptables-legacy -t nat -A POSTROUTING -o "$dev" -j SNAT --to-source "$addr" -p udp

    # 4. Start dockerd with flags to disable its own iptables management
    echo "Starting dockerd (gvisor mode)..."
    exec dockerd --iptables=false --ip6tables=false -D "$@"
else
    # Enable IP forwarding
    echo 1 > /proc/sys/net/ipv4/ip_forward

    # Start dockerd normally
    echo "Starting dockerd (privileged/normal mode)..."
    exec dockerd -D "$@"
fi
