#!/bin/sh
set -e

# "$1" is 0 on rpm erase and "remove"/"purge" on deb; upgrades must keep the
# service running.
case "$1" in
    0 | remove | purge)
        if command -v systemctl >/dev/null 2>&1; then
            systemctl stop cloud-route-manager >/dev/null 2>&1 || true
            systemctl disable cloud-route-manager >/dev/null 2>&1 || true
        fi
        ;;
esac
