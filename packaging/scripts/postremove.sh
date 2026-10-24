#!/bin/sh
# Debian maintainer script (dpkg runs it with /bin/sh); not part of hearthd.
set -e

# Per-user data (~/.config/hearth, ~/.local/share/hearth) belongs to each user
# and is left alone, per Debian policy. Nothing system-wide remains: the unit
# and desktop file are package files, and the --global enable symlink is
# removed in preremove.
if [ "$1" = "purge" ]; then
	rm -f /etc/systemd/user/default.target.wants/hearthd.service
fi
