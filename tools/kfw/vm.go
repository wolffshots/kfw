package main

import (
	"bufio"
	"bytes"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"nickelsymbols/modcheck"
)

// The emulator runs the rootfs image of a firmware as an ARMv7 container
// under qemu-user. The design follows the Nickel VM of NickelInkSync PR 27
// (github.com/dkuester/NickelInkSync, MIT license): /dev/fb0 is a file, an
// LD_PRELOAD shim answers its ioctls, a file stands in for the NTX hardware
// configuration partition, and a tmpfs fakes the battery.
//
// One emulated device has an id. Its container is kfw-vm-<id>, its state
// folder is vm/<id>/ (or -state), and vm/<id>.json records it. That file is
// also the lock: a second run with the same id stops with an error.

var (
	//go:embed vm/init.sh
	vmInit []byte
	//go:embed vm/stub.sh
	vmStub []byte
	//go:embed vm/vmshim.c
	vmShimC []byte
)

const fbW, fbH = 1072, 1448 // as in vm/vmshim.c

// hwcfg returns the hwcfg partition image: the NTX hardware configuration
// of a Kobo Clara Colour (board E60T00, MT8113T, 1448x1072 colour screen),
// from vm/hwcfg.py of NickelInkSync. Each field is an index into the value
// list of the firmware tool ntx_hwconfig. 6.x reads the partition by its
// label. 4.x reads it as /dev/mmcblk0p6, or from sector 1024 of the raw
// disk /dev/mmcblk0 (see rawDisk).
func hwcfg() []byte {
	fields := map[int]byte{
		0: 113, 1: 12, 4: 17, 5: 11, 7: 18, 8: 3, 9: 30, 10: 6, 14: 1, 16: 4,
		17: 2, 19: 2, 27: 16, 29: 12, 31: 5, 32: 1, 38: 10, 45: 11, 63: 0x08, 64: 0x08,
		13: 0x09, // customer: Kobo
	}
	const size = 77
	vals := make([]byte, size)
	for k, v := range fields {
		vals[k] = v
	}
	img := make([]byte, 1<<20)
	block := append([]byte("HW CONFIG v3.8\x00"), size)
	block = append(block, vals...)
	copy(img[512:], block)
	copy(img[4*512:], "VM0000000001\n")
	return img
}

// rawDisk returns a stand-in for the raw eMMC /dev/mmcblk0 of 4.x. Nickel
// 4.x reads its first 16 KiB. On an MTK board, sector 1024 has no hardware
// configuration (it is in partition 6), so the disk is blank.
func rawDisk() []byte {
	return make([]byte, 1<<20)
}

// vmRun is the record of one emulated device.
type vmRun struct {
	ID       string `json:"id"`
	Firmware string `json:"firmware"`
	Format   string `json:"format"` // koboroot (4.x) or rootfs (5.x, 6.x)
	QtMajor  int    `json:"qt_major"`
	Image    string `json:"image"` // the image of the current boot
	State    string `json:"state"`
	Boots    int    `json:"boots"`
	Started  string `json:"started"`
	Strace   bool   `json:"strace"`
	Fat      bool   `json:"fat"` // the user partition is a FAT32 image in the volume fatVolume
}

func (r *vmRun) container() string { return "kfw-vm-" + r.ID }
func (r *vmRun) is6x() bool       { return r.Format == "rootfs" }
func (r *vmRun) fatVolume() string { return r.container() + "-onboard" }

var reID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

func recordPath(dir, id string) string { return filepath.Join(dir, "vm", id+".json") }

func loadRun(dir, id string) (*vmRun, error) {
	b, err := os.ReadFile(recordPath(dir, id))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("no device %s: start it with kfw vm start -name %s", id, id)
	}
	if err != nil {
		return nil, err
	}
	var r vmRun
	return &r, json.Unmarshal(b, &r)
}

func (r *vmRun) save(dir string) error {
	b, _ := json.MarshalIndent(r, "", "  ")
	return os.WriteFile(recordPath(dir, r.ID), b, 0o644)
}

