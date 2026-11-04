# Debian 13 with systemd as PID 1, for `make itest`. Never used on the host.
# Build context: test/ (run.sh), so the e2e tests can be copied in.
FROM debian:13@sha256:4c25275446ca371766b8b093c696782eabce06263fccacb8d5d813b3dc44b308

ENV container=docker DEBIAN_FRONTEND=noninteractive

RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      systemd systemd-sysv dbus dbus-user-session libpam-systemd \
      xdg-utils curl ca-certificates iproute2 procps polkitd passwd man-db \
      chromium nodejs npm \
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

# Browser tests (test/e2e): Playwright pinned by its lockfile (integrity
# hashes), Debian's chromium instead of a Playwright browser download.
COPY e2e/package.json e2e/package-lock.json /opt/e2e/
RUN cd /opt/e2e && PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=1 npm ci --ignore-scripts --no-audit --no-fund
COPY e2e/playwright.config.ts e2e/global-setup.ts /opt/e2e/
COPY e2e/tests /opt/e2e/tests

# gio, the launcher's fallback (a layer of its own keeps the browser's cached).
RUN apt-get update \
 && apt-get install -y --no-install-recommends libglib2.0-bin \
 && rm -rf /var/lib/apt/lists/*

# needrestart, run for real by hearth-needrestart.service (its own layer).
RUN apt-get update \
 && apt-get install -y --no-install-recommends needrestart \
 && rm -rf /var/lib/apt/lists/*

# ufw, run for real by hearth-ufw.service (its own layer).
RUN apt-get update \
 && apt-get install -y --no-install-recommends ufw \
 && rm -rf /var/lib/apt/lists/*

# Lynis, run for real by hearth-lynis.service (its own layer).
RUN apt-get update \
 && apt-get install -y --no-install-recommends lynis \
 && rm -rf /var/lib/apt/lists/*

# unattended-upgrades, read through the real apt-config (its own layer).
RUN apt-get update \
 && apt-get install -y --no-install-recommends unattended-upgrades \
 && rm -rf /var/lib/apt/lists/*

# jq and sqlite3, for the backup checks (their own layer).
RUN apt-get update \
 && apt-get install -y --no-install-recommends jq sqlite3 \
 && rm -rf /var/lib/apt/lists/*

COPY itest/check.sh /usr/local/bin/hearth-itest-check
COPY itest/entrypoint.sh /usr/local/bin/hearth-itest-init
RUN chmod 0755 /usr/local/bin/hearth-itest-check /usr/local/bin/hearth-itest-init

STOPSIGNAL SIGRTMIN+3
CMD ["/usr/local/bin/hearth-itest-init"]
