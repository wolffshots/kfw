#!/bin/sh
# Start script of the kfw emulator. It runs under qemu-user as /bin/sh of
# the firmware. It stands in for the hardware that Nickel expects, installs
# the package like the boot script of the firmware, and starts Nickel.
#
# Adapted from vm/init.sh of NickelInkSync PR 27
# (github.com/dkuester/NickelInkSync, MIT license).
#
# Mounts (kfw vm):
#   /vm     this folder and vmshim.so, read-only
#   /state  hwcfg.img, fb0.raw, onboard/ (the user partition), aux/, pkg/
# Writes /state/nickel.log (stdout and stderr of Nickel) and
# /state/syslog.txt.

# The 4.x KoboRoot.tgz has no /tmp and /var: the base rootfs of the device has them.
mkdir -p /tmp /var/lib/dbus /var/run/dbus /var/log /run/dbus
chmod 1777 /tmp

# The NTX hardware configuration: brand, board, screen.
mkdir -p /dev/disk/by-partlabel
ln -sf /state/hwcfg.img /dev/disk/by-partlabel/hwcfg

mkdir -p /mnt/onboard /mnt/aux
mount --bind /state/onboard /mnt/onboard
mount --bind /state/aux /mnt/aux

# A battery at 80 %, not charging.
mount -t tmpfs none /sys/class/power_supply
for bat in battery bd71827_bat; do
    mkdir -p /sys/class/power_supply/$bat
    cd /sys/class/power_supply/$bat || exit 1
    echo 80 > capacity
    echo 'Not charging' > status
    echo 0 > charging
    echo 0 > charger_type
    echo 0 > time_to_full_now
done
cd /

# Nothing may power off the container or hand the user partition to USB.
# Replace each command in the writable layer of the container. Do not bind
# mount over it: on 4.x /sbin/reboot is a symlink to busybox, and a bind
# mount would replace busybox itself.
for f in /sbin/poweroff /sbin/reboot /sbin/halt /usr/bin/export-usb.sh \
        /usr/sbin/wpa_supplicant /sbin/dhcpcd /usr/libexec/dhcpcd-dbus; do
    if [ -e "$f" ] || [ -L "$f" ]; then
        rm -f "$f" && cp /vm/stub.sh "$f" && chmod 755 "$f"
    fi
done

# Install the package as the boot script of the firmware does. 4.x (rcS):
# Kobo.tgz into /usr/local/Kobo, KoboRoot.tgz into /. 5.x and 6.x
# (/etc/init.d/ota): Kobo.tgz only, and each file of the archive that exists
# in /usr/local/Kobo is removed first. KoboRoot.tgz is not read.
if [ -f /usr/libexec/platform/nickel-env.sh ]; then
    if [ -f /state/pkg/Kobo.tgz ]; then
        echo "kfw: installing Kobo.tgz (6.x ota)"
        tar -tzf /state/pkg/Kobo.tgz | while read -r f; do
            if [ -f "/usr/local/Kobo/$f" ] || [ -L "/usr/local/Kobo/$f" ]; then
                rm -f "/usr/local/Kobo/$f"
            fi
        done
        tar -xzf /state/pkg/Kobo.tgz -C /usr/local/Kobo || echo "kfw: Kobo.tgz failed"
    fi
    [ -f /state/pkg/KoboRoot.tgz ] && echo "kfw: KoboRoot.tgz ignored (6.x does not read it)"
else
    if [ -f /state/pkg/Kobo.tgz ]; then
        echo "kfw: installing Kobo.tgz (4.x rcS)"
        tar -xzf /state/pkg/Kobo.tgz -C /usr/local/Kobo || echo "kfw: Kobo.tgz failed"
    fi
    if [ -f /state/pkg/KoboRoot.tgz ]; then
        echo "kfw: installing KoboRoot.tgz (4.x rcS)"
        tar -xzf /state/pkg/KoboRoot.tgz -C / || echo "kfw: KoboRoot.tgz failed"
    fi
fi

[ -p /tmp/nickel-hardware-status ] || mkfifo /tmp/nickel-hardware-status
ln -sf /state/fb0.raw /dev/fb0

