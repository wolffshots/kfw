# kfw: Kobo firmware tools for Nickel mods

`kfw` indexes Kobo firmware and tests Nickel mods against it, without a device. It does these jobs:

- It asks the Kobo update API for new firmware, downloads it and indexes `libnickel.so.1.0.0`: symbols, direct calls, Qt signals and slots, and strings.
- It checks the Nickel names in mod sources against each indexed firmware (`kfw check`).
- It checks a `Kobo.tgz` or `KoboRoot.tgz` against the libraries of each firmware (`kfw test`).
- It boots Nickel of a firmware in an ARM container with a package installed, and it keeps the emulated device across reboots and a firmware upgrade (`kfw vm`).

Nickel is the Kobo reading app. Mods such as NickelMenu, NickelDBus, kpm and NickelSync call functions in `libnickel` by symbol name. A firmware update can rename or remove a function. Firmware 6.x moves from Qt 5 to Qt 6, and a Qt 5 mod does not load on it.

The firmware files and everything that `kfw` makes from them (`firmware/`, `downloads/`, `cache/`, `vm/`, the rootfs images) stay local. The repository holds only the tool. See `.gitignore`.

## Set up

1. Install Go 1.27 or later, and Podman (or Docker).
2. Start the Podman machine:

   ```bash
   podman machine start
   ```

3. Build `kfw` in this folder:

   ```bash
   go -C tools build -o .. ./kfw
   ```

4. Run `kfw` from this folder or a subfolder. It finds the folder by the file `devices.tsv`.

`kfw` pulls the image `ghcr.io/pgaskin/nickeltc:1.0` on first use. The image has the ARM `readelf`, `objdump`, `c++filt` and `strings` tools, `debugfs`, `tar` and the GCC that builds the emulator shim.

To test `kfw`, run `go -C tools test ./...`.

## Layout

| Path | What it is |
|---|---|
| `kfw` (`kfw.exe` on Windows) | The tool. |
| `devices.tsv` | The devices that `kfw sync` asks Kobo about. |
| `mods.tsv` | The mods that `kfw sync` checks: name, sources, Qt majors, baseline firmware and role. |
| `tools/kfw/` | The source of `kfw`: `sync.go` (update API, downloads), `index.go` (rootfs image, index), `test.go` (package test), `vm.go` and `vm/` (emulator). |
| `tools/modcheck/` | Checks the Nickel names of mod sources against the index and writes the report. `qtpp.go` evaluates `#if QT_VERSION_MAJOR`. |
| `tools/qtmeta/` | Reads the Qt signals and slots out of a library (Qt 5 and Qt 6). |
| `examples/upgrade-test.sh` | A mod across reboots and the 4.x to 6.x upgrade, with the `kfw vm` subcommands. |
| `report.md` | The last compatibility report. `kfw sync` writes it. Local. |
| `available.tsv` | The firmware files that the Kobo update API offered at the last run. `kfw sync` writes it. Local. |
| `firmware/<version>/` | The index of one firmware. See the next table. Local. |
| `downloads/` | Firmware files while `kfw sync` indexes them. It deletes each file after the index is done, unless you give `-keep`. Local. |
| `cache/vm/` | The emulator helpers: `init.sh`, `stub.sh` and `vmshim.so`. Local. |
| `vm/<id>/`, `vm/<id>.json` | The state folder and the record of one emulated device. Local. |
| `catalog.md`, `dual-qt-research.md` | Research notes: useful Nickel functions, and one mod for Qt 5 and Qt 6. Local, not in the repository. |

Each `firmware/<version>/` folder holds these files:

| File | Content |
|---|---|
| `manifest.json` | The firmware version, the device, the source file, SHA-256 hashes of the firmware file and of `libnickel`, the file format (`koboroot` or `rootfs`), the Qt major version (`qt_major`), the Qt version (`qt_version`), the generation date and the toolchain. Indexes made before 2026-11-17 have no `format` and `qt_major`. They are firmware 4.x and Qt 5. |
| `symbols.tsv` | Every defined dynamic symbol: address, size, type (`FUNC` or `OBJECT`), binding, mangled name and demangled name. |
| `calls.tsv` | Every distinct direct call in the library: caller and callee, both demangled and fully qualified. |
| `qtmeta.json` | For each class with a Qt meta-object: its Qt base class, signals, slots, invokable methods and constructors, with parameter types, parameter names and access. |
| `strings.txt` | The printable strings of the library: UI text, Qt meta names and log messages. |
| `libs.json.gz` | Every shared object of the firmware, and the `nickel` executable: path, soname, needed libraries, run path and exported symbols with their version (`memcpy@GLIBC_2.4`). `kfw test` uses it. |
| `files.txt` | Every file and symlink of the firmware root file system. `kfw test` uses it. |