// lock creates the record. It fails if another run has the id.
func (r *vmRun) lock(dir string) error {
	os.MkdirAll(filepath.Join(dir, "vm"), 0o755)
	f, err := os.OpenFile(recordPath(dir, r.ID), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if errors.Is(err, fs.ErrExist) {
		old, _ := loadRun(dir, r.ID)
		since := "?"
		if old != nil {
			since = old.Firmware + ", started " + old.Started
		}
		return fmt.Errorf("device %s is in use (%s). Use another -name, or stop it with kfw vm stop %s", r.ID, since, r.ID)
	}
	if err != nil {
		return err
	}
	f.Close()
	return r.save(dir)
}

func fwManifest(dir, fw string) manifest {
	var m manifest
	if b, err := os.ReadFile(filepath.Join(dir, "firmware", fw, "manifest.json")); err == nil {
		json.Unmarshal(b, &m)
	}
	// Indexes made before 2026-11-17 have no format and Qt fields: all 4.x.
	if m.Format == "" {
		m.Format = "koboroot"
	}
	if m.QtMajor == 0 && strings.HasPrefix(fw, "4.") {
		m.QtMajor, m.QtVersion = 5, "5.2.1"
	}
	return m
}

func cmdVM(args []string) (int, error) {
	if len(args) > 0 {
		sub, rest := args[0], args[1:]
		switch sub {
		case "start":
			return vmStart(rest)
		case "exec", "cp", "logs", "shot", "reboot", "upgrade", "stop", "forge":
			return vmOnDevice(sub, rest)
		case "setup":
			return vmMachineSetup(rest)
		}
	}
	return vmOnce(args)
}

const vmUsage = `usage:
  kfw vm [flags]                     one run: boot, wait -time, check, report, remove
  kfw vm start [flags]               boot a device and leave it running
  kfw vm exec <id> <command>...      run a shell command in the device
  kfw vm cp <file> <id>:<path>       copy a file into the device
  kfw vm cp <id>:<path> <file>       copy a file out of the device
  kfw vm logs <id>                   print the syslog (4.x: logread, 6.x: syslog.txt)
  kfw vm shot <id> [file.png]        save the screen as a PNG
  kfw vm reboot <id> [-wait text] [-time 2m]
                                     restart Nickel and keep the rootfs; the boot
                                     installs .kobo/Kobo.tgz (4.x: also KoboRoot.tgz)
  kfw vm upgrade <id> <version> [-wait text] [-time 2m]
                                     boot a clean rootfs of <version> with the same
                                     user partition, as a firmware update does
  kfw vm forge <id> <dir>            serve a fake Forgejo from <dir> in the device
  kfw vm stop <id>                   remove the container, its images and the lock
  kfw vm setup [-remove]             keep qemu-arm registered in the Podman machine

Flags of kfw vm and kfw vm start:`

// runFlags are the flags of a one-shot run and of start.
type runFlags struct {
	fw, pkg, as, name, state, wait *string
	time                           *time.Duration
	keep, strace, fat              *bool
}

func newRunFlags(name string, wait time.Duration, start bool) (*flag.FlagSet, *runFlags) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	f := &runFlags{
		fw:     fs.String("fw", "", "the firmware version to run (default: the newest with a rootfs image)"),
		pkg:    fs.String("pkg", "", "a Kobo.tgz or KoboRoot.tgz to install before Nickel starts"),
		as:     fs.String("as", "", "the install file of -pkg: kobo or koboroot (default: from the file name)"),
		name:   fs.String("name", "", "the device id: container kfw-vm-<id>, state folder vm/<id>/ (default: the firmware version)"),
		state:  fs.String("state", "", "use this state folder and keep its user partition onboard/ (default: a new vm/<id>/)"),
		time:   fs.Duration("time", wait, "how long Nickel runs before kfw checks it (with -wait: the longest wait)"),
		strace: fs.Bool("strace", false, "log each system call of Nickel to strace.log (qemu-user QEMU_STRACE)"),
		fat:    fs.Bool("fat", false, "make the user partition a FAT32 image, as on a device (use kfw vm cp and exec for its files)"),
	}
	if start {
		f.wait = fs.String("wait", "", "wait until a syslog line has this text, for example \"done init\"")
		f.keep = new(bool)
	} else {
		f.wait = new(string)
		f.keep = fs.Bool("keep", false, "leave the device running (kfw vm exec <id> sh, kfw vm stop <id>)")
	}
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, vmUsage)
		fs.PrintDefaults()
	}
	return fs, f
}

