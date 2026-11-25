#!/bin/sh
# reboot-update-test.sh: an update that keeps the file size must survive
# kfw vm reboot.
#
# kfw vm reboot keeps the root file system with podman commit. Podman overlay
# storage with metacopy=on finds a changed file by its size and mtime only.
# A mod package often has mtime 0 on each file, and a small code change can
# keep the size of a library. Then the commit misses the update, and the next
# boot runs the old file. init.sh extracts packages with tar -m to prevent it.
#
#   1. Install version 1 of a file through the package slot of .kobo/ and
#      reboot twice. The device has version 1.
#   2. Install version 2: same size, same mtime 0, other content. Reboot
#      once: the device has version 2. Reboot again: it must still have
#      version 2.
#
# Usage, from the nickel-symbols folder:
#
#   sh examples/reboot-update-test.sh [firmware]
#
# The firmware (default 6.0.279225) needs a rootfs image. 4.x gets a
# KoboRoot.tgz (rcS extracts it into /), 5.x and 6.x a Kobo.tgz (ota extracts
# it into /usr/local/Kobo). Environment: KFW (default ./kfw), ID (default
# reboot-update-test). The script stops the device at the end.
set -u
# Git Bash turns an argument such as /mnt/onboard/x into a Windows path for kfw.exe.
export MSYS_NO_PATHCONV=1
FW=${1:-6.0.279225}
KFW=${KFW:-./kfw} ID=${ID:-reboot-update-test}
FILE=/usr/local/Kobo/kfw-reboot-update-test.txt
case $FW in
  4.*) PKG=KoboRoot.tgz MEMBER=usr/local/Kobo/kfw-reboot-update-test.txt ;;
  *)   PKG=Kobo.tgz MEMBER=kfw-reboot-update-test.txt ;;
esac
fails=0
check() { # check <name> <want>: PASS if the device file has the md5 <want>
  got=$($KFW vm exec "$ID" "md5sum $FILE" 2>/dev/null | cut -c1-32)
  if [ "$got" = "$2" ]; then echo "PASS: $1"; else echo "FAIL: $1 (md5 ${got:-none}, want $2)"; fails=$((fails+1)); fi
}
boot() { $KFW vm reboot "$ID" -time 15s >/dev/null; }

# A relative folder: kfw.exe on Windows cannot open the /tmp of Git Bash.
tmp=$(mktemp -d ./.reboot-update-test.XXXXXX)
trap 'rm -rf "$tmp"' EXIT
# pkg <version>: a package with one file of the same size for each version.
pkg() {
  mkdir -p "$tmp/v$1/$(dirname "$MEMBER")"
  printf 'kfw reboot update test, version %s\n' "$1" > "$tmp/v$1/$MEMBER"
  tar -czf "$tmp/v$1.tgz" --mtime=@0 --owner=0 --group=0 -C "$tmp/v$1" "$MEMBER"
  md5sum < "$tmp/v$1/$MEMBER" | cut -c1-32
}
v1=$(pkg 1)
v2=$(pkg 2)

$KFW vm stop "$ID" >/dev/null 2>&1
$KFW vm start -fw "$FW" -name "$ID" -time 15s >/dev/null || { echo "FAIL: kfw vm start"; exit 1; }

$KFW vm cp "$tmp/v1.tgz" "$ID:/mnt/onboard/.kobo/$PKG" >/dev/null
boot
check "version 1 installed" "$v1"
boot
check "version 1 after a reboot" "$v1"

$KFW vm cp "$tmp/v2.tgz" "$ID:/mnt/onboard/.kobo/$PKG" >/dev/null
boot
check "version 2 installed (same size, same mtime)" "$v2"
boot
check "version 2 after a reboot" "$v2"

$KFW vm stop "$ID" >/dev/null
echo "$fails failed"
[ "$fails" = 0 ]
