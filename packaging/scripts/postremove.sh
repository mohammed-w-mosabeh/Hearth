#!/bin/sh
# Debian maintainer script (dpkg runs it with /bin/sh); not part of hearthd.
set -e

if [ -d /run/systemd/system ]; then
	systemctl daemon-reload >/dev/null 2>&1 || true
fi

# Per-user data (~/.config/hearth, ~/.local/share/hearth) belongs to each user
# and is left alone, per Debian policy. Everything system-wide goes on purge:
# the --global enable symlink, the broker's state (its audit log) and the
# hearth group if nobody is in it any more.
if [ "$1" = "purge" ]; then
	rm -f /etc/systemd/user/default.target.wants/hearthd.service
	rm -f /etc/systemd/system/sockets.target.wants/hearth-broker.socket
	rm -rf /var/lib/hearth /run/hearth
	# A Lynis schedule set through Hearth (SECURITY S16h).
	rm -f /etc/systemd/system/hearth-lynis.timer /etc/systemd/system/timers.target.wants/hearth-lynis.timer
	# Limits set through Hearth (SECURITY S16d). Other drop-ins stay.
	for f in /etc/systemd/system/*.service.d/hearth.conf; do
		[ -e "$f" ] || continue
		rm -f "$f"
		rmdir "${f%/*}" 2>/dev/null || true
	done
	if [ -d /run/systemd/system ]; then
		systemctl daemon-reload >/dev/null 2>&1 || true
	fi
	members=$(getent group hearth | cut -d: -f4)
	if getent group hearth >/dev/null && [ -z "$members" ]; then
		groupdel hearth >/dev/null 2>&1 || true
	fi
fi
