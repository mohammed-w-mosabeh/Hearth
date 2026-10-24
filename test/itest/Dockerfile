# Debian 13 with systemd as PID 1, for `make itest`. Never used on the host.
FROM debian:13@sha256:4c25275446ca371766b8b093c696782eabce06263fccacb8d5d813b3dc44b308

ENV container=docker DEBIAN_FRONTEND=noninteractive

RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      systemd systemd-sysv dbus dbus-user-session libpam-systemd \
      xdg-utils curl ca-certificates iproute2 procps \
 && rm -rf /var/lib/apt/lists/*

# Units that cannot work in a container.
RUN systemctl mask \
      systemd-firstboot.service systemd-udevd.service systemd-udev-trigger.service \
      getty.target console-getty.service systemd-remount-fs.service \
      dev-hugepages.mount sys-fs-fuse-connections.mount

# Unprivileged test user with lingering, so its user manager starts at boot.
RUN useradd --create-home --uid 1000 --shell /bin/bash tester \
 && mkdir -p /var/lib/systemd/linger \
 && touch /var/lib/systemd/linger/tester

COPY check.sh /usr/local/bin/hearth-itest-check
COPY entrypoint.sh /usr/local/bin/hearth-itest-init
RUN chmod 0755 /usr/local/bin/hearth-itest-check /usr/local/bin/hearth-itest-init

STOPSIGNAL SIGRTMIN+3
CMD ["/usr/local/bin/hearth-itest-init"]