// vmOnce is the one-shot run: boot, wait, check, report, and remove.
func vmOnce(args []string) (int, error) {
	fs, f := newRunFlags("vm", 3*time.Minute, false)
	if err := fs.Parse(args); err != nil {
		return 2, nil
	}
	// An unknown subcommand such as "list" must not boot a default device.
	if fs.NArg() > 0 {
		return 2, fmt.Errorf("kfw vm: unknown command or argument %q (run kfw vm -h)", fs.Arg(0))
	}
	dir, eng, err := vmEnv()
	if err != nil {
		return 2, err
	}
	r, plugins, err := vmSetup(dir, eng, f)
	if err != nil {
		return 2, err
	}
	if !*f.keep {
		defer vmTeardown(dir, eng, r)
	}
	if err := vmBoot(dir, eng, r); err != nil {
		return 2, err
	}
	r.save(dir)
	fmt.Printf("Nickel %s starts in container %s. kfw checks it in %s.\n", r.Firmware, r.container(), *f.time)
	deadline := time.Now().Add(*f.time)
	for time.Now().Before(deadline) && running(eng, r.container()) {
		time.Sleep(5 * time.Second)
	}
	// init.sh execs Nickel as process 1, so the container runs while Nickel runs.
	alive := running(eng, r.container())
	logErr := vmCollect(eng, r, alive)
	code := vmReport(r, fwManifest(dir, r.Firmware), *f.pkg, plugins, alive, logErr, *f.time)
	b, _ := os.ReadFile(filepath.Join(r.State, "report.md"))
	fmt.Print(string(b))
	if *f.keep {
		fmt.Printf("The device runs. kfw vm exec %s <command>, kfw vm stop %s.\n", r.ID, r.ID)
	}
	return code, nil
}

// vmStart boots a device and leaves it running for exec, reboot and upgrade.
func vmStart(args []string) (int, error) {
	fs, f := newRunFlags("vm start", 2*time.Minute, true)
	if err := fs.Parse(args); err != nil {
		return 2, nil
	}
	if fs.NArg() > 0 {
		return 2, fmt.Errorf("kfw vm start: unknown argument %q", fs.Arg(0))
	}
	dir, eng, err := vmEnv()
	if err != nil {
		return 2, err
	}
	r, _, err := vmSetup(dir, eng, f)
	if err != nil {
		return 2, err
	}
	if err := vmBoot(dir, eng, r); err != nil {
		vmTeardown(dir, eng, r)
		return 2, err
	}
	r.save(dir)
	fmt.Printf("Device %s: firmware %s in container %s. State: %s\n", r.ID, r.Firmware, r.container(), r.State)
	return vmWait(eng, r, *f.wait, *f.time), nil
}

func vmEnv() (string, string, error) {
	dir, err := root()
	if err != nil {
		return "", "", err
	}
	eng, err := engine()
	return dir, eng, err
}

// vmSetup picks the firmware, prepares the state folder and the package
// slot, and takes the lock.
func vmSetup(dir, eng string, f *runFlags) (*vmRun, []*pkgFile, error) {
	fw := *f.fw
	if fw == "" {
		if fw = newestImage(eng, dir); fw == "" {
			return nil, nil, fmt.Errorf("no rootfs image: run kfw index <firmware.zip> first")
		}
	}
	img := rootfsImage(fw)
	if !imageExists(eng, img) {
		return nil, nil, fmt.Errorf("no image %s: run kfw index -libs <firmware.zip> (the files in downloads/ work)", img)
	}
	id := *f.name
	if id == "" {
		id = fw
	}
	if !reID.MatchString(id) {
		return nil, nil, fmt.Errorf("-name %q: use letters, digits, '.', '_' and '-'", id)
	}
	m := fwManifest(dir, fw)
	r := &vmRun{ID: id, Firmware: fw, Format: m.Format, QtMajor: m.QtMajor, Image: img,
		Started: time.Now().Format(time.RFC3339), Strace: *f.strace, Fat: *f.fat}
	if err := ensureARM(eng, img); err != nil {
		return nil, nil, err
	}
	if err := buildHelpers(eng, filepath.Join(dir, "cache", "vm")); err != nil {
		return nil, nil, err
	}
	if err := r.lock(dir); err != nil {
		return nil, nil, err
	}
	// A new device gets a new FAT image. It lasts until kfw vm stop.
	exec.Command(eng, "volume", "rm", "-f", r.fatVolume()).Run()
	fail := func(err error) (*vmRun, []*pkgFile, error) {
		os.Remove(recordPath(dir, id))
		return nil, nil, err
	}
	if *f.state != "" {
		r.State, _ = filepath.Abs(*f.state)
	} else {
		r.State = filepath.Join(dir, "vm", id)
		if err := os.RemoveAll(r.State); err != nil {
			return fail(err)
		}
	}
	for _, d := range []string{"onboard/.kobo", "aux", "pkg"} {
		if err := os.MkdirAll(filepath.Join(r.State, d), 0o755); err != nil {
			return fail(err)
		}
	}
	clearBoot(r.State)
	for _, n := range []string{"report.md", "container.log", "screen.png"} {
		os.Remove(filepath.Join(r.State, n))
	}
	os.WriteFile(filepath.Join(r.State, "hwcfg.img"), hwcfg(), 0o644)
	os.WriteFile(filepath.Join(r.State, "mmcblk0.img"), rawDisk(), 0o644)
	os.WriteFile(filepath.Join(r.State, "fb0.raw"), make([]byte, fbW*fbH*4), 0o644)
	var plugins []*pkgFile
	if *f.pkg != "" {
		kind, err := pkgKind(*f.pkg, *f.as)
		if err != nil {
			return fail(err)
		}
		files, _, err := readPackage(*f.pkg, kind)
		if err != nil {
			return fail(err)
		}
		for _, pf := range files {
			if pf.ELF != nil && pf.ELF.QtMajor > 0 {
				plugins = append(plugins, pf)
			}
		}
		name := map[string]string{"kobo": "Kobo.tgz", "koboroot": "KoboRoot.tgz"}[kind]
		if kind == "koboroot" && r.is6x() {
			fmt.Printf("Firmware %s does not install KoboRoot.tgz. kfw does not install it either.\n", fw)
		} else if err := copyFile(*f.pkg, filepath.Join(r.State, "pkg", name)); err != nil {
			return fail(err)
		}
	}
	return r, plugins, nil
}

