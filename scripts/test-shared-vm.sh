#!/bin/sh
# The privileged suite needs exclusive use of the VM's engine and networking.
# Preserve daily-use data and the installed restart manager across the test run.
set -eu
fail() { printf 'test-shared-vm: %s\n' "$*" >&2; exit 1; }
[ "$(uname -s)" = Linux ] || fail 'Linux is required'
[ "$(id -u)" = 0 ] || fail 'root privileges are required'
[ "$#" -gt 0 ] || fail 'a test command is required'
transaction=/var/lib/casklet-integration
store=/var/lib/casklet
service=/etc/systemd/system/casklet-restarts.service
enable=/etc/systemd/system/multi-user.target.wants/casklet-restarts.service
exec 9>/var/lib/.casklet-integration.lock
flock -n 9 || fail 'another integration run is in progress'
[ ! -e "$transaction" ] && [ ! -L "$transaction" ] ||
    fail "interrupted run found at $transaction; restore its store and restart-service before retrying"

# These tests deliberately inject conflicting bridges and alter forwarding.
# Never stop a daily-use workload automatically to make room for them.
units=$(systemctl list-units --no-legend --plain --state=active,activating,deactivating 'casklet-*' |
    awk '$1 != "casklet-restarts.service" { print $1 }')
[ -z "$units" ] || fail "stop active casklet workloads before testing: $units"
links=$(ip -o link show | awk -F ': ' '$2 == "casklet0" || $2 ~ /^csn/ { print $2 }')
[ -z "$links" ] || fail "remove idle casklet networks before testing: $links"
tables=$(nft list tables)
case "$tables" in *casklet_*) fail 'casklet network rules remain; clean up networks before testing' ;; esac
mounts=$(findmnt -rn -o TARGET)
case "$mounts" in *"$store"*) fail 'casklet storage still has mounted filesystems' ;; esac
[ ! -L "$store" ] || fail 'the engine store must not be a symlink'
[ ! -L "$service" ] || fail 'the restart service must not be a symlink'

manager_active=0
systemctl is-active --quiet casklet-restarts.service && manager_active=1
mkdir -m 0700 "$transaction"
printf '%s\n' "$manager_active" > "$transaction/manager-active"
store_saved=0
service_saved=0
enable_saved=0
test_started=0
test_pid=
cleanup() {
    code=$?
    trap - EXIT HUP INT TERM
    set +e
    if [ -n "$test_pid" ]; then
        kill "$test_pid" 2>/dev/null
        wait "$test_pid" 2>/dev/null
    fi
    if [ "$test_started" = 1 ]; then
        # All matching units now belong to the exclusive test run.
        if ! systemctl stop 'casklet-*'; then
            printf 'test-shared-vm: cannot stop test units; saved daily data remains at %s/store\n' "$transaction" >&2
            exit 1
        fi
        systemctl reset-failed 'casklet-*' 2>/dev/null
        # Unload the transient test manager before restoring its installed unit
        # and target link, otherwise systemd can restart the test executable.
        systemctl daemon-reload || code=1
        links=$(ip -o link show | awk -F ': ' '$2 == "casklet0" || $2 ~ /^csn/ { print $2 }')
        tables=$(nft list tables)
        if [ -n "$links" ]; then
            printf 'test-shared-vm: test network interfaces remain: %s\n' "$links" >&2
            code=1
        fi
        case "$tables" in *casklet_*) code=1 ;; esac
        mv -T "$store" "$transaction/results" || code=1
    fi
    if [ "$store_saved" = 1 ]; then
        mv -T "$transaction/store" "$store" || {
            printf 'test-shared-vm: restore failed; data is at %s/store\n' "$transaction" >&2
            exit 1
        }
    fi
    if [ "$service_saved" = 1 ]; then
        mv -T "$transaction/restart-service" "$service" || exit 1
    fi
    if [ "$enable_saved" = 1 ]; then
        mv -T "$transaction/restart-enable" "$enable" || exit 1
    fi
    systemctl daemon-reload || code=1
    if [ "$manager_active" = 1 ]; then
        systemctl start casklet-restarts.service || code=1
    fi
    if [ "$code" = 0 ]; then
        # Never recursively remove a directory containing a leaked test mount.
        mounts=$(findmnt -rn -o TARGET)
        case "$mounts" in
            *"$transaction"*) code=1 ;;
            *) rm -rf -- "$transaction" || code=1 ;;
        esac
    fi
    if [ "$code" != 0 ]; then
        printf 'test-shared-vm: daily data restored; test artifacts retained at %s\n' "$transaction" >&2
    fi
    exit "$code"
}
trap cleanup EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM
systemctl stop casklet-restarts.service
units=$(systemctl list-units --no-legend --plain --state=active,activating,deactivating 'casklet-*' |
    awk '$1 != "casklet-restarts.service" { print $1 }')
[ -z "$units" ] || fail "a workload started during preflight: $units"
if [ -f "$service" ]; then
    mv "$service" "$transaction/restart-service"
    service_saved=1
fi
# The active multi-user target would otherwise keep a stopped transient test
# manager loaded, preventing the next restart-policy test from recreating it.
if [ -L "$enable" ]; then
    mv "$enable" "$transaction/restart-enable"
    enable_saved=1
fi
systemctl daemon-reload
if [ -d "$store" ]; then
    mv "$store" "$transaction/store"
    store_saved=1
fi
mkdir -m 0755 "$store"
test_started=1
"$@" &
test_pid=$!
code=0
wait "$test_pid" || code=$?
test_pid=
exit "$code"
