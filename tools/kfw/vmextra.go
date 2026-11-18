package main

import (
	"encoding/base64"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var reDevicePath = regexp.MustCompile(`^([A-Za-z0-9][A-Za-z0-9._-]*):(/.*)$`)

// devicePath splits "<id>:/path" if a device with that id exists. A Windows
// path such as C:/x is a host path, because no device has the id C.
func devicePath(dir, s string) (*vmRun, string) {
	m := reDevicePath.FindStringSubmatch(s)
	if m == nil {
		return nil, ""
	}
	r, err := loadRun(dir, m[1])
	if err != nil {
		return nil, ""
	}
	return r, m[2]
}

// vmCopy copies one file into or out of a running device. It streams the
// file through exec, so it also reaches a FAT user partition (-fat), which
// only the container sees.
func vmCopy(dir, eng string, args []string) (int, error) {
	if len(args) != 2 {
		return 2, fmt.Errorf("usage: kfw vm cp <file> <id>:<path>, or kfw vm cp <id>:<path> <file>")
	}
	if r, p := devicePath(dir, args[1]); r != nil {
		f, err := os.Open(args[0])
		if err != nil {
			return 2, err
		}
		defer f.Close()
		ensureARM(eng, r.Image)
		c := exec.Command(eng, "exec", "-i", r.container(), "sh", "-c", `mkdir -p "$(dirname "$1")" && cat > "$1"`, "sh", p)
		c.Stdin, c.Stderr = f, os.Stderr
		if err := c.Run(); err != nil {
			return 1, fmt.Errorf("copy to %s:%s: %w", r.ID, p, err)
		}
		return 0, nil
	}
	if r, p := devicePath(dir, args[0]); r != nil {
		ensureARM(eng, r.Image)
		out, err := exec.Command(eng, "exec", r.container(), "cat", p).Output()
		if err != nil {
			return 1, fmt.Errorf("copy from %s:%s: %w", r.ID, p, err)
		}
		return 0, os.WriteFile(args[1], out, 0o644)
	}
	return 2, fmt.Errorf("give one path as <id>:<absolute path> of a started device")
}

// vmForge starts a fake Forgejo in the device: HTTPS on localhost:8443,
// with a new self-signed certificate. A package manager or an updater in the
// device can test its downloads against it. See tools/fakeforge.
func vmForge(dir, eng string, r *vmRun, data string) (int, error) {
	bin := filepath.Join(dir, "cache", "vm", "fakeforge")
	b := exec.Command("go", "build", "-o", bin, "./fakeforge")
	b.Dir = filepath.Join(dir, "tools")
	b.Env = append(os.Environ(), "GOOS=linux", "GOARCH=arm", "GOARM=7", "CGO_ENABLED=0")
	b.Stdout, b.Stderr = os.Stdout, os.Stderr
	if err := b.Run(); err != nil {
		return 2, fmt.Errorf("build fakeforge: %w", err)
	}
	root := filepath.Join(r.State, "forge")
	if err := os.RemoveAll(root); err != nil {
		return 2, err
	}
	if err := copyTree(data, root); err != nil {
		return 2, err
	}
	ensureARM(eng, r.Image)
	if err := exec.Command(eng, "exec", "-d", r.container(), "sh", "-c",
		"exec /vm/fakeforge -root /state/forge >> /state/forge/forge.log 2>&1").Run(); err != nil {
		return 2, fmt.Errorf("start fakeforge: %w", err)
	}
	for i := 0; i < 20; i++ {
		if _, err := os.Stat(filepath.Join(root, "ca.pem")); err == nil {
			fmt.Printf("Fake Forgejo on https://localhost:8443 in device %s. Owner: o.\n"+
				"Trust its certificate in the device: SSL_CERT_FILE=/state/forge/ca.pem. Log: %s\n",
				r.ID, filepath.Join(root, "forge.log"))
			return 0, nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return 1, fmt.Errorf("fakeforge did not start: see %s", filepath.Join(root, "forge.log"))
}

func copyTree(src, dst string) error {
	return filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		t := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(t, 0o755)
		}
		in, err := os.Open(p)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.Create(t)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			out.Close()
			return err
		}
		return out.Close()
	})
}

const keeperUnit = `[Unit]
Description=kfw: keep the qemu-arm binfmt handler registered
After=systemd-binfmt.service

[Service]
ExecStart=/bin/sh /usr/local/libexec/kfw-binfmt.sh wait
Restart=always

[Install]
WantedBy=multi-user.target
`

// vmMachineSetup makes the ARM handler permanent in the Podman machine: a
// systemd service runs the keeper loop at all times, and the stale binfmt_misc
// automount is masked. -remove undoes both.
func vmMachineSetup(args []string) (int, error) {
	fs := flag.NewFlagSet("vm setup", flag.ContinueOnError)
	remove := fs.Bool("remove", false, "remove the service and unmask the automount")
	if err := fs.Parse(args); err != nil {
		return 2, nil
	}
	eng, err := engine()
	if err != nil {
		return 2, err
	}
	if !isPodman(eng) {
		return 2, fmt.Errorf("kfw vm setup is for the Podman machine. For Docker, register qemu-arm with qemu-user-static on the host")
	}
	if *remove {
		err := machineSSH(`systemctl disable --now kfw-binfmt.service 2>/dev/null; rm -f /etc/systemd/system/kfw-binfmt.service /usr/local/libexec/kfw-binfmt.sh; systemctl unmask proc-sys-fs-binfmt_misc.automount; systemctl daemon-reload`)
		if err != nil {
			return 1, fmt.Errorf("remove: %w", err)
		}
		fmt.Println("Removed kfw-binfmt.service and unmasked proc-sys-fs-binfmt_misc.automount in the Podman machine.")
		return 0, nil
	}
	loop := base64.StdEncoding.EncodeToString([]byte(keeperLoop))
	unit := base64.StdEncoding.EncodeToString([]byte(keeperUnit))
	err = machineSSH(`mkdir -p /usr/local/libexec && echo ` + loop + ` | base64 -d > /usr/local/libexec/kfw-binfmt.sh && echo ` + unit +
		` | base64 -d > /etc/systemd/system/kfw-binfmt.service && systemctl mask proc-sys-fs-binfmt_misc.automount && systemctl daemon-reload && systemctl enable kfw-binfmt.service && systemctl restart kfw-binfmt.service`)
	if err != nil {
		return 1, fmt.Errorf("setup: %w", err)
	}
	c := exec.Command("podman", "machine", "ssh", "systemctl is-active kfw-binfmt.service")
	c.Dir = os.TempDir()
	out, _ := c.Output()
	if strings.TrimSpace(string(out)) != "active" {
		return 1, fmt.Errorf("kfw-binfmt.service is %q", strings.TrimSpace(string(out)))
	}
	fmt.Println("kfw-binfmt.service runs in the Podman machine and starts with it. It registers qemu-arm again each second when another WSL distro clears it. Undo: kfw vm setup -remove")
	return 0, nil
}