// clearBoot removes the package slot and the logs of the previous boot, so
// that a wait for a log line sees only the new boot.
func clearBoot(state string) {
	for _, n := range []string{"pkg/Kobo.tgz", "pkg/KoboRoot.tgz", "nickel.log", "syslog.txt", "logread.txt", "strace.log"} {
		os.Remove(filepath.Join(state, n))
	}
}

func vmBoot(dir, eng string, r *vmRun) error {
	exec.Command(eng, "rm", "-f", r.container()).Run()
	strace := "0"
	if r.Strace {
		strace = "1"
	}
	run := exec.Command(eng, "run", "-d", "--name", r.container(), "--privileged", "--platform", "linux/arm/v7",
		"-e", "KFW_STRACE="+strace,
		"-v", hostPath(filepath.Join(dir, "cache", "vm"))+":/vm:ro", "-v", hostPath(r.State)+":/state")
	if r.Fat {
		run.Args = append(run.Args, "-e", "KFW_FAT=1", "-v", r.fatVolume()+":/fat")
	}
	run.Args = append(run.Args, r.Image, "/bin/sh", "/vm/init.sh")
	run.Stderr = os.Stderr
	if err := run.Run(); err != nil {
		return fmt.Errorf("start the container: %w", err)
	}
	r.Boots++
	return nil
}

// vmWait waits until Nickel logs a line with text, or for d if text is
// empty. It returns 0 if Nickel runs at the end.
func vmWait(eng string, r *vmRun, text string, d time.Duration) int {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		time.Sleep(5 * time.Second)
		if !running(eng, r.container()) {
			fmt.Printf("FAIL: Nickel stopped. See %s.\n", filepath.Join(r.State, "nickel.log"))
			return 1
		}
		if text == "" {
			continue
		}
		if l, _ := vmSyslog(eng, r); strings.Contains(l, text) {
			// The mod and Nickel go on with their start after the line.
			time.Sleep(20 * time.Second)
			fmt.Printf("OK: the syslog has %q.\n", text)
			return 0
		}
	}
	if text != "" {
		fmt.Printf("FAIL: no syslog line with %q after %s.\n", text, d)
		return 1
	}
	fmt.Printf("OK: Nickel runs after %s.\n", d)
	return 0
}

// vmSyslog returns the syslog of the device. Nickel 4.x replaces our
// syslogd with its own in-memory one (syslogd -C), which only logread
// reads. On 6.x syslogd writes syslog.txt in the state folder.
func vmSyslog(eng string, r *vmRun) (string, error) {
	b, _ := os.ReadFile(filepath.Join(r.State, "syslog.txt"))
	if r.is6x() {
		return string(b), nil
	}
	out, err := exec.Command(eng, "exec", r.container(), "logread").CombinedOutput()
	if err != nil {
		return string(b), fmt.Errorf("logread in %s: %v: %s", r.container(), err, lastLine(string(out)))
	}
	return string(b) + string(out), nil
}

