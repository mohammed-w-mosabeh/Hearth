#!/bin/sh
# Debian maintainer script (dpkg runs it with /bin/sh); not part of hearthd.
set -e

if [ "$1" = "remove" ] || [ "$1" = "purge" ]; then
	systemctl --global disable hearthd.service >/dev/null 2>&1 || true
	# Stop running per-user instances so no orphaned daemon keeps the port.
	for user in $(loginctl list-users --no-legend 2>/dev/null | awk '{print $2}'); do
		systemctl --user --machine="$user@.host" stop hearthd.service >/dev/null 2>&1 || true
	done
	if [ -d /run/systemd/system ]; then
		systemctl disable --now hearth-broker.socket >/dev/null 2>&1 || true
		systemctl disable --now hearth-pathcheck.socket >/dev/null 2>&1 || true
		systemctl stop hearth-broker.service >/dev/null 2>&1 || true
	fi
fi
