#!/bin/sh
# Docker mounts the (private) cgroup namespace read-only for non-privileged
# containers; systemd needs it writable. CAP_SYS_ADMIN allows the remount.
set -e
mount -o remount,rw /sys/fs/cgroup
exec /sbin/init