// vmCollect writes logread.txt, container.log and screen.png. It returns
// the error of a failed logread, which makes the plugin results unreliable.
func vmCollect(eng string, r *vmRun, alive bool) string {
	logErr := ""
	if alive && !r.is6x() {
		ensureARM(eng, r.Image)
		if out, err := exec.Command(eng, "exec", r.container(), "logread").Output(); err == nil {
			os.WriteFile(filepath.Join(r.State, "logread.txt"), out, 0o644)
		} else {
			logErr = err.Error()
		}
	}
	logs, _ := exec.Command(eng, "logs", r.container()).CombinedOutput()
	os.WriteFile(filepath.Join(r.State, "container.log"), logs, 0o644)
	if err := screenshot(filepath.Join(r.State, "fb0.raw"), filepath.Join(r.State, "screen.png")); err != nil {
		fmt.Fprintln(os.Stderr, "screenshot:", err)
	}
	return logErr
}

// vmTeardown removes the container, the images of its reboots and the lock.
func vmTeardown(dir, eng string, r *vmRun) {
	exec.Command(eng, "rm", "-f", r.container()).Run()
	out, _ := exec.Command(eng, "images", "--format", "{{.Repository}}:{{.Tag}}").Output()
	for _, img := range strings.Fields(string(out)) {
		if strings.HasPrefix(img, "localhost/"+r.container()+":") {
			exec.Command(eng, "rmi", "-f", img).Run()
		}
	}
	exec.Command(eng, "volume", "rm", "-f", r.fatVolume()).Run()
	os.Remove(recordPath(dir, r.ID))
	stopKeeper(eng)
}

// vmOnDevice runs a subcommand on a started device.
func vmOnDevice(sub string, args []string) (int, error) {
	if len(args) < 1 || strings.HasPrefix(args[0], "-") {
		fmt.Fprintln(os.Stderr, vmUsage)
		return 2, nil
	}
	id, args := args[0], args[1:]
	dir, eng, err := vmEnv()
	if err != nil {
		return 2, err
	}
	if sub == "cp" {
		return vmCopy(dir, eng, append([]string{id}, args...))
	}
	if sub == "stop" {
		r, err := loadRun(dir, id)
		if err != nil {
			r = &vmRun{ID: id}
		}
		vmTeardown(dir, eng, r)
		fmt.Printf("Device %s stopped. Its state folder stays.\n", id)
		return 0, nil
	}
	r, err := loadRun(dir, id)
	if err != nil {
		return 2, err
	}
	switch sub {
	case "exec":
		if len(args) == 0 {
			return 2, fmt.Errorf("usage: kfw vm exec <id> <command>...")
		}
		if err := ensureARM(eng, r.Image); err != nil {
			return 2, err
		}
		c := exec.Command(eng, "exec", "-i", r.container(), "sh", "-c", strings.Join(args, " "))
		c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := c.Run(); err != nil {
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				return ee.ExitCode(), nil
			}
			return 2, err
		}
		return 0, nil
	case "logs":
		if !r.is6x() {
			ensureARM(eng, r.Image)
		}
		l, err := vmSyslog(eng, r)
		fmt.Print(l)
		if err != nil {
			return 2, err
		}
		return 0, nil
	case "forge":
		if len(args) != 1 {
			return 2, fmt.Errorf("usage: kfw vm forge <id> <dir>")
		}
		return vmForge(dir, eng, r, args[0])
	case "shot":
		out := filepath.Join(r.State, "screen-"+time.Now().Format("150405")+".png")
		if len(args) > 0 {
			out = args[0]
		}
		if err := screenshot(filepath.Join(r.State, "fb0.raw"), out); err != nil {
			return 2, err
		}
		fmt.Println(out)
		return 0, nil
	}

	// reboot and upgrade
	fs := flag.NewFlagSet("vm "+sub, flag.ContinueOnError)
	wait := fs.String("wait", "", "wait until a syslog line has this text, for example \"done init\"")
	d := fs.Duration("time", 2*time.Minute, "how long to wait (with -wait: the longest wait)")
	ver := ""
	if sub == "upgrade" {
		if len(args) == 0 || strings.HasPrefix(args[0], "-") {
			return 2, fmt.Errorf("usage: kfw vm upgrade <id> <version> [-wait text] [-time 2m]")
		}
		ver, args = args[0], args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return 2, nil
	}
	if sub == "upgrade" {
		img := rootfsImage(ver)
		if !imageExists(eng, img) {
			return 2, fmt.Errorf("no image %s: run kfw index -libs <firmware.zip>", img)
		}
		m := fwManifest(dir, ver)
		r.Firmware, r.Format, r.QtMajor, r.Image = ver, m.Format, m.QtMajor, img
	}
	if err := ensureARM(eng, r.Image); err != nil {
		return 2, err
	}
	// Write the files of the user partition to disk, as the firmware does
	// before a reboot. The FAT image needs it.
	exec.Command(eng, "exec", r.container(), "sync").Run()
	exec.Command(eng, "stop", "-t", "5", r.container()).Run()
	if sub == "reboot" {
		// Keep the root file system of the device: the mods that the last
		// boot installed and the files that they changed.
		img := fmt.Sprintf("localhost/%s:boot%d", r.container(), r.Boots)
		if out, err := exec.Command(eng, "commit", r.container(), img).CombinedOutput(); err != nil {
			return 2, fmt.Errorf("commit %s: %v: %s", r.container(), err, lastLine(string(out)))
		}
		r.Image = img
	}
	clearBoot(r.State)
	if err := vmBoot(dir, eng, r); err != nil {
		return 2, err
	}
	r.save(dir)
	fmt.Printf("Device %s: firmware %s, boot %d (%s).\n", r.ID, r.Firmware, r.Boots, r.Image)
	return vmWait(eng, r, *wait, *d), nil
}

