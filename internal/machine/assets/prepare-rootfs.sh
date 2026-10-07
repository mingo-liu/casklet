#!/bin/sh
set -eu
umask 022

fail() { printf 'prepare-rootfs: %s\n' "$*" >&2; exit 1; }
[ "$(uname -s)" = Linux ] || fail 'Linux is required; prepare the rootfs inside the development VM'
for tool in readelf sha256sum dpkg-query mktemp; do
    command -v "$tool" >/dev/null 2>&1 || fail "missing prerequisite: $tool"
done
busybox=${BUSYBOX_BINARY:-/bin/busybox}
[ -f "$busybox" ] && [ -x "$busybox" ] || fail 'install busybox-static first'
if readelf -l "$busybox" | grep -q INTERP || readelf -d "$busybox" | grep -q NEEDED; then
    fail 'BusyBox must be statically linked (install busybox-static)'
fi
case $(uname -m) in
    aarch64) architecture=arm64; machine=AArch64 ;;
    x86_64) architecture=amd64; machine='Advanced Micro Devices X86-64' ;;
    *) fail 'only Linux arm64 and amd64 are supported' ;;
esac
readelf -h "$busybox" | grep -F "$machine" >/dev/null || fail 'BusyBox architecture does not match the host'
version=$(dpkg-query -W -f='${Version}' busybox-static) || fail 'busybox-static package is required'
package_binary=$(dpkg-query -L busybox-static | grep '/bin/busybox$' | head -n 1)
[ -n "$package_binary" ] || fail 'busybox-static package does not contain BusyBox'
cmp "$busybox" "$package_binary" >/dev/null || fail 'BusyBox does not match the installed busybox-static package'

rootfs=${1:-rootfs/busybox}
[ ! -e "$rootfs" ] && [ ! -L "$rootfs" ] || fail "destination already exists: $rootfs (remove it explicitly to regenerate)"
parent=$(dirname "$rootfs")
mkdir -p "$parent"
staging=$(mktemp -d "$parent/.busybox.XXXXXXXX")
trap 'rm -rf "$staging"' EXIT HUP INT TERM
mkdir -p "$staging/bin" "$staging/proc" "$staging/dev" "$staging/tmp" "$staging/etc" "$staging/usr/bin"
chmod 1777 "$staging/tmp"
cp "$busybox" "$staging/bin/busybox"
chmod 0755 "$staging/bin/busybox"
applets=$("$busybox" --list)
printf '%s\n' "$applets" | while IFS= read -r applet; do
    case "$applet" in ''|busybox) continue ;; */*|.|..|*\"*|*\\*) fail "invalid applet: $applet" ;; esac
    ln -s busybox "$staging/bin/$applet"
done
printf 'root:x:0:0:root:/:/bin/sh\n' > "$staging/etc/passwd"
printf 'root:x:0:\n' > "$staging/etc/group"
checksum=$(sha256sum "$staging/bin/busybox" | cut -d ' ' -f 1)
cat > "$staging/.mini-docker-rootfs.json" <<METADATA
{
  "architecture": "$architecture",
  "source": "installed Debian/Ubuntu package",
  "package": "busybox-static",
  "version": "$version",
  "sha256": "$checksum",
  "applets":
METADATA
printf '%s\n' "$applets" | awk 'BEGIN { printf "[" } $0 != "" && $0 != "busybox" { printf "%s\"%s\"", separator, $0; separator=", " } END { print "]\n}" }' >> "$staging/.mini-docker-rootfs.json"
# Commands using a numeric non-root identity must be able to traverse the root.
chmod 0755 "$staging"
mv "$staging" "$rootfs"
trap - EXIT HUP INT TERM
printf 'Prepared %s (%s, busybox-static %s, SHA-256 %s)\n' "$rootfs" "$architecture" "$version" "$checksum"