The large indexes are TSV, not JSON. A TSV file is about half the size of the same data in JSON, and `grep` can search it one line per entry. `qtmeta.json` is JSON because each class has nested data.

## Indexed firmwares

| Firmware | Device | Date | `libs.json.gz` and rootfs image |
|---|---|---|---|
| 6.0.279225 | Kobo Libra Colour, Clara BW, Clara Colour (beta, Qt 6, `qt6-update-6.0.279225.zip`) | 2026-11-17 | Yes |
| 4.46.23852 | Kobo Libra Colour (`kobo13`, model `0390`) | 2026-11-10 | Yes |
| 4.46.23836 | Kobo Libra Colour (`kobo13`, model `0390`) | 2026-11-10 | No |
| 4.45.23792 | Kobo Libra Colour (`kobo13`, model `0390`) | 2026-11-10 | No |
| 4.45.23697 | Kobo Libra Colour (`kobo13`, model `0390`) | 2026-11-10 | No |

`kfw check` uses all five. `kfw test` uses only the firmwares with `libs.json.gz`. `kfw vm` uses only the firmwares with a rootfs image. To add the library index and the image to an older firmware, run `kfw index -libs <firmware.zip>` with its firmware file.

When `kfw sync` or `kfw index` adds a firmware, add a row to this table.

The Clara BW and Clara Colour (`kobo12`) get a different 4.46.23852 file than the Libra Colour (`kobo13`), but its `libnickel` has the same SHA-256. So the index has one folder for each version, not for each device.

## Commands

