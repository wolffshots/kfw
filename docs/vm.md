# Run Nickel in an emulator: kfw vm

`kfw vm` boots Nickel of one firmware in an ARMv7 container under qemu-user. It installs a package as the firmware boot script does, and it can keep the emulated device across reboots, an uninstall and the firmware upgrade from 4.x to 6.x.

The emulator needs the rootfs image of the firmware (`kfw index`, see [index.md](index.md)) and ARM emulation in the container engine (see [ARM emulation](#arm-emulation-in-the-podman-machine)).

## One run

```bash
./kfw vm -fw 6.0.279225 -pkg build/Kobo.tgz
./kfw vm -fw 4.46.23852 -pkg KoboRoot.tgz -time 5m
```

The run boots Nickel, waits `-time`, checks the result, writes a report, and removes the container.

| Flag | What it does |
|---|---|
| `-fw X.Y.Z` | The firmware. The default is the newest firmware with a rootfs image. |
| `-pkg <file>` | The package to install before Nickel starts. On 6.x, `kfw` does not install a `KoboRoot.tgz`, as the firmware does not. |
| `-as kobo\|koboroot` | The install file of `-pkg`, if its name does not tell it. |
| `-time 3m` | How long Nickel runs before `kfw` checks it. |
| `-name <id>` | The device id. The container is `kfw-vm-<id>`, and the state folder is `vm/<id>/`. The default is the firmware version. Give each test that runs at the same time its own id. |
| `-state <dir>` | Use this state folder, and keep its user partition `onboard/`. Without it, `kfw` makes `vm/<id>/` new. |
| `-fat` | Make the user partition a FAT32 image, as on a device. See [The user partition](#the-user-partition). |
| `-keep` | Leave the device running. Then use `kfw vm exec <id> sh` and `kfw vm stop <id>`. |
| `-strace` | Write each system call of Nickel to `strace.log` (qemu-user `QEMU_STRACE`). It shows what Nickel waits for or fails to open. |

`kfw vm` writes these files to the state folder:

| File | Content |
|---|---|
| `report.md` | The result: is Nickel still running, and did Qt load each plugin of the package that matches its Qt version and skip the others. |
| `nickel.log` | The output of Nickel, with `QT_DEBUG_PLUGINS=1`: Qt logs each plugin that it finds, loads or skips. With `-strace`, the error output goes to `strace.log`. |
| `syslog.txt` | The syslog of 6.x: Nickel and NickelHook log here. |
| `logread.txt` | The syslog of 4.x. Nickel 4.x replaces the syslog daemon with an in-memory one, so `kfw` reads it with `logread` at the end. |
| `screen.png` | The screen at the end. |
| `container.log` | The output of the start script: what it installed. |
| `onboard/` | The user partition, `/mnt/onboard` in the device (not with `-fat`). |

Exit code 1 means that Nickel stopped, a matching plugin did not load, a plugin of the other Qt version loaded, or `kfw` could not read the 4.x syslog (ERROR in the report).

`vm/<id>.json` records a device, and it is the lock of the id. A second run with the same id stops with an error. If a run stopped without cleanup, for example after Ctrl+C, run `kfw vm stop <id>`.

## A device across reboots and upgrades

A mod must survive a reboot, its own uninstall and the firmware upgrade. These subcommands keep one device:

| Command | What it does |
|---|---|
| `kfw vm start -fw X -name <id> [-pkg <file>] [-fat] [-wait text] [-time 2m]` | Boots the device and leaves it running. With `-wait`, it waits until a syslog line has the text, for example `-wait "done init"`, for at most `-time`. Without `-wait`, it waits `-time`. Exit code 1 if Nickel stopped or the text did not come. |
| `kfw vm exec <id> <command>...` | Runs a shell command in the device and returns its exit code. |
| `kfw vm cp <file> <id>:<path>` | Copies a file into the device. |
| `kfw vm cp <id>:<path> <file>` | Copies a file out of the device. |
| `kfw vm logs <id>` | Prints the syslog of the current boot: `logread` on 4.x, `syslog.txt` on 6.x. |
| `kfw vm shot <id> [file.png]` | Saves the screen. The default file is `screen-<time>.png` in the state folder. |
| `kfw vm reboot <id> [-wait text] [-time 2m]` | Stops Nickel, keeps the root file system (`podman commit` to `localhost/kfw-vm-<id>:boot<n>`), and boots again. |
| `kfw vm upgrade <id> <version> [-wait text] [-time 2m]` | Boots a clean root file system of `<version>` with the same user partition, as a firmware update does. |
| `kfw vm forge <id> <dir>` | Serves a fake Forgejo in the device. See [A fake Forgejo](#a-fake-forgejo). |
| `kfw vm stop <id>` | Removes the container, the images of its reboots, the FAT image and the lock. The state folder stays. |

Each boot installs packages as the firmware does: first the `-pkg` file, then a `Kobo.tgz` in `.kobo/` on the user partition (on 4.x also a `KoboRoot.tgz`). The boot deletes the files in `.kobo/` after the install. So a test copies a package to `.kobo/` and reboots, as a user does over USB.

For example:

```bash
./kfw vm start -fw 4.46.23852 -name ns -pkg old/KoboRoot.tgz -wait "done init"
./kfw vm cp build/Kobo.tgz ns:/mnt/onboard/.kobo/Kobo.tgz
./kfw vm reboot ns -wait "done init"
./kfw vm upgrade ns 6.0.279225
./kfw vm exec ns 'ls /usr/local/Kobo/imageformats'
./kfw vm stop ns
```

In Git Bash on Windows, set `MSYS_NO_PATHCONV=1` before you give a device path to `kfw.exe`. Else Git Bash turns an argument such as `/mnt/onboard/x` into a Windows path. Then use Windows paths for the host files, for example `C:/temp/x`.

`examples/upgrade-test.sh` runs a full path: the old package on 4.x, the new package, the upgrade to 6.x, the package again, and the uninstall. It reaches the device only through `kfw`, so it works with both kinds of user partition.

## The user partition

By default, the user partition is the folder `onboard/` in the state folder, a bind mount of a host folder. You can read and change its files from the host. But it differs from a device:

- Under Podman on Windows, the folder is a 9p mount. Its inode numbers are larger than 32 bits, so a 32-bit `stat()` in a mod fails with `EOVERFLOW`.
- It has the file system rules of the host: symlinks, permissions, and (on Linux) names with case.

With `-fat`, the user partition is a 256 MB FAT32 image (label `KOBOeReader`), loop-mounted at `/mnt/onboard` with the vfat options of a device. It is in the volume `kfw-vm-<id>-onboard`, so it stays across `reboot` and `upgrade`, and `stop` removes it. The host cannot read the image directly: use `kfw vm cp` and `kfw vm exec`. Use `-fat` to test file code of a mod before a device test.

## A fake Forgejo

A package manager or an updater in the device can test its downloads without the internet. `kfw vm forge <id> <dir>` builds `tools/fakeforge` for ARM (it needs Go), copies `<dir>` to `/state/forge` in the device and starts the server on `https://localhost:8443`:

| Path | Content |
|---|---|
| `<dir>/registry.toml` | Served at `/<owner>/<repo>/raw/branch/<ref>/registry.toml`. |
| `<dir>/repos/<repo>/<tag>/<asset>` | Releases. `releases/latest` is the greatest tag name. |

The API has `/api/v1/version`, `/api/v1/repos/o/<repo>/releases/latest`, `/api/v1/repos/o/<repo>/releases/tags/<tag>` and the downloads under `/dl/`. The owner is always `o`. The server makes a new self-signed certificate at each start: tell the client in the device to trust `/state/forge/ca.pem`, for example with `SSL_CERT_FILE`. A reboot stops the server. Run `kfw vm forge` again after it.

The client must have a current TLS stack, for example a Go program such as kpm. The busybox `wget` of the firmware cannot connect: its TLS client sends no signature algorithms, and the server refuses the handshake. The firmware has no `curl`.

## ARM emulation in the Podman machine

The container engine must run ARM programs. The Podman machine has `qemu-user-static`, but under WSL the ARM handler is often not registered. All WSL distros share one `binfmt_misc`, and `systemd-binfmt` in another distro removes all handlers when that distro starts, also during a run.

Make the handler permanent once:

```bash
./kfw vm setup
```

It installs the service `kfw-binfmt.service` in the Podman machine. The service registers qemu-arm again within a second when it is gone, and starts with the machine. It also masks `proc-sys-fs-binfmt_misc.automount`, a systemd automount that can hide `binfmt_misc` behind a stale WSL mount. `kfw vm setup -remove` undoes both.

Without the service, `kfw vm` does the same for the length of a run: it checks that an ARM program runs before each boot and each `exec`, registers the handler if it is gone, and runs a keeper loop in the machine until no `kfw-vm-` container runs.

If this fails, run the commands yourself:

```bash
podman machine ssh "sh -c 'cat /usr/lib/binfmt.d/qemu-arm-static.conf > /proc/sys/fs/binfmt_misc/register'"
podman machine ssh 'umount -l /proc/sys/fs/binfmt_misc; mount -t binfmt_misc binfmt_misc /proc/sys/fs/binfmt_misc'
```

`podman machine ssh` from Git Bash writes a file `NUL` with the host key of the machine into the current folder. Delete it. `kfw` runs the command in the temp folder.

## How the emulator works

The design follows the Nickel VM of NickelInkSync PR 27 (github.com/dkuester/NickelInkSync, MIT license, see `THIRD_PARTY_NOTICES`):

- The root file system is the image `localhost/kfw-rootfs:<version>`.
- `/dev/fb0` is a file. `tools/kfw/vm/vmshim.c`, preloaded into Nickel, answers the framebuffer ioctls and accepts the e-ink update ioctls. `kfw` builds it with the toolchain image.
- A file stands in for the NTX hardware configuration partition (board of the Clara Colour, brand Kobo). A tmpfs fakes a battery at 80 %.
- For 4.x, `init.sh` does what the 4.x rcS and the base rootfs do: it links `/dev/mmcblk0p6` (the hardware configuration) and a blank `/dev/mmcblk0`, sets `PLATFORM` and `PRODUCT` with the firmware tools `ntx_hwconfig` and `kobo_config.sh` (without them Nickel 4.x takes an old Freescale device, finds no battery and powers off), makes `/tmp` and `/var`, and writes open D-Bus configs. The shim drops `O_TRUNC` when Nickel opens a disk file.
- `poweroff`, `reboot`, USB export, `wpa_supplicant` and `dhcpcd` are stubs. There is no Wi-Fi, touch input or sync.
- Nickel is process 1 of the container. So the container runs while Nickel runs.

Both firmware lines boot to the language screen in about 30 seconds. Nickel 4.x shows no fonts: the 4.x `KoboRoot.tgz` has none, and the base rootfs of a device has them.

## Differences from a device

- The emulator runs several times slower than the device. It has no e-ink, Wi-Fi or touch. It shows that Nickel starts, that the plugin loads and what the mod does with its files. It does not replace a device test.
- The default user partition is a host folder. See [The user partition](#the-user-partition).
- `df /` shows the container overlay, not the free space of the firmware root file system.
- On 6.0, busybox `pidof nickel` returns nothing, because Nickel is process 1. Use `ps | grep [n]ickel`.
- The 6.0 busybox is version 1.35. Its `head` needs `-n 4`, not `-4`.
