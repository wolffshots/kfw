# The firmware index

`kfw` keeps an index of `libnickel.so.1.0.0` for each firmware. Use it to design a Nickel mod, to check a mod against a firmware, and to compare firmwares.

## Get new firmware: kfw sync

```bash
./kfw sync
```

The command does these steps:

1. It asks the Kobo update API for the newest firmware of each device in `devices.tsv`, on three channels. See the next table.
2. It writes the answers to `available.tsv`. It accepts only files on `https://ereaderfiles.kobo.com/`.
3. For each version that has no `libs.json.gz` in `firmware/`, it downloads the file to `downloads/<version>/`, runs `kfw index`, and deletes the file.
4. It checks each mod in `mods.tsv` and writes `report.md`. It prints the table of the report. See [check.md](check.md).

| Channel | Request | Answer |
|---|---|---|
| stable | Version `4.0.0` | The newest 4.x, `kobo-update-X.zip` |
| beta | Version `6.0.0` | The newest 6.x, `qt6-update-X.zip` |
| preview | The newest stable version, with the header `X-Kobo-Accept-Preview: true` | The 4.x to 6.x upgrade that an opted-in device gets, `qt5-qt6-X.zip` (577 MB for 6.0.279225) |

If one version comes in several files, the command takes the smallest kind: `kobo-update`, then `qt6-update`, then `qt5-qt6`.

Options:

- `-offline`: ask Kobo nothing and download nothing. Only run the check.
- `-keep`: keep the firmware files in `downloads/`.

Exit code 1 means that a mod with role `check` fails on a firmware.

The command sends one request for each device and channel, and it runs only when you start it. Do not schedule it. The Kobo Terms of Use do not allow a robot that monitors the service.

To track one more device, add its model number to `devices.tsv`. The model numbers are in `kobo/device.go` of github.com/pgaskin/koboutils. For example, the Libra Colour is 390, the Clara BW 391, the Clara Colour 393, the Elipsa 2E 389 and the Libra 2 388.

## Index a firmware file by hand

Use these steps for a firmware that the API does not offer, for example an older version.

1. Find the firmware URL for the device. Replace `0390` with the model number of the device and `4.40.0` with any older version:

   ```bash
   curl -s "https://api.kobobooks.com/1.0/UpgradeCheck/Device/00000000-0000-0000-0000-000000000390/kobo/4.40.0/N0"
   ```

   The `UpgradeURL` field is the firmware file. The device shows its model number in the `X-Kobo-Platformid` and `User-Agent` request headers. It shows its firmware version in Settings > Device information.
2. Download the file. Keep its name: `kfw index` reads the version from it (`kobo-update-X.zip`, `qt6-update-X.zip` or `qt5-qt6-X.zip`). A 4.x file is about 160 MB. A 6.x `qt6-update` file is about 330 MB.
3. Index the file. The Podman machine must run:

   ```bash
   ./kfw index ~/Downloads/kobo-update-4.46.23852.zip
   ```

4. Delete the firmware file. The index and the rootfs image do not need it.

`kfw index` reads two firmware layouts:

| Firmware | File | Root file system |
|---|---|---|
| 4.x | `kobo-update-X.zip` | `KoboRoot.tgz` in the zip. It is an overlay, not the full system. It has no soname links such as `libc.so.6`, so `kfw` adds them. |
| 5.x, 6.x | `qt6-update-X.zip` | `update.tar` in the zip holds `rootfs.img`, an ext4 image with zstd compression. `kfw` decompresses it, and `debugfs` copies the files out of it. |

`kfw index` keeps the root file system as the container image `localhost/kfw-rootfs:<version>`. `kfw vm` and `kfw index <version>` need it. An image is 0.3 GB (4.x) to 0.7 GB (6.x). Remove one with `podman rmi localhost/kfw-rootfs:<version>`. To make it again, run `kfw index -libs <firmware.zip>`.

