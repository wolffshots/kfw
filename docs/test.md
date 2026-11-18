# Test an install package: kfw test

`kfw test` checks a `Kobo.tgz` or `KoboRoot.tgz` against each firmware that has a library index (`libs.json.gz`). It reads the package and the index only, so it needs no device and no emulator.

```bash
./kfw test ../nickelsync/KoboRoot.tgz
./kfw test -as kobo -fw 6.0.279225 -out test.md build/nickelsync.tgz
```

| Flag | What it does |
|---|---|
| `-as kobo\|koboroot` | The install file. The default comes from the file name: a name with `KoboRoot` is a `KoboRoot.tgz`, a name that starts with `Kobo.`, `Kobo-` or `Kobo_` is a `Kobo.tgz`. |
| `-fw X.Y.Z,...` | Test only these firmwares. |
| `-out <file>` | Write the report to a file. |

Exit code 1 means a FAIL on a firmware.

## How the firmware installs a package

The firmware reads two files in `.kobo/` on the user partition at boot:

| File in `.kobo/` | 4.x | 5.x, 6.x |
|---|---|---|
| `Kobo.tgz` | Extracted into `/usr/local/Kobo` (rcS) | Each file of the archive that exists in `/usr/local/Kobo` is removed, then the archive is extracted into `/usr/local/Kobo` (`/etc/init.d/ota`) |
| `KoboRoot.tgz` | Extracted into `/` (rcS) | Not read |

So `Kobo.tgz` is the only file that both firmware lines install. See [firmware-6.md](firmware-6.md).

## What the test reports

For each firmware:

- FAIL: the firmware does not install the file, a path is absolute or has `..`, an ELF file is not 32-bit ARM hard-float, a needed library is missing, an undefined symbol has no export with the same name and version in the libraries that the file loads (for a Nickel plugin, also the libraries of Nickel), or no Qt plugin in the package matches the Qt major version of the firmware.
- WARN: the package replaces a firmware file, a tar entry is not a file, folder or symlink, a file has setuid, a plugin has mangled `_Z` names in its read-only data that libnickel does not have, or a NickelHook plugin has the version `dev`.
- INFO: Qt skips a plugin of the other Qt major version.

NickelHook resolves the mangled names with `dlsym`. The binary does not show which names are optional, so run `kfw check` on the source for that.

A `dev` version comes from a NickelHook build without `VERSION=<tag>`. For example, the kobuild Qt 6 image has no `git`, so `NickelHook.mk` finds no version.

## How Qt picks a plugin

`kfw test` reads the Qt version of a plugin from its metadata. Qt reads the same metadata before it loads a plugin, and skips a plugin of another major version without `dlopen`:

| Qt | Plugin metadata |
|---|---|
| 5.0 to 5.12 (firmware 4.x has 5.2.1) | Section `.qtmetadata`: `QTMETADATA  ` and binary JSON |
| 5.13 to 6.1 | Section `.qtmetadata`: `QTMETADATA !`, then the bytes version, Qt major, Qt minor, then CBOR |
| 6.2 and later (firmware 6.0 has 6.5.2) | ELF note `qt-project!` in `.note.qt.metadata`, with the same header |

Qt 6 also refuses a plugin of a newer minor version. `kfw test` reports that as a FAIL.

## Limits

The test does not run code. It does not see a crash, a wrong object layout, a changed function behaviour, or a file that the mod needs on the user partition. Use `kfw vm` ([vm.md](vm.md)) and a device test for these.
