#!/bin/sh
# Debian maintainer script (dpkg runs it with /bin/sh); not part of hearthd.
set -e

if [ "$1" = "configure" ]; then
	# Members of this group may use hearth-broker (SECURITY S11). Nobody is
	# added automatically: joining it is the administrator's decision.
	if ! getent group hearth >/dev/null; then
		groupadd --system hearth
	fi
	if [ -d /run/systemd/system ]; then
		systemctl daemon-reload >/dev/null 2>&1 || true
		systemctl enable --now hearth-broker.socket >/dev/null 2>&1 || true
		systemctl enable --now hearth-pathcheck.socket >/dev/null 2>&1 || true
		systemctl enable --now hearth-removed-clean.timer >/dev/null 2>&1 || true
	fi
	# Start hearthd at login for every user.
	systemctl --global enable hearthd.service >/dev/null 2>&1 || true
	echo "Hearth installed. It starts at your next login, or now with:"
	echo "  systemctl --user start hearthd"
	echo "For Docker and other system changes, add yourself to the hearth group"
	echo "and log in again:"
	echo "  sudo usermod -aG hearth \$USER"
fi
