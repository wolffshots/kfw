# Check mod sources: kfw check

`kfw check` reads the source of a mod, finds the Nickel names that it uses, and checks each name against each indexed firmware. `kfw sync` runs the same check for each mod in `mods.tsv` and writes `report.md`.

```bash
./kfw check ../nickelsync/src
./kfw check -qt 5,6 ../nickelsync/src
./kfw check -baseline 4.46.23836 -out report.md ../kpm/hook/src
```

| Flag | What it does |
|---|---|
| `-qt 5\|6\|5,6` | The Qt majors that the source builds for. The default is `5`. |
| `-baseline X.Y.Z[,X.Y.Z]` | The firmware that the mod was tested on, one for each Qt major. The default is the newest indexed firmware of each Qt major. |
| `-out <file>` | Write the report to a file. |

## mods.tsv

One row for each mod that `kfw sync` checks. Columns, separated by one tab:

| Column | Content |
|---|---|
| `name` | The column title in the report. |
| `sources` | Source folders or files, relative to `mods.tsv`, separated by commas. |
| `qt` | `5`, `6`, or `5,6` for a source that builds for both. |
| `baseline` | The firmware that the mod was tested on, one for each Qt major (`4.46.23852,6.0.279225`), or `-` for the newest of each Qt major. |
| `role` | `check`: a FAIL sets exit code 1. `info`: the report shows the mod, but it does not change the exit code. |

## What the check finds

| Name | Where in the source | Checked against |
|---|---|---|
| Symbol | `.name` and `.sym` in the NickelHook tables, with `.optional` and `.desc`, and any other quoted `"_Z..."` | `symbols.tsv`. For a missing symbol, it also lists the overloads with the same `Class::method`. |
| Signal, slot | `SIGNAL(...)` and `SLOT(...)` | The signals, or the slots and methods, in `qtmeta.json`, in the classes that have it on the baseline |
| Method | `invokeMethod(obj, "name")` | Any method with that name in `qtmeta.json` |
| Class | `inherits("X")`, `qt_metacast("X")`, `className()` compares, `...ClassName("X")` | The classes in `qtmeta.json` |
| Object name | `findChild<T>("name")`, `findChildren<T>("name")`, `viewWithObjectName(..., "name")` | `strings.txt` |

A name counts only if the baseline firmware has it. A name that the baseline does not have is a Qt name, a name of the mod itself, or a name that is already broken. The report lists these names at the end of each mod section. A required symbol always counts.

## A source for Qt 5 and Qt 6

Firmware 4.x has Qt 5 and firmware 6.x has Qt 6. A mod for both has one source and two builds. See [firmware-6.md](firmware-6.md).

With `-qt 5,6`, each firmware column uses the build of its own Qt major and the baseline of that Qt major. `kfw check` evaluates `#if` and `#elif` conditions on `QT_VERSION_MAJOR`, `QT_VERSION` and `QT_VERSION_CHECK`. So a name in a Qt 5 branch is not checked against a Qt 6 firmware:

```cpp
#if QT_VERSION_MAJOR < 6
    if (o->inherits("KepubBookReaderBase")) ...   // checked on 4.x only
#endif
```

A mod that builds only for Qt 5 gets a FAIL on each Qt 6 firmware ("Qt ABI"), because Qt 6 does not load a Qt 5 plugin.

## Read the report

The report has one table: one row for each firmware and one column for each mod. A cell is one of:

- OK: the firmware has every Nickel name that the mod uses on its baseline.
- DEGRADED: an optional symbol, a signal, a slot, an `invokeMethod` name, a class name or an object name is missing. NickelHook loads the mod, but one feature can fail.
- FAIL: a required symbol is missing, or the mod has no build for the Qt major version of the firmware. NickelHook does not load the mod.

Below the table, the report names each finding with its source line. Examples are a missing symbol, a changed signature with the new one, a missing signal with its old class, and a missing object name.

## Limits

- `strings.txt` holds strings of 6 or more characters. An object name with fewer characters is not checked.
- An object name in `strings.txt` shows only that the library has the string. It does not show that the widget still has that name.
- The check finds only string literals. A name in a variable or a macro is not found.
- The check reads no other preprocessor condition. A name under `#ifdef X` counts for both Qt majors.
- The check does not see Qt style sheet properties, resource paths, object layouts or return types. Use `kfw vm` and a device test for these.

For a check against a firmware that is not indexed, the kpm repository has `tools/symcheck`. It reads the firmware file directly.
