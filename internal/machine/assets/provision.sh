#!/bin/sh
set -eu
export PATH=/usr/sbin:/usr/bin:/sbin:/bin
export DEBIAN_FRONTEND=noninteractive

dependencies_ready() {
    for tool in busybox readelf ip nft unshare conntrack apparmor_parser; do
        command -v "$tool" >/dev/null 2>&1 || return 1
    done
    [ "$(dpkg-query -W -f='${db:Status-Status}' busybox-static 2>/dev/null)" = installed ]
}

ready() {
    [ -f /usr/local/lib/casklet/provisioned-v1 ] && dependencies_ready
}

if [ "${1:-}" = --check ]; then
    ready
    exit $?
fi

# Serialize cloud-init and client repair, including callers with separate host state.
exec 9>/run/lock/casklet-provision.lock
flock -w 540 9
if ready; then exit 0; fi
rm -f /usr/local/lib/casklet/provisioned-v1

user=${1:-${SUDO_USER:-}}
uid=${2:-${SUDO_UID:-}}
[ -n "$user" ] && [ -n "$uid" ] && [ "$uid" != 0 ] && [ "$(id -u -- "$user")" = "$uid" ] || {
    echo 'casklet: cannot identify the runtime machine user' >&2
    exit 1
}

apt() {
    apt-get -o Acquire::Retries=3 -o Acquire::http::Timeout=30 -o Acquire::https::Timeout=30 -o DPkg::Lock::Timeout=60 "$@"
}
apt update
# Resume packages left unpacked or unconfigured by interrupted initialization.
dpkg --configure --pending || apt install --fix-broken -y
apt install --reinstall -y busybox-static binutils iproute2 nftables util-linux conntrack apparmor-utils
dependencies_ready
cat > /etc/apparmor.d/casklet <<'PROFILE'
abi <abi/4.0>,
profile casklet-rootless /usr/local/bin/casklet flags=(unconfined,attach_disconnected) {
  userns,
}
PROFILE
apparmor_parser -r /etc/apparmor.d/casklet
loginctl enable-linger "$user"
controllers=$(systemctl show --property=DelegateControllers --value "user@$uid.service")
directory="/etc/systemd/system/user@$uid.service.d"
mkdir -p "$directory"
printf '[Service]\nDelegate=cpu memory pids\n' > "$directory/casklet.conf"
systemctl daemon-reload
# Package repair must not restart an already delegated user manager or its workloads.
case " $controllers " in *' cpu '*) cpu=1 ;; *) cpu=0 ;; esac
case " $controllers " in *' memory '*) memory=1 ;; *) memory=0 ;; esac
case " $controllers " in *' pids '*) pids=1 ;; *) pids=0 ;; esac
if [ "$cpu$memory$pids" != 111 ]; then
    systemctl restart "user@$uid.service"
fi
mkdir -p /usr/local/lib/casklet
touch /usr/local/lib/casklet/provisioned-v1
