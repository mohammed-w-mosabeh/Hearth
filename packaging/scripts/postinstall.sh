#!/bin/sh
# Debian maintainer script (dpkg runs it with /bin/sh); not part of hearthd.
set -e

if [ "$1" = "configure" ]; then
	# Start hearthd at login for every user.
	systemctl --global enable hearthd.service >/dev/null 2>&1 || true
	echo "Hearth installed. It starts at your next login, or now with:"
	echo "  systemctl --user start hearthd"
fi
