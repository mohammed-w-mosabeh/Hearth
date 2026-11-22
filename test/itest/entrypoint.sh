#!/bin/sh
# Docker mounts the (private) cgroup namespace read-only for non-privileged
# containers; systemd needs it writable. CAP_SYS_ADMIN allows the remount.
set -e
mount -o remount,rw /sys/fs/cgroup
# A real host has a device node here, Docker a link to pts/ptmx. The link
# hides that PrivateDevices=yes leaves a user service a ptmx it cannot open.
rm /dev/ptmx
mknod -m 0666 /dev/ptmx c 5 2
chmod 0666 /dev/ptmx
exec /sbin/init
