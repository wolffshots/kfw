#!/bin/sh
# upgrade-test.sh: a NickelHook mod across reboots and the firmware upgrade
# from 4.x to 6.x, with the kfw vm subcommands. It is path A of the
# NickelSync upgrade suite (hand install):
#
#   1. The old KoboRoot.tgz on 4.x. The mod starts.
#   2. The user copies the new dual Kobo.tgz to .kobo/ and reboots. The Qt 5
#      build loads.
#   3. The firmware upgrade to 6.x. The new rootfs has no mod. The user
#      partition is the same.
#   4. The user copies the Kobo.tgz again and reboots. The Qt 6 build loads.
#   5. The user deletes the uninstall flag and reboots. The mod uninstalls.
#
# Usage, from the nickel-symbols folder:
#
#   sh examples/upgrade-test.sh <old KoboRoot.tgz> <new Kobo.tgz> <flag>
#
# <flag> is the NickelHook !flag file of the mod, relative to the user
# partition, for example .adds/nickelsync/uninstall. The mod must log
# "done init" (NickelHook does). Environment: KFW (default ./kfw), ID
# (default upgrade-test), FROM (4.46.23852), TO (6.0.279225).
set -u
OLD=$1 NEW=$2 FLAG=$3
KFW=${KFW:-./kfw} ID=${ID:-upgrade-test} FROM=${FROM:-4.46.23852} TO=${TO:-6.0.279225}
ON=vm/$ID/onboard
fails=0
check() { # check <name> <command>: PASS if the command succeeds
  if sh -c "$2" >/dev/null 2>&1; then echo "PASS: $1"; else echo "FAIL: $1"; fails=$((fails+1)); fi
}
libs() { $KFW vm exec "$ID" 'ls /usr/local/Kobo/imageformats/ 2>/dev/null'; }

$KFW vm stop "$ID" >/dev/null 2>&1
echo "== 1. old package on $FROM"
$KFW vm start -fw "$FROM" -name "$ID" -pkg "$OLD" -wait "done init" -time 5m || exit 1
check "the flag file exists" "test -f '$ON/$FLAG'"
echo "kfw upgrade test" > "$ON/kfw-marker.txt"

echo "== 2. new Kobo.tgz, reboot on $FROM"
cp "$NEW" "$ON/.kobo/Kobo.tgz"
$KFW vm reboot "$ID" -wait "done init" -time 5m
libs
check "Kobo.tgz was taken from .kobo" "test ! -f '$ON/.kobo/Kobo.tgz'"
check "no uninstall on $FROM" "! $KFW vm logs $ID | grep -q uninstalling"

echo "== 3. firmware upgrade to $TO"
$KFW vm upgrade "$ID" "$TO" -time 2m
check "the new rootfs has no plugin" "test -z \"\$($KFW vm exec $ID 'ls /usr/local/Kobo/imageformats 2>/dev/null')\""
check "the user partition stays" "grep -q 'kfw upgrade test' '$ON/kfw-marker.txt' && test -f '$ON/$FLAG'"

echo "== 4. Kobo.tgz again, reboot on $TO"
cp "$NEW" "$ON/.kobo/Kobo.tgz"
$KFW vm reboot "$ID" -wait "done init" -time 5m
libs
check "no uninstall on $TO" "! $KFW vm logs $ID | grep -q uninstalling"
$KFW vm shot "$ID" "vm/$ID/step4.png"

echo "== 5. flag deleted, reboot on $TO"
rm -f "$ON/$FLAG"
$KFW vm reboot "$ID" -wait "uninstalling" -time 5m
check "the mod uninstalled itself" "$KFW vm logs $ID | grep -q uninstalling"
check "no plugin is left" "test -z \"\$($KFW vm exec $ID 'ls /usr/local/Kobo/imageformats 2>/dev/null')\""
check "Nickel runs" "$KFW vm exec $ID 'ps | grep -q [n]ickel'"

$KFW vm stop "$ID"
echo "$fails failed checks"
[ "$fails" = 0 ]