| Command | What it does |
|---|---|
| `kfw sync [-offline] [-keep]` | Asks Kobo for new firmware, downloads and indexes each new version, checks each mod in `mods.tsv`, and writes `report.md`. See [Check the mods against new firmware](#check-the-mods-against-new-firmware). |
| `kfw index [-libs] <firmware.zip>` | Builds the rootfs image and the index of one firmware file. |
| `kfw index [-libs] <version>` | Builds the index again from the rootfs image of that version. |
| `kfw check [-qt 5\|6\|5,6] [-baseline X.Y.Z[,X.Y.Z]] [-out file] <source>...` | Checks the Nickel names in mod sources. See [Check one mod](#check-one-mod-against-the-indexed-firmwares). |
| `kfw test [-as kobo\|koboroot] [-fw X.Y.Z,...] [-out file] <package>` | Checks an install package against each firmware, without a device. See [Test an install package](#test-an-install-package). |
| `kfw vm [flags]` | Boots Nickel of a firmware with a package installed, checks it and writes a report. See [Run Nickel in an emulator](#run-nickel-in-an-emulator). |
| `kfw vm start\|exec\|logs\|shot\|reboot\|upgrade\|stop` | Keeps an emulated device across boots. See [Test across reboots and upgrades](#test-across-reboots-and-upgrades). |
| `kfw qtmeta <libnickel.so.1.0.0>` | Prints the Qt meta data of a library as JSON. |

`kfw index` keeps the root file system of each firmware as the container image `localhost/kfw-rootfs:<version>`. `kfw test` does not need it, but `kfw vm` and `kfw index <version>` do. An image is 0.3 GB (4.x) to 0.7 GB (6.x). Remove one with `podman rmi localhost/kfw-rootfs:<version>`. If the image is missing, run `kfw index -libs <firmware.zip>` again.

`-libs` writes only `libs.json.gz` and `files.txt` and keeps the libnickel index. It takes about 30 seconds from an image. Without `-libs`, `kfw index` also reads all of libnickel again, which takes 15 minutes (4.x) to 60 minutes (6.x).

## Check the mods against new firmware

Run this command:

```bash
./kfw sync
```

The command does these steps:

1. It asks the Kobo update API for the newest firmware of each device in `devices.tsv`, on three channels. See the next table.
2. It writes the answers to `available.tsv`. It accepts only files on `https://ereaderfiles.kobo.com/`.
3. For each version that has no `libs.json.gz` in `firmware/`, it downloads the file to `downloads/<version>/`, runs `kfw index`, and deletes the file.
4. It checks each mod in `mods.tsv` and writes `report.md`. It prints the table of the report.

| Channel | Request | Answer |
|---|---|---|
| stable | Version `4.0.0` | The newest 4.x, `kobo-update-X.zip` |
| beta | Version `6.0.0` | The newest 6.x, `qt6-update-X.zip` |
| preview | The newest stable version, with the header `X-Kobo-Accept-Preview: true` | The 4.x to 6.x upgrade that an opted-in device gets, `qt5-qt6-X.zip` (577 MB for 6.0.279225) |

If one version comes in several files, the command takes the smallest kind: `kobo-update`, then `qt6-update`, then `qt5-qt6`.

Options:

- `-offline`: ask Kobo nothing and download nothing. Only run the check.
- `-keep`: keep the firmware files in `downloads/`.

Exit code 1 means that a mod with role `check` fails on a firmware. The command sends one request for each device and channel, and it runs only when you start it. Do not schedule it. The Kobo Terms of Use do not allow a robot that monitors the service.

To track one more device, add its model number to `devices.tsv`. The model numbers are in `kobo/device.go` of github.com/pgaskin/koboutils. For example, the Elipsa 2E is 389 and the Libra 2 is 388.

To check one more mod, add a row to `mods.tsv`. The `qt` column is `5`, `6`, or `5,6` for a source that builds for both. The `baseline` column is the firmware that the mod was tested on, one for each Qt major (`4.46.23852,6.0.279225`), or `-` for the newest firmware of each Qt major.

### Read the report

The report has one table: one row for each firmware and one column for each mod. A cell is one of:

- OK: the firmware has every Nickel name that the mod uses on its baseline.
- DEGRADED: an optional symbol, a signal, a slot, an `invokeMethod` name, a class name or an object name is missing. NickelHook loads the mod, but one feature can fail.
- FAIL: a required symbol is missing, or the mod has no build for the Qt major version of the firmware. NickelHook does not load the mod.

Below the table, the report names each finding with its source line. Examples are a missing symbol, a changed signature with the new one, a missing signal with its old class, and a missing object name.

`modcheck` finds these names in the mod sources:

| Name | Where in the source | Checked against |
|---|---|---|
| Symbol | `.name` and `.sym` in the NickelHook tables, with `.optional` and `.desc`, and any other quoted `"_Z..."` | `symbols.tsv`. For a missing symbol, it also lists the overloads with the same `Class::method`. |
| Signal, slot | `SIGNAL(...)` and `SLOT(...)` | The signals, or the slots and methods, in `qtmeta.json`, in the classes that have it on the baseline |
| Method | `invokeMethod(obj, "name")` | Any method with that name in `qtmeta.json` |
| Class | `inherits("X")`, `qt_metacast("X")`, `className()` compares, `...ClassName("X")` | The classes in `qtmeta.json` |
| Object name | `findChild<T>("name")`, `findChildren<T>("name")`, `viewWithObjectName(..., "name")` | `strings.txt` |

A name counts only if the baseline firmware has it. A name that the baseline does not have is a Qt name, a name of the mod itself, or a name that is already broken. The report lists these names at the end of each mod section. A required symbol always counts.

For a source that builds for Qt 5 and Qt 6, each column uses the build of its own Qt major and the baseline of that Qt major. `modcheck` evaluates `#if` and `#elif` conditions on `QT_VERSION_MAJOR`, `QT_VERSION` and `QT_VERSION_CHECK`, so a name in a Qt 5 branch is not checked against a Qt 6 firmware.

Limits of the check:

- `strings.txt` holds strings of 6 or more characters. An object name with fewer characters is not checked.
- An object name in `strings.txt` shows only that the library has the string. It does not show that the widget still has that name.
- The check finds only string literals. A name in a variable or a macro is not found.
- The check reads no other preprocessor condition. A name under `#ifdef X` counts for both Qt majors.
- The check does not see Qt style sheet properties, resource paths, object layouts or return types. A device test is still necessary.

## Index a new firmware by hand

`kfw sync` does these steps for you. Use them for a firmware that the API does not offer, for example an older version.

1. Find the firmware URL for the device. Kobo's update API returns the latest one. Replace `0390` with the model number of the device and `4.40.0` with any older version:

   ```bash
   curl -s "https://api.kobobooks.com/1.0/UpgradeCheck/Device/00000000-0000-0000-0000-000000000390/kobo/4.40.0/N0"
   ```

   The `UpgradeURL` field is the firmware file. The device shows its model number in the `X-Kobo-Platformid` and `User-Agent` request headers. It shows its firmware version in Settings > Device information.
2. Download the file. Keep its name: `kfw index` reads the version from it (`kobo-update-X.zip`, `qt6-update-X.zip` or `qt5-qt6-X.zip`). A 4.x file is about 160 MB. A 6.x `qt6-update` file is about 330 MB.
3. Index the file. The Podman machine must run:

   ```bash
   ./kfw index ~/Downloads/kobo-update-4.46.23852.zip
   ```

4. Add a row to [Indexed firmwares](#indexed-firmwares).
5. Delete the firmware file. The index and the rootfs image do not need it.

`kfw index` reads two firmware layouts:

| Firmware | File | Root file system |
|---|---|---|
| 4.x | `kobo-update-X.zip` | `KoboRoot.tgz` in the zip. It is an overlay, not the full system. It has no soname links such as `libc.so.6`, so `kfw` adds them. |
| 5.x, 6.x | `qt6-update-X.zip` | `update.tar` in the zip holds `rootfs.img`, an ext4 image with zstd compression. `kfw` decompresses it, and `debugfs` copies the files out of it. |

## Check one mod against the indexed firmwares

`kfw check` checks one source file or folder and prints the report. It does the same checks as `kfw sync`.

```bash
./kfw check ../projects/nickelsync/src
./kfw check -qt 5,6 ../projects/nickelsync/.claude/worktrees/ns-dual-qt/src
./kfw check -baseline 4.46.23836 ../projects/kpm/hook/src
```

`-qt` gives the Qt majors that the source builds for. The default is `5`. The default baseline is the newest indexed firmware of each Qt major.

For a check against a firmware that is not indexed, use `symcheck` in the kpm repository. It reads the firmware file directly:

```bash
cd ../projects/kpm
go run ./tools/symcheck -src ../nickelsync/src/nickelsync.cc ~/Downloads/kobo-update-4.46.23852.zip
```

## Test an install package

`kfw test` checks a `Kobo.tgz` or `KoboRoot.tgz` against each firmware that has `libs.json.gz`. It reads the package and the index only, so it needs no device and no emulator.

```bash
./kfw test ../projects/nickelsync/KoboRoot.tgz
./kfw test -as kobo -fw 6.0.279225 -out test.md build/nickelsync.tgz
```

The file name tells the install file: a name with `KoboRoot` is a `KoboRoot.tgz`, a name that starts with `Kobo.`, `Kobo-` or `Kobo_` is a `Kobo.tgz`. Use `-as kobo` or `-as koboroot` for other names. Use `-fw` to test only some firmwares, and `-out` to write the report to a file.

The firmware installs the two files differently:

| File in `.kobo/` | 4.x | 5.x, 6.x |
|---|---|---|
| `Kobo.tgz` | Extracted into `/usr/local/Kobo` (rcS) | Each file of the archive that exists in `/usr/local/Kobo` is removed, then the archive is extracted into `/usr/local/Kobo` (`/etc/init.d/ota`) |
| `KoboRoot.tgz` | Extracted into `/` (rcS) | Not read |

For each firmware, `kfw test` reports:

- FAIL: the firmware does not install the file, a path is absolute or has `..`, an ELF file is not 32-bit ARM hard-float, a needed library is missing, an undefined symbol has no export with the same name and version in the libraries that the file loads (for a Nickel plugin, also the libraries of Nickel), or no Qt plugin in the package matches the Qt major version of the firmware.
- WARN: the package replaces a firmware file, a tar entry is not a file, folder or symlink, a file has setuid, a plugin has mangled `_Z` names in its read-only data that libnickel does not have, or a NickelHook plugin has the version `dev`. NickelHook resolves the mangled names with `dlsym`. The binary does not show which are optional, so run `kfw check` on the source for that. A `dev` version comes from a build without `VERSION=<tag>`: the kobuild Qt 6 image has no `git`.
- INFO: Qt skips a plugin of the other Qt major version.

Exit code 1 means a FAIL on a firmware.

`kfw test` reads the Qt version of a plugin from its metadata. Qt reads the same metadata before it loads a plugin, and skips a plugin of another major version without `dlopen`:

| Qt | Plugin metadata |
|---|---|
| 5.0 to 5.12 (firmware 4.x has 5.2.1) | Section `.qtmetadata`: `QTMETADATA  ` and binary JSON |
| 5.13 to 6.1 | Section `.qtmetadata`: `QTMETADATA !`, then the bytes version, Qt major, Qt minor, then CBOR |
| 6.2 and later (firmware 6.0 has 6.5.2) | ELF note `qt-project!` in `.note.qt.metadata`, with the same header |

Limits: the test does not run code. It does not see a crash, a wrong object layout, a changed function behaviour, or a file that the mod needs on the user partition. Use `kfw vm` and a device test for these.

## Run Nickel in an emulator

`kfw vm` boots Nickel of one firmware in an ARMv7 container under qemu-user, installs a package as the firmware boot script does, lets Nickel run, checks the result and removes the container:

```bash
./kfw vm -fw 6.0.279225 -pkg build/Kobo.tgz
./kfw vm -fw 4.46.23852 -pkg ../projects/nickelsync/KoboRoot.tgz -time 5m
```

| Flag | What it does |
|---|---|
| `-fw X.Y.Z` | The firmware. The default is the newest firmware with a rootfs image. |
| `-pkg <file>` | The package to install before Nickel starts. On 6.x, `kfw` does not install a `KoboRoot.tgz`, as the firmware does not. |
| `-as kobo\|koboroot` | The install file of `-pkg`, if its name does not tell it. |
| `-time 3m` | How long Nickel runs before `kfw` checks it. |
| `-name <id>` | The device id. The container is `kfw-vm-<id>`, and the state folder is `vm/<id>/`. The default is the firmware version. Give each test that runs at the same time its own id. |
| `-state <dir>` | Use this state folder, and keep its user partition `onboard/`. Without it, `kfw` makes `vm/<id>/` new. |
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
| `container.log` | The output of the start script. |
| `onboard/` | The user partition, `/mnt/onboard` in the device. |

Exit code 1 means that Nickel stopped, a matching plugin did not load, a plugin of the other Qt version loaded, or `kfw` could not read the 4.x syslog (ERROR in the report).

`vm/<id>.json` records a running device, and it is the lock of the id. A second run with the same id stops with an error. If a run stopped without cleanup, for example after Ctrl+C, run `kfw vm stop <id>`.

## Test across reboots and upgrades

A mod must survive a reboot, its own uninstall and the firmware upgrade from 4.x to 6.x. These subcommands keep one emulated device:

| Command | What it does |
|---|---|
| `kfw vm start -fw X -name <id> [-pkg <file>] [-wait text] [-time 2m]` | Boots the device and leaves it running. With `-wait`, it waits until a syslog line has the text, for example `-wait "done init"`, for at most `-time`. Without `-wait`, it waits `-time`. Exit code 1 if Nickel stopped or the text did not come. |
| `kfw vm exec <id> <command>...` | Runs a shell command in the device and returns its exit code. |
| `kfw vm logs <id>` | Prints the syslog of the current boot: `logread` on 4.x, `syslog.txt` on 6.x. |
| `kfw vm shot <id> [file.png]` | Saves the screen. The default file is `screen-<time>.png` in the state folder. |
| `kfw vm reboot <id> [-wait text] [-time 2m]` | Stops Nickel, keeps the root file system (`podman commit` to `localhost/kfw-vm-<id>:boot<n>`), and boots again. The boot installs a `.kobo/Kobo.tgz` that the user or a mod left on the user partition, and on 4.x also a `.kobo/KoboRoot.tgz`, as the firmware does. |
| `kfw vm upgrade <id> <version> [-wait text] [-time 2m]` | Boots a clean root file system of `<version>` with the same user partition, as a firmware update does. The first boot installs a `.kobo/Kobo.tgz`. |
| `kfw vm stop <id>` | Removes the container, the images of its reboots and the lock. The state folder stays. |

For example:

```bash
./kfw vm start -fw 4.46.23852 -name ns -pkg old/KoboRoot.tgz -wait "done init"
cp build/Kobo.tgz vm/ns/onboard/.kobo/Kobo.tgz
./kfw vm reboot ns -wait "done init"
./kfw vm upgrade ns 6.0.279225
./kfw vm exec ns 'ls /usr/local/Kobo/imageformats'
./kfw vm stop ns
```

`examples/upgrade-test.sh` runs the full path: the old package on 4.x, the new package, the upgrade to 6.x, the package again, and the uninstall.

## How the emulator works

The design follows the Nickel VM of NickelInkSync PR 27 (github.com/dkuester/NickelInkSync, MIT license, see `THIRD_PARTY_NOTICES`):

- The root file system is the image `localhost/kfw-rootfs:<version>`.
- `/dev/fb0` is a file. `vm/vmshim.c`, preloaded into Nickel, answers the framebuffer ioctls and accepts the e-ink update ioctls. `kfw` builds it with the toolchain image.
- A file stands in for the NTX hardware configuration partition (board of the Clara Colour, brand Kobo). A tmpfs fakes a battery at 80 %.
- For 4.x, `init.sh` does what the 4.x rcS and the base rootfs do: it links `/dev/mmcblk0p6` (the hardware configuration) and a blank `/dev/mmcblk0`, sets `PLATFORM` and `PRODUCT` with the firmware tools `ntx_hwconfig` and `kobo_config.sh` (without them Nickel 4.x takes an old Freescale device, finds no battery and powers off), makes `/tmp` and `/var`, and writes open D-Bus configs. The shim drops `O_TRUNC` when Nickel opens a disk file.
- `poweroff`, `reboot`, USB export, `wpa_supplicant` and `dhcpcd` are stubs. There is no Wi-Fi, touch input or sync.
- Nickel is process 1 of the container. So the container runs while Nickel runs.

State on 2026-11-18:

| Firmware | Result |
|---|---|
| 6.0.279225 | Works. Nickel boots to the language screen in about 30 s. Qt loads a matching plugin from `/usr/local/Kobo/imageformats` and refuses a Qt 5 plugin. |
| 4.46.23852 | Works. Nickel boots to the language screen in about 30 s, but without fonts: the 4.x KoboRoot has none, the base rootfs of the device has them. Qt loads a matching plugin and refuses a Qt 6 plugin. NickelHook mods initialize. |

### ARM emulation in the Podman machine

The container engine must run ARM programs. The Podman machine has `qemu-user-static`, but under WSL the ARM handler is often not registered. All WSL distros share one `binfmt_misc`, and `systemd-binfmt` in another distro removes all handlers every few minutes, also during a run.

`kfw vm` handles this for Podman:

- Before each boot and each `exec`, it checks that an ARM program runs. If not, it registers the qemu-arm handler in the Podman machine. If `/proc/sys/fs/binfmt_misc` is hidden by a systemd automount, it mounts it again first.
- It starts a loop in the Podman machine that registers the handler again each second (`/tmp/kfw-binfmt.sh`, PID in `/tmp/kfw-binfmt.pid`). The loop stops when no `kfw-vm-` container runs.

If this fails, run the commands yourself:

```bash
podman machine ssh "sh -c 'cat /usr/lib/binfmt.d/qemu-arm-static.conf > /proc/sys/fs/binfmt_misc/register'"
podman machine ssh 'umount -l /proc/sys/fs/binfmt_misc; mount -t binfmt_misc binfmt_misc /proc/sys/fs/binfmt_misc'
```

`podman machine ssh` from Git Bash writes a file `NUL` with the host key of the machine into the current folder. Delete it. `kfw` runs the command in the temp folder.

### Differences from a device

- The emulator runs several times slower than the device. It has no e-ink, Wi-Fi or touch. It shows that Nickel starts and that the plugin loads. It does not replace a device test.
- `/mnt/onboard` is a bind mount of a Windows folder (9p). Its inode numbers are larger than 32 bits, so a 32-bit `stat()` in a mod fails with `EOVERFLOW`. A device (FAT) does not have this problem. In mod code, use `open(O_DIRECTORY)`, `access()` or `stat64()`.
- `df /` shows the container overlay, not the free space of the firmware root file system.
- On 6.0, busybox `pidof nickel` returns nothing, because Nickel is process 1. Use `ps | grep [n]ickel`.
- The 6.0 busybox is version 1.35. Its `head` needs `-n 4`, not `-4`.

## Compare two firmwares

List the symbols that one firmware has and the other does not. Column 5 is the mangled name.

```bash
comm -3 <(cut -f5 firmware/4.45.23697/symbols.tsv | sort) <(cut -f5 firmware/4.46.23852/symbols.tsv | sort)
```

Lines with no indent exist only in the first firmware. Lines with one tab exist only in the second firmware. Compare `qtmeta.json` the same way to find changed signals and slots.

## Search the index

- Find a function and its mangled name: `grep -P '\tReadingView::saveBookmark' firmware/4.46.23852/symbols.tsv`
- Find who calls a function: `grep -P '\tReadingView::sendBookmark\(Volume const&\)$' firmware/4.46.23852/calls.tsv`
- Find what a function calls: `grep -P '^ReadingView::leavingReadingExperience\(\)\t' firmware/4.46.23852/calls.tsv`
- List the signals and slots of a class: `python3 -c "import json; print(json.dumps(json.load(open('firmware/4.46.23852/qtmeta.json'))['ReadingView'], indent=1))"`
- Find the class that shows a UI text: search `strings.txt`. The strings of one class are often next to each other.

## Limits of the index

- `calls.tsv` holds direct calls only. It does not show virtual calls, calls through function pointers, or signal and slot connections. A function that the index shows with no caller can still run.
- The library exports only some of its functions. The disassembler gives the code of a function that is not exported the name of the exported function before it. So a caller name in `calls.tsv` can be wrong for internal code.
- A mangled name does not include the return type. Read the disassembly to find it. For example, `ReadingView::getVolume() const` returns a reference: its code is `this + 116`.
- `qtmeta.json` shows only classes with the `Q_OBJECT` macro. A plain C++ class, such as `ConfirmationDialogFactory`, is only in `symbols.tsv`.

## Read the code of one function

The index does not include disassembly. To read one function, extract `libnickel` from the firmware and disassemble the address range from `symbols.tsv`. Thumb code has bit 0 of the address set. Clear it before you use the address. In Git Bash, set `MSYS_NO_PATHCONV=1`, or Git Bash changes the `/w` path.

```bash
MSYS_NO_PATHCONV=1 podman run --rm -v "$PWD":/w -w /w ghcr.io/pgaskin/nickeltc:1.0 sh -c '
  unzip -p kobo-update-4.46.23852.zip KoboRoot.tgz | tar xz ./usr/local/Kobo/libnickel.so.1.0.0
  arm-nickel-linux-gnueabihf-objdump -d -C --no-show-raw-insn \
    --start-address=0xf96204 --stop-address=0xf96230 usr/local/Kobo/libnickel.so.1.0.0'
```

A function whose only call is `QMetaObject::activate` is a Qt signal.

## Call Nickel safely from a mod

Prefer the Qt meta-object system to raw function pointers:

- Call a slot or a signal by name with `QMetaObject::invokeMethod(object, "name")`. Qt checks the name at run time. A missing method returns `false` and does not crash.
- Connect to a signal with `QObject::connect(object, SIGNAL(name(Type)), receiver, SLOT(...))`. A missing signal returns `false`.
- Find an object of a Nickel class with `QObject::inherits("ClassName")` on the children of a view.

A raw function pointer needs the exact C++ signature, including the return type and the object layout. A wrong guess can crash Nickel. Use a raw pointer only when no Qt method exists, and check its code first.