# Busybox syslogd writes the syslog of Nickel and NickelHook to a file.
syslogd -n -O /state/syslog.txt &

mkdir -p /run/dbus /var/lib/dbus /var/run/dbus
rm -f /run/dbus/pid /var/run/dbus/pid
if [ -f /usr/libexec/platform/nickel-env.sh ]; then
    # Firmware 5.x and 6.x.
    export CPU_CORES=2
    . /usr/libexec/platform/nickel-env.sh
    export QT_USE_SYSTEM_LINE_METRICS=1
    dbus-daemon --system --fork
    ARGS="-platform kobo"
else
    # Firmware 4.x, as /etc/init.d/rcS does. On an MTK board the hardware
    # configuration is in partition 6. Nickel also reads the raw disk.
    ln -sf /state/mmcblk0.img /dev/mmcblk0
    ln -sf /state/hwcfg.img /dev/mmcblk0p6
    # Nickel 4.x takes the device from PLATFORM and PRODUCT. Without them it
    # takes an old Freescale device, reads no battery and powers off.
    CPU=$(ntx_hwconfig -S 1 -p /dev/mmcblk0p6 CPU)
    PLATFORM=$CPU-ntx
    WIFI=$(ntx_hwconfig -S 1 -p /dev/mmcblk0p6 Wifi)
    BATTERY=$(ntx_hwconfig -S 1 -p /dev/mmcblk0p6 Battery)
    PRODUCT=$(/bin/kobo_config.sh)$(/bin/kobo_config.sh --odm)
    INTERFACE=wlan0 WIFI_MODULE=wlan_drv_gen4m
    export PLATFORM PRODUCT WIFI BATTERY INTERFACE WIFI_MODULE
    echo "kfw: PLATFORM=$PLATFORM PRODUCT=$PRODUCT"
    # The base rootfs of the device has the D-Bus configuration, the
    # KoboRoot.tgz overlay does not. Allow everything on both buses.
    if [ ! -f /etc/dbus-1/system.conf ]; then
        mkdir -p /etc/dbus-1
        for bus in system session; do
            listen="unix:path=/var/run/dbus/system_bus_socket"
            [ $bus = session ] && listen="unix:tmpdir=/tmp"
            cat > /etc/dbus-1/$bus.conf <<EOF
<!DOCTYPE busconfig PUBLIC "-//freedesktop//DTD D-Bus Bus Configuration 1.0//EN"
 "http://www.freedesktop.org/standards/dbus/1.0/busconfig.dtd">
<busconfig>
  <type>$bus</type>
  <listen>$listen</listen>
  <auth>EXTERNAL</auth>
  <policy context="default">
    <allow user="*"/>
    <allow own="*"/>
    <allow send_type="method_call"/>
    <allow send_destination="*"/>
    <allow receive_sender="*"/>
  </policy>
</busconfig>
EOF
        done
    fi
    export NICKEL_HOME=/mnt/onboard/.kobo LD_LIBRARY_PATH=/usr/local/Kobo LANG=en_US.UTF-8
    /bin/dbus-uuidgen > /var/lib/dbus/machine-id 2>/dev/null
    /bin/dbus-daemon --system --fork 2>/dev/null || dbus-daemon --system --fork
    ARGS="-platform kobo -skipFontLoad"
fi
DBUS_SESSION_BUS_ADDRESS=$(dbus-daemon --session --print-address --fork)
export DBUS_SESSION_BUS_ADDRESS
echo "kfw: starting nickel $ARGS"

export QT_DEBUG_PLUGINS=1 LD_PRELOAD=/vm/vmshim.so VMSHIM_FB_FILE=/state/fb0.raw
if [ "$KFW_STRACE" = 1 ]; then
    # qemu-user writes the system calls to stderr, with the output of Nickel.
    export QEMU_STRACE=1
    exec /usr/local/Kobo/nickel $ARGS > /state/nickel.log 2> /state/strace.log
fi
exec /usr/local/Kobo/nickel $ARGS > /state/nickel.log 2>&1
