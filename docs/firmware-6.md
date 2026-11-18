# Nickel mods on firmware 4.x and 6.x

Kobo firmware 6.x moves Nickel from Qt 5 to Qt 6. A NickelHook mod built for 4.x does not load on it. This page explains how one mod package works on both, from research and emulator tests of November 2026 on firmware 4.46.23852 and the beta 6.0.279225. NickelSync and kpm ship this way.

## Short answer

One binary cannot work on both. One install package can.

A NickelHook mod is a Qt plugin. It links against `libQt5Core.so.5` or `libQt6Core.so.6`, and Qt 6 changed the binary layout of core classes such as `QString` and `QList`. So the mod needs two builds from one source: a Qt 5 build and a Qt 6 build.

Both firmware lines install `/mnt/onboard/.kobo/Kobo.tgz` into `/usr/local/Kobo`. So one `Kobo.tgz` can hold both builds:

```
Kobo.tgz
  imageformats/libmymod.so       Qt 5 build, loads on 4.x
  imageformats/libmymod-qt6.so   Qt 6 build, loads on 6.x
```

On each firmware, Qt loads the build of its own major version and skips the other one without `dlopen`. `kfw test` and `kfw vm` show this in both directions.

## The 6.x firmware

- "Kobo OS 6 Beta" is opt-in, with a staged rollout, for the Libra Colour, Clara BW and Clara Colour. Kobo warns that a rollback from the beta deletes data that is only on the device.
- Qt 6.5.2, glibc symbols up to `GLIBC_2.35`, libstdc++ up to `GLIBCXX_3.4.29`.
- A Yocto system. `/etc/init.d/ota` applies `Kobo.tgz` and `update.tar`. The root file system is writable (`ROOTFS_READ_ONLY=no` in `/etc/default/rcS`).
- The update API offers it on the beta channel (`qt6-update-X.zip`), and offers the 4.x to 6.x upgrade on the preview channel (`qt5-qt6-X.zip`). See [index.md](index.md).

## Install files

| File in `.kobo/` | 4.46.23852 | 6.0.279225 |
|---|---|---|
| `Kobo.tgz` | `/etc/init.d/rcS`: extracted into `/usr/local/Kobo` when `pickel can-upgrade` allows it | `/etc/init.d/ota`: each file of the archive that exists in `/usr/local/Kobo` is removed, then the archive is extracted into `/usr/local/Kobo` |
| `KoboRoot.tgz` | `/etc/init.d/rcS`: extracted into `/` | Not read. No file of the 6.0 rootfs names it. |
| `update.tar` | Not read | The firmware update |