// ensureARM checks that the engine runs ARM programs, and registers the
// qemu-arm handler in the Podman machine if it does not. All WSL distros
// share one binfmt_misc, and systemd-binfmt in another distro clears it
// every few minutes, so it also starts a keeper loop in the machine that
// registers the handler again each second while a kfw-vm container runs.
// The user approved these binfmt changes on 2026-11-17.
func ensureARM(eng, img string) error {
	ok := func() bool {
		return exec.Command(eng, "run", "--rm", "--platform", "linux/arm/v7", img, "/bin/true").Run() == nil
	}
	if !isPodman(eng) {
		if ok() {
			return nil
		}
		return fmt.Errorf("the container engine cannot run ARM programs: install qemu-user-static and register qemu-arm in binfmt_misc")
	}
	startKeeper()
	if ok() {
		return nil
	}
	machineSSH("sh -c 'cat /usr/lib/binfmt.d/qemu-arm-static.conf > /proc/sys/fs/binfmt_misc/register'")
	if ok() {
		fmt.Println("kfw: registered qemu-arm in binfmt_misc of the Podman machine.")
		return nil
	}
	// A systemd automount over a stale WSL mount hides binfmt_misc.
	machineSSH("sh -c 'umount -l /proc/sys/fs/binfmt_misc; mount -t binfmt_misc binfmt_misc /proc/sys/fs/binfmt_misc && cat /usr/lib/binfmt.d/qemu-arm-static.conf > /proc/sys/fs/binfmt_misc/register'")
	if ok() {
		fmt.Println("kfw: mounted binfmt_misc again and registered qemu-arm in the Podman machine.")
		return nil
	}
	return fmt.Errorf("the Podman machine cannot run ARM programs, and kfw could not register qemu-arm.\n" +
		"Check: podman machine ssh \"ls /usr/lib/binfmt.d/ /proc/sys/fs/binfmt_misc/\"")
}

func isPodman(eng string) bool {
	return strings.HasPrefix(strings.ToLower(filepath.Base(eng)), "podman")
}

// machineSSH runs a command in the Podman machine. It runs in the temp
// folder: podman machine ssh from Git Bash leaves a file NUL with the host
// key in the current folder.
func machineSSH(cmd string) error {
	c := exec.Command("podman", "machine", "ssh", cmd)
	c.Dir = os.TempDir()
	return c.Run()
}

// keeperLoop registers qemu-arm again each second when it is gone. If a
// stale automount hides binfmt_misc, it mounts binfmt_misc again first.
// One loop runs at a time: it holds the lock /tmp/kfw-binfmt.lock and writes
// its PID to /tmp/kfw-binfmt.pid. A second loop exits at once, or with the
// argument "wait" (the service of kfw vm setup) waits for the lock.
const keeperLoop = `exec 9>/tmp/kfw-binfmt.lock
if [ "$1" = wait ]; then flock 9; else flock -n 9 || exit 0; fi
echo $$ > /tmp/kfw-binfmt.pid
while true; do
  if [ ! -e /proc/sys/fs/binfmt_misc/qemu-arm ]; then
    [ -w /proc/sys/fs/binfmt_misc/register ] || { umount -l /proc/sys/fs/binfmt_misc 2>/dev/null; mount -t binfmt_misc binfmt_misc /proc/sys/fs/binfmt_misc; }
    cat /usr/lib/binfmt.d/qemu-arm-static.conf > /proc/sys/fs/binfmt_misc/register 2>/dev/null
  fi
  sleep 1
done
`

