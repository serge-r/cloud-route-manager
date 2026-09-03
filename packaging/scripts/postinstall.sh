#!/bin/sh
set -e

if command -v systemctl >/dev/null 2>&1; then
    systemctl daemon-reload >/dev/null 2>&1 || true
fi

cat <<'EOF'
cloud-route-manager installed to /opt/cloud-route-manager

  1. edit  /opt/cloud-route-manager/config.yml
  2. check /opt/cloud-route-manager/bin/cloud-route-manager \
             -config /opt/cloud-route-manager/config.yml -check-config
  3. start systemctl enable --now cloud-route-manager
EOF