`kfw index -libs` writes only `libs.json.gz` and `files.txt` and keeps the libnickel index. It takes about 30 seconds. Without `-libs`, `kfw index` also reads all of libnickel, which takes 15 minutes (4.x) to 60 minutes (6.x).

## The files of one firmware

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

The Clara BW and Clara Colour (`kobo12`) get a different 4.46.23852 file than the Libra Colour (`kobo13`), but its `libnickel` has the same SHA-256. So the index has one folder for each version, not for each device.

## Search the index

- Find a function and its mangled name: `grep -P '\tReadingView::saveBookmark' firmware/4.46.23852/symbols.tsv`
- Find who calls a function: `grep -P '\tReadingView::sendBookmark\(Volume const&\)$' firmware/4.46.23852/calls.tsv`
- Find what a function calls: `grep -P '^ReadingView::leavingReadingExperience\(\)\t' firmware/4.46.23852/calls.tsv`
- List the signals and slots of a class: `python3 -c "import json; print(json.dumps(json.load(open('firmware/4.46.23852/qtmeta.json'))['ReadingView'], indent=1))"`
- Find the class that shows a UI text: search `strings.txt`. The strings of one class are often next to each other.
- Print the Qt meta data of any library: `./kfw qtmeta <libnickel.so.1.0.0>`.

## Compare two firmwares

List the symbols that one firmware has and the other does not. Column 5 is the mangled name.

```bash
comm -3 <(cut -f5 firmware/4.45.23697/symbols.tsv | sort) <(cut -f5 firmware/4.46.23852/symbols.tsv | sort)
```

Lines with no indent exist only in the first firmware. Lines with one tab exist only in the second firmware. Compare `qtmeta.json` the same way to find changed signals and slots.

## Read the code of one function

The index does not include disassembly. To read one function, extract `libnickel` from the firmware and disassemble the address range from `symbols.tsv`. Thumb code has bit 0 of the address set. Clear it before you use the address. In Git Bash, set `MSYS_NO_PATHCONV=1`, or Git Bash changes the `/w` path.

```bash
MSYS_NO_PATHCONV=1 podman run --rm -v "$PWD":/w -w /w ghcr.io/pgaskin/nickeltc:1.0 sh -c '
  unzip -p kobo-update-4.46.23852.zip KoboRoot.tgz | tar xz ./usr/local/Kobo/libnickel.so.1.0.0
  arm-nickel-linux-gnueabihf-objdump -d -C --no-show-raw-insn \
    --start-address=0xf96204 --stop-address=0xf96230 usr/local/Kobo/libnickel.so.1.0.0'
```

A function whose only call is `QMetaObject::activate` is a Qt signal.

## Limits of the index

- `calls.tsv` holds direct calls only. It does not show virtual calls, calls through function pointers, or signal and slot connections. A function that the index shows with no caller can still run.
- The library exports only some of its functions. The disassembler gives the code of a function that is not exported the name of the exported function before it. So a caller name in `calls.tsv` can be wrong for internal code.
- A mangled name does not include the return type. Read the disassembly to find it. For example, `ReadingView::getVolume() const` returns a reference: its code is `this + 116`.
- `qtmeta.json` shows only classes with the `Q_OBJECT` macro. A plain C++ class, such as `ConfirmationDialogFactory`, is only in `symbols.tsv`.

## Call Nickel safely from a mod

Prefer the Qt meta-object system to raw function pointers:

- Call a slot or a signal by name with `QMetaObject::invokeMethod(object, "name")`. Qt checks the name at run time. A missing method returns `false` and does not crash.
- Connect to a signal with `QObject::connect(object, SIGNAL(name(Type)), receiver, SLOT(...))`. A missing signal returns `false`.
- Find an object of a Nickel class with `QObject::inherits("ClassName")` on the children of a view.

A raw function pointer needs the exact C++ signature, including the return type and the object layout. A wrong guess can crash Nickel. Use a raw pointer only when no Qt method exists, and check its code first.
