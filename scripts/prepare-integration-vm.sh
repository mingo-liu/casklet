#!/bin/sh
# Provision only a dedicated, disposable Ubuntu integration VM.
set -eu
fail() { printf 'prepare-integration-vm: %s\n' "$*" >&2; exit 1; }
[ "${MINI_DOCKER_DEDICATED_VM:-}" = 1 ] || fail 'set MINI_DOCKER_DEDICATED_VM=1 only in a dedicated disposable VM'
[ "$(uname -s)" = Linux ] || fail 'Linux is required'
[ -d /run/systemd/system ] || fail 'a running systemd system instance is required'
[ "$(id -u)" -ne 0 ] || fail 'run as the unprivileged test user with passwordless sudo'
command -v go >/dev/null 2>&1 || fail 'install Go before provisioning'
sudo -n true || fail 'passwordless sudo is required'
project=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
sudo env DEBIAN_FRONTEND=noninteractive apt-get update
sudo env DEBIAN_FRONTEND=noninteractive apt-get install -y busybox-static binutils make iproute2 nftables util-linux conntrack apparmor-utils
sudo install -m 0644 "$project/dev/apparmor/mini-docker" /etc/apparmor.d/mini-docker
sudo apparmor_parser -r /etc/apparmor.d/mini-docker
# Rootless tests need a live user manager with all workload controllers.
user_unit="user@$(id -u).service"
sudo install -d -m 0755 "/run/systemd/system/$user_unit.d"
printf '[Service]\nDelegate=cpu memory pids\n' | sudo tee "/run/systemd/system/$user_unit.d/mini-docker.conf" >/dev/null
sudo systemctl daemon-reload
sudo loginctl enable-linger "$(id -un)"
sudo systemctl restart "$user_unit"
test -S "/run/user/$(id -u)/bus" || fail 'the systemd user bus is unavailable'
