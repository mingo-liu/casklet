#!/bin/sh
# Provision only a dedicated, disposable Ubuntu integration VM.
set -eu
fail() { printf 'prepare-integration-vm: %s\n' "$*" >&2; exit 1; }
[ "${CASKLET_DEDICATED_VM:-}" = 1 ] || fail 'set CASKLET_DEDICATED_VM=1 only in a dedicated disposable VM'
[ "$(uname -s)" = Linux ] || fail 'Linux is required'
[ -d /run/systemd/system ] || fail 'a running systemd system instance is required'
[ "$(id -u)" -ne 0 ] || fail 'run as the unprivileged test user with passwordless sudo'
command -v go >/dev/null 2>&1 || fail 'install Go before provisioning'
sudo -n true || fail 'passwordless sudo is required'
project=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
sudo env DEBIAN_FRONTEND=noninteractive apt-get update
sudo env DEBIAN_FRONTEND=noninteractive apt-get install -y busybox-static binutils make iproute2 iptables nftables util-linux conntrack apparmor-utils
# Hosted runners may have Docker's FORWARD policy set to DROP. An accept in
# casklet's nftables table cannot override a drop in another base chain.
# Permit only casklet bridge interfaces in the existing IPv4 forwarding chain, preserving
# the host policy and unrelated rules. Repeated provisioning is idempotent.
for direction in -i -o; do
    for interface in casklet0 csn+; do
        sudo iptables -w -C FORWARD "$direction" "$interface" -j ACCEPT 2>/dev/null ||
            sudo iptables -w -I FORWARD 1 "$direction" "$interface" -j ACCEPT
    done
done
sudo iptables -S FORWARD
sudo install -m 0644 "$project/dev/apparmor/casklet" /etc/apparmor.d/casklet
sudo apparmor_parser -r /etc/apparmor.d/casklet
# Rootless tests need a live user manager with all workload controllers.
user_unit="user@$(id -u).service"
sudo install -d -m 0755 "/run/systemd/system/$user_unit.d"
printf '[Service]\nDelegate=cpu memory pids\n' | sudo tee "/run/systemd/system/$user_unit.d/casklet.conf" >/dev/null
sudo systemctl daemon-reload
sudo loginctl enable-linger "$(id -un)"
sudo systemctl restart "$user_unit"
test -S "/run/user/$(id -u)/bus" || fail 'the systemd user bus is unavailable'