// startKeeper starts a keeper loop for the length of a run. It exits at once
// if a loop runs already, for example the service of kfw vm setup.
func startKeeper() {
	b64 := base64.StdEncoding.EncodeToString([]byte(keeperLoop))
	machineSSH(`echo ` + b64 + ` | base64 -d > /tmp/kfw-binfmt.sh; nohup sh /tmp/kfw-binfmt.sh >/dev/null 2>&1 &`)
}

// stopKeeper stops the keeper loop when no kfw-vm container runs.
func stopKeeper(eng string) {
	if !isPodman(eng) {
		return
	}
	out, _ := exec.Command(eng, "ps", "--format", "{{.Names}}").Output()
	for _, n := range strings.Fields(string(out)) {
		if strings.HasPrefix(n, "kfw-vm-") {
			return
		}
	}
	// The service of kfw vm setup is not stopped: systemd owns it.
	machineSSH(`systemctl is-active -q kfw-binfmt 2>/dev/null || kill $(cat /tmp/kfw-binfmt.pid 2>/dev/null) 2>/dev/null`)
}

// lastLine returns the error line of engine output: the last line that has
// "error" or "msg", or else the last line.
func lastLine(s string) string {
	l := strings.Split(strings.TrimSpace(s), "\n")
	for i := len(l) - 1; i >= 0; i-- {
		if strings.Contains(strings.ToLower(l[i]), "error") || strings.Contains(l[i], "msg") {
			return strings.TrimSpace(l[i])
		}
	}
	return l[len(l)-1]
}

func newestImage(eng, dir string) string {
	best := ""
	ms, _ := filepath.Glob(filepath.Join(dir, "firmware", "*", "manifest.json"))
	for _, p := range ms {
		v := filepath.Base(filepath.Dir(p))
		if imageExists(eng, rootfsImage(v)) && (best == "" || modcheck.VersionLess(best, v)) {
			best = v
		}
	}
	return best
}

func pkgKind(p, as string) (string, error) {
	if as != "" {
		return as, nil
	}
	switch b := strings.ToLower(filepath.Base(p)); {
	case strings.Contains(b, "koboroot"):
		return "koboroot", nil
	case strings.HasPrefix(b, "kobo.") || strings.HasPrefix(b, "kobo-") || strings.HasPrefix(b, "kobo_"):
		return "kobo", nil
	}
	return "", fmt.Errorf("cannot tell the install file from the name %q: give -as kobo or -as koboroot", filepath.Base(p))
}

func copyFile(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o644)
}

func running(eng, name string) bool {
	out, err := exec.Command(eng, "inspect", "-f", "{{.State.Running}}", name).Output()
	return err == nil && strings.TrimSpace(string(out)) == "true"
}

// buildHelpers writes init.sh and stub.sh and compiles vmshim.so with the
// Kobo toolchain, which links against an old glibc that 4.x and 6.x have.
func buildHelpers(eng, dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	os.WriteFile(filepath.Join(dir, "init.sh"), vmInit, 0o755)
	os.WriteFile(filepath.Join(dir, "stub.sh"), vmStub, 0o755)
	old, _ := os.ReadFile(filepath.Join(dir, "vmshim.c"))
	if _, err := os.Stat(filepath.Join(dir, "vmshim.so")); err == nil && bytes.Equal(old, vmShimC) {
		return nil
	}
	os.WriteFile(filepath.Join(dir, "vmshim.c"), vmShimC, 0o644)
	c := exec.Command(eng, "run", "--rm", "-v", hostPath(dir)+":/w", toolImage,
		"arm-nickel-linux-gnueabihf-gcc", "-O2", "-Wall", "-shared", "-fPIC", "-o", "/w/vmshim.so", "/w/vmshim.c", "-ldl")
	c.Stdout, c.Stderr = os.Stdout, os.Stderr
	if err := c.Run(); err != nil {
		return fmt.Errorf("build vmshim.so: %w", err)
	}
	return nil
}

// screenshot saves the 32-bit BGRA framebuffer file as a PNG.
func screenshot(raw, out string) error {
	b, err := os.ReadFile(raw)
	if err != nil {
		return err
	}
	if len(b) < fbW*fbH*4 {
		return fmt.Errorf("%s is too small", raw)
	}
	im := image.NewRGBA(image.Rect(0, 0, fbW, fbH))
	for i := 0; i < fbW*fbH; i++ {
		im.Set(i%fbW, i/fbW, color.RGBA{b[4*i+2], b[4*i+1], b[4*i], 255})
	}
	f, err := os.Create(out)
	if err != nil {
		return err
	}
	if err := png.Encode(f, im); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// grepFile returns the lines of p that contain any of the words.
func grepFile(p string, words ...string) []string {
	f, err := os.Open(p)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []string
	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 1<<20), 1<<20)
	for s.Scan() {
		for _, w := range words {
			if strings.Contains(s.Text(), w) {
				out = append(out, s.Text())
				break
			}
		}
	}
	return out
}

