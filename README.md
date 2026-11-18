# kfw: Kobo firmware tools for Nickel mods

`kfw` tests Nickel mods against Kobo firmware without a device. It does these jobs:

- `kfw sync` asks the Kobo update API for new firmware, downloads it and indexes `libnickel.so.1.0.0`: symbols, direct calls, Qt signals and slots, strings and the libraries of the firmware.
- `kfw check` checks the Nickel names in mod sources against each indexed firmware.
- `kfw test` checks a `Kobo.tgz` or `KoboRoot.tgz` against the libraries and the install rules of each firmware.
- `kfw vm` boots Nickel of a firmware in an ARM container with a package installed, and keeps the emulated device across reboots, an uninstall and the firmware upgrade from 4.x to 6.x.

Nickel is the Kobo reading app. Mods such as NickelMenu, NickelDBus, kpm and NickelSync load into it as Qt plugins (through NickelHook) and call its functions by symbol name. A firmware update can rename or remove a function. Firmware 6.x moves from Qt 5 to Qt 6, and a Qt 5 mod does not load on it.

## Background

`kfw` started in November 2026 as shell scripts that indexed `libnickel` to design and check two mods: NickelSync (a "Sync now" item and reading position sync) and the NickelHook UI of kpm (a package manager for Kobo). Then Kobo offered the firmware 6.0 beta to the Libra Colour, Clara BW and Clara Colour. It runs Qt 6, so neither mod loaded.

The scripts became one Go tool to answer three questions without a device:

1. Which Nickel names did 6.0 change? (`kfw check`)
2. Will this package install and link on each firmware? (`kfw test`)
3. Does Nickel load the right build, and does the mod survive a reboot and the upgrade to 6.x? (`kfw vm`)

The answers made one `Kobo.tgz` with a Qt 5 and a Qt 6 build possible. [docs/firmware-6.md](docs/firmware-6.md) has the findings. The NickelSync and kpm ports then used `kfw vm` and a separate test harness for reboot and upgrade tests. That harness became `kfw vm start`, `reboot` and `upgrade`.

The emulator design comes from the Nickel VM of NickelInkSync PR 27 by dkuester (github.com/dkuester/NickelInkSync, MIT license). See `THIRD_PARTY_NOTICES`.

## Set up

1. Install Go 1.27 or later, and Podman (or Docker).
2. Start the Podman machine: `podman machine start`.
3. Build `kfw` in this folder: `go -C tools build -o .. ./kfw`.
4. On Windows (WSL), make ARM emulation permanent once: `./kfw vm setup`. See [docs/vm.md](docs/vm.md#arm-emulation-in-the-podman-machine).
5. Get firmware: `./kfw sync`. It downloads the newest firmware for the devices in `devices.tsv` (about 160 MB for 4.x and 330 MB for 6.x) and indexes it.

`kfw` finds its folder by the file `devices.tsv`, so run it from this folder or a subfolder. It pulls the image `ghcr.io/pgaskin/nickeltc:1.0` on first use (ARM binutils, `debugfs`, and the GCC for the emulator shim).

To test `kfw` itself, run `go -C tools test ./...`.

## Quick start

```bash
./kfw check -qt 5,6 ../mymod/src                        # Nickel names, each Qt build
./kfw test build/Kobo.tgz                                 # install, link and Qt checks
./kfw vm -fw 6.0.279225 -pkg build/Kobo.tgz              # boot Nickel with the package
sh examples/upgrade-test.sh old/KoboRoot.tgz build/Kobo.tgz .adds/mymod/uninstall
```

## Documentation

| Read | When |
|---|---|
| [docs/index.md](docs/index.md) | You get or index firmware (`kfw sync`, `kfw index`), search the index, compare firmwares, or read the code of a Nickel function. |
| [docs/check.md](docs/check.md) | You check mod sources (`kfw check`, `mods.tsv`) or read `report.md`. |
| [docs/test.md](docs/test.md) | You test a `Kobo.tgz` or `KoboRoot.tgz` (`kfw test`). |
| [docs/vm.md](docs/vm.md) | You run Nickel in the emulator, test reboots, uninstalls and upgrades, use a FAT user partition or the fake Forgejo, or set up ARM emulation. |
| [docs/firmware-6.md](docs/firmware-6.md) | You make a mod work on firmware 4.x (Qt 5) and 6.x (Qt 6). |

## Commands

| Command | What it does |
|---|---|
| `kfw sync [-offline] [-keep]` | Gets new firmware, indexes it, checks the mods in `mods.tsv`, writes `report.md`. |
| `kfw index [-libs] <firmware.zip>\|<version>` | Indexes one firmware file, or indexes again from its rootfs image. |
| `kfw check [-qt 5\|6\|5,6] [-baseline X.Y.Z[,X.Y.Z]] [-out file] <source>...` | Checks mod sources against each firmware. |
| `kfw test [-as kobo\|koboroot] [-fw X.Y.Z,...] [-out file] <package>` | Checks an install package against each firmware. |
| `kfw vm [flags]` | One emulator run: boot, check, report, remove. |
| `kfw vm start\|exec\|cp\|logs\|shot\|reboot\|upgrade\|forge\|stop` | One emulated device across boots. |
| `kfw vm setup [-remove]` | Keeps ARM emulation registered in the Podman machine. |
| `kfw qtmeta <library>` | Prints the Qt signals, slots and methods of a library as JSON. |

## Layout

| Path | What it is |
|---|---|
| `devices.tsv` | The devices that `kfw sync` asks Kobo about. |
| `mods.tsv` | The mods that `kfw sync` checks. See [docs/check.md](docs/check.md#modstsv). |
| `tools/kfw/` | The source of `kfw`: `sync.go` (update API, downloads), `index.go` (rootfs image, index), `test.go` (package test), `vm.go`, `vmextra.go` and `vm/` (emulator). |
| `tools/modcheck/` | The source check and the report. `qtpp.go` evaluates `#if QT_VERSION_MAJOR`. |
| `tools/qtmeta/` | Reads the Qt meta data out of a library (Qt 5 and Qt 6). |
| `tools/fakeforge/` | The fake Forgejo of `kfw vm forge`. |
| `examples/` | Test scripts that use `kfw`. |
| `docs/` | The documentation. |

`kfw` writes these local folders and files. They hold Kobo firmware or data from it, so they stay out of the repository (`.gitignore`). Do not publish them:

| Path | What it is |
|---|---|
| `firmware/<version>/` | The index of one firmware. |
| `downloads/` | Firmware files while `kfw sync` indexes them. |
| `cache/vm/` | The emulator helpers: `init.sh`, `stub.sh`, `vmshim.so`, `fakeforge`. |
| `vm/<id>/`, `vm/<id>.json` | The state folder and the record of one emulated device. |
| `report.md`, `available.tsv` | The last check report and the last answers of the update API. |
| `localhost/kfw-rootfs:<version>` | The rootfs image of each firmware, in the container engine. |

`kfw sync` sends one request for each device and channel, and only when you start it. Do not schedule it: the Kobo Terms of Use do not allow a robot that monitors the service.