So `Kobo.tgz` is the only file that both firmware lines install. It can place files only under `/usr/local/Kobo`. Mods for 4.x often ship files on the user partition in `KoboRoot.tgz`, for example a NickelMenu entry in `/mnt/onboard/.adds/nm/` or a NickelHook uninstall flag. A mod in a `Kobo.tgz` must write these files itself at the first start. See [Uninstall and the first start](#uninstall-and-the-first-start).

## Plugin loading

- Both firmware lines start Nickel from `/usr/local/Kobo`. Qt adds the folder of the executable to its plugin paths, so both scan `/usr/local/Kobo/imageformats`. 6.0 has no such folder, but Qt scans it when it exists.
- Qt reads the metadata of a plugin from the file before it loads it, and refuses a plugin of another major version:
  - Qt 5.2.1 (4.x) looks for `QTMETADATA  ` in the `.qtmetadata` section. A Qt 6 plugin has an ELF note `qt-project!` instead, so Qt 5 logs "Plugin verification data mismatch" and does not load the file.
  - Qt 6.5.2 (6.0) reads the ELF note `.note.qt.metadata`. A Qt 5 plugin has no such note, so Qt 6 logs ".qtmetadata section has incorrect magic" and does not load the file.
- Qt 6 also refuses a plugin of a newer minor version. Build against Qt 6.5 or older for firmware 6.0.
- Qt loads only one plugin for each plugin key. Give the two builds different keys (NickelHook uses the library name).

## Build

| | Firmware 4.x | Firmware 6.0 |
|---|---|---|
| Qt | 5.2.1 | 6.5.2 |
| Newest glibc symbol version | `GLIBC_2.18` | `GLIBC_2.35` |
| Newest libstdc++ symbol version | `GLIBCXX_3.4.20` | `GLIBCXX_3.4.29` |
| Toolchain | `ghcr.io/pgaskin/nickeltc:1.0` (GCC 4.9.4, prefix `arm-nickel-linux-gnueabihf-`), or `ghcr.io/nicoverbruggen/kobuild:qt5-v0.5.3` | `ghcr.io/nicoverbruggen/kobuild:qt6-v0.5.3` (prefix `arm-kobo-linux-gnueabihf-`, C++17) |
| NickelHook | Upstream `pgaskin/NickelHook` | The fork `nicoverbruggen/NickelHook`, branch `qt6-support`. It also builds Qt 5. |

Pin the kobuild images by digest. `kfw test` checks the glibc and libstdc++ versions of each build against each firmware.

The NickelHook fork:

- `NH_QT_MAJOR=5` or `6` selects the Qt `pkg-config` names, the compiler prefix and the C++ standard. Set `PKGCONF += Qt$(NH_QT_MAJOR)Widgets` in the mod Makefile.
- `make koboroot` (Qt 5 only) writes `KoboRoot.tgz`. `make kobo` (Qt 6 only) writes `Kobo.tgz` with only `imageformats/$(LIBRARY)`. Neither target makes a package with both builds, so a dual package needs its own packaging step.
- `resources.c` with `struct nh_resources` writes files that are embedded in the plugin to a config folder once, before NickelHook checks the uninstall flags.
- The kobuild Qt 6 image has no `git`, so `NickelHook.mk` finds no version and the plugin logs `dev`. Pass `VERSION=<tag>` to `make`. `kfw test` warns about a `dev` build.

## Source

- Keep one source and use `#if QT_VERSION_MAJOR >= 6` where the Qt API differs. For example, `QLabel::pixmap()` returns a value in Qt 6.
- Nickel names differ between 4.x and 6.0. `kfw check -qt 5,6` checks each build against its own firmware and reads the `#if` branches. See [check.md](check.md). Examples on 6.0:
  - The reader class `KepubBookReaderBase` is gone. The 6.0 reader is a `PageImageReader`, and its `onResume()` does not request the reading position. No replacement was found in the direct calls.
  - `Content::getId() const` is now `Content::getId(QString const&) const`, and the `ReadingMenuView` constructor is now `(QWidget*, bool, bool)`.
- Make each name that differs an optional NickelHook symbol, and check for it at run time.

## Uninstall and the first start

- A NickelHook uninstall flag that must exist (`!flag`) uninstalls the mod at the first boot, because a `Kobo.tgz` cannot ship the flag file. Use a flag whose presence means uninstall, or create the file before NickelHook checks it.
- NickelHook checks the uninstall flags in `nh_init`, an ELF constructor without a priority. The `init` function of the mod runs later, so it cannot create the flag file in time. A constructor with a priority, for example `__attribute__((constructor(200)))`, runs before `nh_init` in the same library. Use only plain C file calls there, no Qt and no `nh_*` helpers. Check the order with `readelf` on `.init_array`.
- A seed constructor writes the first-start files (the flag file, a NickelMenu entry) once, and then a receipt file. With the receipt present, a missing flag file means uninstall again. NickelSync does this.
- NickelHook deletes the library of the build that runs. Make the uninstall code of the mod also delete the other build. Otherwise the other build stays in `/usr/local/Kobo/imageformats`.
- The NickelHook fork crashes in every uninstall of a mod that does not link `resources.c`: its hidden weak `nh_resources_uninstalled` resolves to the load address, not to null. Define that function in the mod.

## Firmware updates

- A 6.x firmware update writes a new root file system: `/etc/init.d/ota` runs `driver.sh` from `update.tar`, which writes `rootfs.img` over `system_a` with `dd`. So the update removes the mod from `/usr/local/Kobo`, and the user installs `Kobo.tgz` again. The user partition stays.
- The 4.x to 6.x upgrade (`qt5-qt6-X.zip`) does the same.
- A package manager must know the firmware line: 6.x does not read `KoboRoot.tgz`, so it must stage `Kobo.tgz` or write files directly. kpm reads `.kobo/version` for this.
- `kfw vm upgrade` tests this path in the emulator. A device test is still necessary.
