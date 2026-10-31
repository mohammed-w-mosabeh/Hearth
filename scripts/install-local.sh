#!/usr/bin/env bash
# Installs the newest .deb from dist/ on this machine and restarts hearthd.
# Run it yourself: it asks for sudo.
set -euo pipefail

cd "$(dirname "$0")/.."
deb=$(ls -t dist/hearth_*_amd64.deb | head -n 1)
echo "Installing $deb"
sudo apt install "./$deb"
systemctl --user daemon-reload
systemctl --user restart hearthd
echo "Package: $(dpkg -s hearth | sed -n 's/^Version: //p')"
echo "hearthd: $(hearthd --version)"
