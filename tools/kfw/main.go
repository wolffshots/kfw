// Command kfw finds new Kobo firmware, indexes it, and checks the mods and
// their install packages against each indexed firmware.
//
// Run it from the nickel-symbols folder. Build it once with
//
//	go -C tools build -o .. ./kfw
//
// Commands:
//
//	kfw sync [-offline] [-keep]           ask Kobo for new firmware, index it, write report.md
//	kfw index [-libs] <firmware.zip>|<version>
//	                                      build or rebuild the index of one firmware
//	kfw check [-qt 5|6|5,6] [-baseline X] <src>
//	                                      check mod sources (see modcheck)
//	kfw test [-as kobo|koboroot] [-fw X] <pkg>
//	                                      check a Kobo.tgz or KoboRoot.tgz against each firmware
//	kfw vm [start|exec|logs|shot|reboot|upgrade|stop]
//	                                      run Nickel of a firmware in an emulator
//	kfw qtmeta <libnickel>                print the Qt meta data of a library
//
// Firmware files and their contents are untrusted data. kfw reads them as
// archives and ELF files. Only the emulator runs firmware code, and it runs
// it in a container.
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"nickelsymbols/modcheck"
	"nickelsymbols/qtmeta"
)

const usage = `usage: kfw <command> [arguments]

  sync [-offline] [-keep]             ask Kobo for new firmware, index it, check the mods, write report.md
  index [-libs] <firmware.zip>|<version>
                                      build the index of one firmware (a version rebuilds from its rootfs image)
  check [-qt 5|6|5,6] [-baseline X.Y.Z[,X.Y.Z]] <source>...
                                      check mod sources against each indexed firmware
  test [-as kobo|koboroot] [-fw X.Y.Z,...] [-out file] <pkg>
                                      check a Kobo.tgz or KoboRoot.tgz against each indexed firmware
  vm [flags] | vm start|exec|logs|shot|reboot|upgrade|stop ...
                                      run Nickel in an emulator (kfw vm -h)
  qtmeta <libnickel.so.1.0.0>         print the Qt signals, slots and methods of a library as JSON
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	var err error
	code := 0
	switch cmd {
	case "sync":
		code, err = cmdSync(args)
	case "index":
		err = cmdIndex(args)
	case "check":
		r, e := root()
		if e != nil {
			err = e
			break
		}
		code = modcheck.Main(append([]string{"-index", filepath.Join(r, "firmware")}, args...))
	case "test":
		code, err = cmdTest(args)
	case "vm":
		code, err = cmdVM(args)
	case "qtmeta":
		if len(args) != 1 {
			err = fmt.Errorf("usage: kfw qtmeta <libnickel.so.1.0.0>")
			break
		}
		err = qtmeta.Run(args[0], os.Stdout, os.Stderr)
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "kfw:", err)
		os.Exit(2)
	}
	os.Exit(code)
}

// root returns the nickel-symbols folder: the current folder or the nearest
// parent that has devices.tsv.
func root() (string, error) {
	d, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(d, "devices.tsv")); err == nil {
			return d, nil
		}
		p := filepath.Dir(d)
		if p == d {
			return "", fmt.Errorf("no devices.tsv in the current folder or a parent: run kfw in nickel-symbols")
		}
		d = p
	}
}