// vmReport writes report.md and returns the exit code: 1 if Nickel did not
// survive, a plugin did not load on the Qt it is built for or loaded on
// another Qt, or kfw could not read the 4.x syslog.
func vmReport(r *vmRun, m manifest, pkg string, plugins []*pkgFile, alive bool, logErr string, wait time.Duration) int {
	var w bytes.Buffer
	code := 0
	fmt.Fprintf(&w, "# Emulator run: %s\n\nFirmware %s (Qt %s). Package: %s. Nickel ran for %s.\n\n", r.ID, r.Firmware, m.QtVersion, orNone(pkg), wait)
	if alive {
		fmt.Fprintln(&w, "- OK: Nickel is still running at the end.")
	} else {
		fmt.Fprintln(&w, "- FAIL: Nickel is not running at the end. See nickel.log and container.log.")
		code = 1
	}
	if logErr != "" {
		fmt.Fprintf(&w, "- ERROR: kfw could not read the 4.x syslog (`logread`: %s). The plugin lines of the syslog are missing, so a plugin that loaded can show as UNKNOWN. Most likely the qemu-arm handler was cleared. Run the test again.\n", logErr)
		code = 1
	}
	// Qt writes its plugin lines to stderr until Nickel installs its message
	// handler, and to the syslog after that. With -strace, stderr goes to
	// strace.log.
	for _, p := range plugins {
		base := path.Base(p.Dest)
		var lines []string
		for _, f := range []string{"nickel.log", "strace.log", "syslog.txt", "logread.txt"} {
			lines = append(lines, grepFile(filepath.Join(r.State, f), base)...)
		}
		loaded := false
		for _, l := range lines {
			if strings.Contains(l, "loaded library") {
				loaded = true
			}
		}
		want := p.ELF.QtMajor == m.QtMajor
		switch {
		case len(lines) == 0 && !want:
			fmt.Fprintf(&w, "- UNKNOWN: Qt did not look at `%s` (Qt %d plugin) in this run, so the run shows nothing about it.\n", p.Dest, p.ELF.QtMajor)
		case want && !loaded && logErr != "":
			fmt.Fprintf(&w, "- UNKNOWN: no line shows that Qt loaded `%s` (Qt %d plugin), but the syslog is missing.\n", p.Dest, p.ELF.QtMajor)
		case want && loaded:
			fmt.Fprintf(&w, "- OK: Qt loaded `%s` (Qt %d plugin).\n", p.Dest, p.ELF.QtMajor)
		case want && !loaded:
			fmt.Fprintf(&w, "- FAIL: Qt did not load `%s` (Qt %d plugin).\n", p.Dest, p.ELF.QtMajor)
			code = 1
		case !want && loaded:
			fmt.Fprintf(&w, "- FAIL: Qt %d loaded `%s`, a Qt %d plugin.\n", m.QtMajor, p.Dest, p.ELF.QtMajor)
			code = 1
		default:
			fmt.Fprintf(&w, "- OK: Qt %d skipped `%s` (Qt %d plugin).\n", m.QtMajor, p.Dest, p.ELF.QtMajor)
		}
		for _, l := range limitLines(lines, 6) {
			fmt.Fprintf(&w, "  - `%s`\n", strings.TrimSpace(l))
		}
	}
	nh := grepFile(filepath.Join(r.State, "syslog.txt"), "NickelHook", "nickelhook", "(nh)")
	nh = append(nh, grepFile(filepath.Join(r.State, "logread.txt"), "NickelHook", "nickelhook", "(nh)")...)
	if len(nh) > 0 {
		fmt.Fprint(&w, "\nNickelHook lines in the syslog:\n\n")
		for _, l := range limitLines(nh, 30) {
			fmt.Fprintf(&w, "    %s\n", l)
		}
	}
	fmt.Fprintf(&w, "\nFiles in %s: nickel.log (stdout and stderr, with QT_DEBUG_PLUGINS=1), syslog.txt, logread.txt (the syslog of 4.x), container.log, screen.png.\n", r.State)
	os.WriteFile(filepath.Join(r.State, "report.md"), w.Bytes(), 0o644)
	return code
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return filepath.Base(s)
}

func limitLines(l []string, n int) []string {
	if len(l) > n {
		return append(l[:n:n], fmt.Sprintf("... %d more lines", len(l)-n))
	}
	return l
}
