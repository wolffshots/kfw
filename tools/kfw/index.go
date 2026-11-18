package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"debug/elf"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"

	"nickelsymbols/qtmeta"
)

const (
	toolImage  = "ghcr.io/pgaskin/nickeltc:1.0" // ARM binutils, debugfs, tar
	libnickel  = "usr/local/Kobo/libnickel.so.1.0.0"
	nickelExec = "usr/local/Kobo/nickel"
)

// rootfsImage is the container image that holds the root file system of a
// firmware. kfw index builds it, and kfw test and kfw vm use it.
func rootfsImage(ver string) string { return "localhost/kfw-rootfs:" + ver }

func engine() (string, error) {
	for _, e := range []string{"podman", "docker"} {
		if p, err := exec.LookPath(e); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("no podman or docker in PATH")
}

// hostPath gives a path in the form that a bind mount of the engine takes.
func hostPath(p string) string {
	a, err := filepath.Abs(p)
	if err != nil {
		a = p
	}
	return filepath.ToSlash(a)
}

func imageExists(eng, img string) bool {
	return exec.Command(eng, "image", "exists", img).Run() == nil
}

// pipe runs a | b and returns when both end.
func pipe(a, b *exec.Cmd) error {
	pr, pw, err := os.Pipe()
	if err != nil {
		return err
	}
	a.Stdout, b.Stdin = pw, pr
	a.Stderr, b.Stderr = os.Stderr, os.Stderr
	if err := a.Start(); err != nil {
		return err
	}
	if err := b.Start(); err != nil {
		a.Process.Kill()
		return err
	}
	pw.Close()
	pr.Close()
	ea, eb := a.Wait(), b.Wait()
	if ea != nil {
		return fmt.Errorf("%s: %w", a.Args[0], ea)
	}
	return eb
}

// The 4.x KoboRoot.tgz is an overlay: the device's base rootfs has the
// soname links (libc.so.6 -> libc-2.11.1.so). Make them, so that the
// emulator can load the libraries.
const sonameLinks = `for d in lib usr/lib usr/local/Qt-5.2.1-arm/lib usr/local/Kobo; do
  [ -d "$d" ] || continue
  for f in "$d"/*.so*; do
    [ -f "$f" ] && [ ! -L "$f" ] || continue
    s=$(arm-nickel-linux-gnueabihf-readelf -d "$f" 2>/dev/null | sed -n 's/.*Library soname: \[\(.*\)\]/\1/p')
    [ -n "$s" ] && [ ! -e "$d/$s" ] && ln -sf "$(basename "$f")" "$d/$s"
  done
done
# lib/ld-linux-armhf.so.3 links to ld-linux.so.3, which only the base rootfs has.
if [ ! -e lib/ld-linux-armhf.so.3 ]; then
  ld=$(cd lib && ls ld-2.*.so 2>/dev/null | head -1)
  [ -n "$ld" ] && ln -sf "$ld" lib/ld-linux.so.3
fi
true`

// buildImage makes the rootfs image of a firmware file and returns the
// firmware layout: koboroot (4.x) or rootfs (5.x, 6.x).
func buildImage(dir, ver, zipPath string) (string, error) {
	eng, err := engine()
	if err != nil {
		return "", err
	}
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return "", err
	}
	defer zr.Close()
	var koboroot, update *zip.File
	for _, f := range zr.File {
		switch f.Name {
		case "KoboRoot.tgz":
			koboroot = f
		case "update.tar":
			update = f
		}
	}
	img := rootfsImage(ver)
	importCmd := exec.Command(eng, "import", "--arch", "arm", "--variant", "v7", "-", img)
	importCmd.Stdout = io.Discard
	switch {
	case koboroot != nil:
		r, err := koboroot.Open()
		if err != nil {
			return "", err
		}
		defer r.Close()
		// Unpack and pack again: the tgz has duplicate paths, which import refuses.
		c := exec.Command(eng, "run", "--rm", "-i", toolImage, "sh", "-ec",
			"mkdir /tmp/r && cd /tmp/r && tar xz && "+sonameLinks+" && tar -c .")
		c.Stdin = r
		fmt.Println("building", img, "from KoboRoot.tgz")
		return "koboroot", importOrRemove(eng, img, c, importCmd)
	case update != nil:
		cache := filepath.Join(dir, "cache", ver)
		if err := os.MkdirAll(cache, 0o755); err != nil {
			return "", err
		}
		ext4 := filepath.Join(cache, "rootfs.ext4")
		if err := unpackRootfs(update, ext4); err != nil {
			return "", err
		}
		defer os.Remove(ext4)
		// debugfs reads the ext4 image without a mount.
		c := exec.Command(eng, "run", "--rm", "-v", hostPath(cache)+":/w:ro", toolImage, "sh", "-ec",
			`mkdir /tmp/r && debugfs -R "rdump / /tmp/r" /w/rootfs.ext4 >/dev/null 2>&1; tar -C /tmp/r -c .`)
		fmt.Println("building", img, "from rootfs.img")
		return "rootfs", importOrRemove(eng, img, c, importCmd)
	}
	return "", fmt.Errorf("%s: no KoboRoot.tgz or update.tar", zipPath)
}

// unpackRootfs writes the decompressed rootfs.img of update.tar to dst.
func unpackRootfs(update *zip.File, dst string) error {
	r, err := update.Open()
	if err != nil {
		return err
	}
	defer r.Close()
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if err != nil {
			return fmt.Errorf("rootfs.img not found in update.tar: %w", err)
		}
		if h.Name != "rootfs.img" {
			continue
		}
		zd, err := zstd.NewReader(tr, zstd.WithDecoderMaxWindow(1<<30))
		if err != nil {
			return err
		}
		defer zd.Close()
		f, err := os.Create(dst)
		if err != nil {
			return err
		}
		// An ext4 image of 1 GB. Refuse more than 4 GB.
		if _, err := io.Copy(f, io.LimitReader(zd, 4<<30)); err != nil {
			f.Close()
			return err
		}
		return f.Close()
	}
}

type manifest struct {
	Firmware      string         `json:"firmware"`
	Device        string         `json:"device"`
	SourceFile    string         `json:"source_file"`
	SourceSHA256  string         `json:"source_sha256"`
	Format        string         `json:"format"`
	Library       string         `json:"library"`
	LibrarySHA256 string         `json:"library_sha256"`
	QtMajor       int            `json:"qt_major"`
	QtVersion     string         `json:"qt_version"`
	Generated     string         `json:"generated"`
	Toolchain     string         `json:"toolchain"`
	Counts        map[string]int `json:"counts"`
}

func fileSHA256(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func cmdIndex(args []string) error {
	fs := flag.NewFlagSet("index", flag.ContinueOnError)
	libsOnly := fs.Bool("libs", false, "only write libs.json.gz and files.txt; keep the libnickel index (fast)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	args = fs.Args()
	if len(args) != 1 {
		return fmt.Errorf("usage: kfw index [-libs] <firmware.zip>|<version>")
	}
	dir, err := root()
	if err != nil {
		return err
	}
	if strings.HasSuffix(args[0], ".zip") {
		return indexZip(dir, args[0], "", !*libsOnly)
	}
	// Rebuild from the rootfs image. Keep the source fields of the manifest.
	var m manifest
	b, err := os.ReadFile(filepath.Join(dir, "firmware", args[0], "manifest.json"))
	if err == nil {
		json.Unmarshal(b, &m)
	}
	m.Firmware = args[0]
	return indexImage(dir, m, !*libsOnly)
}

func indexZip(dir, zipPath, device string, full bool) error {
	mm := reFile.FindStringSubmatch(filepath.Base(zipPath))
	if mm == nil {
		return fmt.Errorf("cannot read the version from the file name: %s", zipPath)
	}
	ver := mm[2]
	sum, err := fileSHA256(zipPath)
	if err != nil {
		return err
	}
	// Keep the fields of an older manifest that this run does not set.
	var m manifest
	if old, err := os.ReadFile(filepath.Join(dir, "firmware", ver, "manifest.json")); err == nil {
		json.Unmarshal(old, &m)
	}
	m.Firmware, m.SourceFile, m.SourceSHA256 = ver, filepath.Base(zipPath), sum
	if device != "" {
		m.Device = device
	}
	eng, err := engine()
	if err != nil {
		return err
	}
	if imageExists(eng, rootfsImage(ver)) {
		fmt.Println("using the existing image", rootfsImage(ver))
		zr, err := zip.OpenReader(zipPath)
		if err != nil {
			return err
		}
		m.Format = "rootfs"
		for _, f := range zr.File {
			if f.Name == "KoboRoot.tgz" {
				m.Format = "koboroot"
			}
		}
		zr.Close()
	} else if m.Format, err = buildImage(dir, ver, zipPath); err != nil {
		return err
	}
	return indexImage(dir, m, full)
}

// binutils writes symbols.tsv, calls.tsv and strings.txt of libnickel to /w.
const binutils = `P=arm-nickel-linux-gnueabihf
L=/r/` + libnickel + `
[ -s "$L" ] || { echo "libnickel not found in the firmware" >&2; exit 1; }
cd /w
# symbols.tsv: address, size, type, bind, mangled, demangled.
$P-readelf --dyn-syms -W $L | awk '$7 != "UND" && NF >= 8 && $1 ~ /:$/ {print $2"\t"$3"\t"$4"\t"$5"\t"$8}' | sort -u -k5,5 > syms.raw
cut -f5 syms.raw | $P-c++filt > syms.dem
{ printf "address\tsize\ttype\tbind\tmangled\tdemangled\n"; paste syms.raw syms.dem; } > symbols.tsv
rm syms.raw syms.dem
# calls.tsv: one line per distinct direct call (bl, blx, b.w, b).
{ printf "caller\tcallee\n"
  $P-objdump -d -C --no-show-raw-insn $L | awk '
    /^[0-9a-f]+ <.*>:$/ { fn=$0; sub(/^[0-9a-f]+ </,"",fn); sub(/>:$/,"",fn); sub(/@@Base$/,"",fn); next }
    /\t(bl|blx|b\.w|b)\t[0-9a-f]+ </ { t=$0; t=substr(t, index(t, " <")+2); sub(/>$/,"",t); sub(/@plt(\+0x[0-9a-f]+)?$/,"",t); sub(/@@Base$/,"",t); if (t !~ /\+0x[0-9a-f]+$/) print fn "\t" t }
  ' | sort -u; } > calls.tsv
$P-strings -n 6 $L > strings.txt
$P-gcc --version | head -1 > toolchain.txt
`

// indexImage writes firmware/<ver>/ from the rootfs image. If full is
// false, it writes only the library index and keeps the libnickel index.
func indexImage(dir string, m manifest, full bool) error {
	eng, err := engine()
	if err != nil {
		return err
	}
	img := rootfsImage(m.Firmware)
	if !imageExists(eng, img) {
		return fmt.Errorf("no image %s: run kfw index <firmware.zip>", img)
	}
	work, err := os.MkdirTemp(dir, ".work.")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	mount := "type=image,source=" + img + ",destination=/r"

	fmt.Println("scanning the libraries of", img)
	scan := exec.Command(eng, "run", "--rm", "--mount", mount, toolImage, "tar", "-C", "/r", "-c", ".")
	scan.Stderr = os.Stderr
	out, err := scan.StdoutPipe()
	if err != nil {
		return err
	}
	if err := scan.Start(); err != nil {
		return err
	}
	fsx, err := scanRootfs(out, filepath.Join(work, "libnickel"))
	if err != nil {
		scan.Process.Kill()
		scan.Wait()
		return err
	}
	if err := scan.Wait(); err != nil {
		return err
	}

	out2 := filepath.Join(dir, "firmware", m.Firmware)
	outputs := []string{"libs.json.gz", "files.txt"}
	if full {
		outputs = append(outputs, "symbols.tsv", "calls.tsv", "strings.txt", "qtmeta.json")
		if err := libnickelIndex(eng, mount, work); err != nil {
			return err
		}
	} else if _, err := os.Stat(filepath.Join(out2, "symbols.tsv")); err != nil {
		return fmt.Errorf("no libnickel index in %s: run kfw index without -libs", out2)
	}

	// Write the outputs.
	if err := writeGzipJSON(filepath.Join(work, "libs.json.gz"), fsx.Libs); err != nil {
		return err
	}
	sort.Strings(fsx.Files)
	if err := os.WriteFile(filepath.Join(work, "files.txt"), []byte(strings.Join(fsx.Files, "\n")+"\n"), 0o644); err != nil {
		return err
	}
	if err := os.MkdirAll(out2, 0o755); err != nil {
		return err
	}
	for _, f := range outputs {
		if err := os.Rename(filepath.Join(work, f), filepath.Join(out2, f)); err != nil {
			return err
		}
	}
	m.Library = "/" + libnickel
	if m.LibrarySHA256 != "" && m.LibrarySHA256 != fsx.NickelSHA {
		return fmt.Errorf("libnickel in %s differs from the index (%s, not %s)", rootfsImage(m.Firmware), fsx.NickelSHA, m.LibrarySHA256)
	}
	m.LibrarySHA256 = fsx.NickelSHA
	m.QtMajor, m.QtVersion = fsx.QtMajor, fsx.QtVersion
	if m.Format == "" {
		m.Format = "koboroot"
		if m.QtMajor >= 6 {
			m.Format = "rootfs"
		}
	}
	m.Generated = time.Now().UTC().Format(time.RFC3339)
	if full {
		tc, _ := os.ReadFile(filepath.Join(work, "toolchain.txt"))
		m.Toolchain = strings.TrimSpace(string(tc)) + " (" + toolImage + ")"
	}
	m.Counts = map[string]int{
		"symbols":    countLines(filepath.Join(out2, "symbols.tsv")) - 1,
		"calls":      countLines(filepath.Join(out2, "calls.tsv")) - 1,
		"qt_classes": countQtClasses(filepath.Join(out2, "qtmeta.json")),
		"libraries":  len(fsx.Libs),
		"files":      len(fsx.Files),
	}
	b, _ := json.MarshalIndent(m, "", "  ")
	fmt.Println(string(b))
	return os.WriteFile(filepath.Join(out2, "manifest.json"), append(b, '\n'), 0o644)
}

// libnickelIndex writes symbols.tsv, calls.tsv, strings.txt, toolchain.txt
// and qtmeta.json of libnickel to work.
func libnickelIndex(eng, mount, work string) error {
	fmt.Println("reading symbols, calls and strings of libnickel (this takes 15 to 60 minutes)")
	c := exec.Command(eng, "run", "--rm", "--mount", mount, "-v", hostPath(work)+":/w", toolImage, "sh", "-eu", "-c", binutils)
	c.Stdout, c.Stderr = os.Stdout, os.Stderr
	if err := c.Run(); err != nil {
		return err
	}
	qf, err := os.Create(filepath.Join(work, "qtmeta.json"))
	if err != nil {
		return err
	}
	err = qtmeta.Run(filepath.Join(work, "libnickel"), qf, io.Discard)
	if cerr := qf.Close(); err == nil {
		err = cerr
	}
	return err
}

func countLines(p string) int {
	b, _ := os.ReadFile(p)
	return bytes.Count(b, []byte{'\n'})
}

func countQtClasses(p string) int {
	b, _ := os.ReadFile(p)
	var m map[string]json.RawMessage
	json.Unmarshal(b, &m)
	return len(m)
}

func writeGzipJSON(p string, v any) error {
	f, err := os.Create(p)
	if err != nil {
		return err
	}
	zw := gzip.NewWriter(f)
	if err := json.NewEncoder(zw).Encode(v); err != nil {
		f.Close()
		return err
	}
	if err := zw.Close(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func readGzipJSON(p string, v any) error {
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	return json.NewDecoder(zr).Decode(v)
}

// ---- rootfs scan ---------------------------------------------------------

// Lib is one ELF shared object or executable of a firmware or a package.
type Lib struct {
	Path    string   `json:"path"`
	Soname  string   `json:"soname,omitempty"`
	Needed  []string `json:"needed,omitempty"`
	Runpath []string `json:"runpath,omitempty"`
	// Exports are the defined dynamic symbols, as name or name@version.
	Exports []string `json:"exports,omitempty"`
}

type rootfsScan struct {
	Libs      []Lib
	Files     []string
	NickelSHA string
	QtMajor   int
	QtVersion string
}

var reQtCore = regexp.MustCompile(`/libQt([0-9])Core\.so\.([0-9]+\.[0-9]+\.[0-9]+)$`)

// maxELF is the largest ELF file that kfw reads into memory.
const maxELF = 512 << 20

// scanRootfs reads a tar stream of a root file system. It lists the files,
// reads each shared object and the nickel executable, and writes libnickel
// to nickelOut.
func scanRootfs(r io.Reader, nickelOut string) (*rootfsScan, error) {
	s := &rootfsScan{}
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		name := strings.TrimPrefix(filepath.ToSlash(filepath.Clean("/"+h.Name)), "/")
		if name == "" || name == "." {
			continue
		}
		if h.Typeflag == tar.TypeReg || h.Typeflag == tar.TypeSymlink {
			s.Files = append(s.Files, "/"+name)
		}
		if mm := reQtCore.FindStringSubmatch("/" + name); mm != nil && s.QtVersion == "" {
			s.QtMajor = int(mm[1][0] - '0')
			s.QtVersion = mm[2]
		}
		if h.Typeflag != tar.TypeReg || h.Size < 52 || h.Size > maxELF {
			continue
		}
		b, err := io.ReadAll(tr)
		if err != nil {
			return nil, err
		}
		if !bytes.HasPrefix(b, []byte("\x7fELF")) {
			continue
		}
		if name == libnickel {
			sum := sha256.Sum256(b)
			s.NickelSHA = hex.EncodeToString(sum[:])
			if err := os.WriteFile(nickelOut, b, 0o644); err != nil {
				return nil, err
			}
		}
		l, ok := readLib("/"+name, b, name == nickelExec)
		if ok {
			s.Libs = append(s.Libs, l)
		}
	}
	if s.NickelSHA == "" {
		return nil, fmt.Errorf("no %s in the rootfs", libnickel)
	}
	return s, nil
}

// readLib reads the dynamic section and the exports of an ELF file. It
// takes shared objects, and executables only if exec is true.
func readLib(p string, b []byte, exec bool) (Lib, bool) {
	f, err := elf.NewFile(bytes.NewReader(b))
	if err != nil {
		return Lib{}, false
	}
	defer f.Close()
	if f.Type != elf.ET_DYN && !exec {
		return Lib{}, false
	}
	l := Lib{Path: p}
	if v, _ := f.DynString(elf.DT_SONAME); len(v) > 0 {
		l.Soname = v[0]
	}
	l.Needed, _ = f.DynString(elf.DT_NEEDED)
	rp, _ := f.DynString(elf.DT_RUNPATH)
	rp2, _ := f.DynString(elf.DT_RPATH)
	for _, x := range append(rp, rp2...) {
		l.Runpath = append(l.Runpath, strings.Split(x, ":")...)
	}
	vers := map[uint16]string{}
	if dv, err := f.DynamicVersions(); err == nil {
		for _, v := range dv {
			if v.Flags&elf.VER_FLG_BASE == 0 {
				vers[v.Index] = v.Name
			}
		}
	}
	syms, _ := f.DynamicSymbols()
	seen := map[string]bool{}
	for _, s := range syms {
		if s.Section == elf.SHN_UNDEF || s.Name == "" {
			continue
		}
		// STB_LOOS (10) is STB_GNU_UNIQUE on Linux. libQt6Core exports
		// template statics such as QMetaTypeInterfaceWrapper<T>::metaType
		// with it, and the dynamic linker resolves them like globals.
		if b := elf.ST_BIND(s.Info); b != elf.STB_GLOBAL && b != elf.STB_WEAK && b != elf.STB_LOOS {
			continue
		}
		e := s.Name
		if s.HasVersion {
			if v, ok := vers[s.VersionIndex.Index()]; ok {
				e += "@" + v
			}
		}
		if !seen[e] {
			seen[e] = true
			l.Exports = append(l.Exports, e)
		}
	}
	// A shared object without a soname is a plugin or a PIE executable.
	// Keep its dynamic section, but nothing links against its exports.
	if l.Soname == "" && p != "/"+nickelExec {
		l.Exports = nil
	}
	return l, true
}

// importOrRemove runs c | importCmd. If one of them fails, it removes the
// image, because an import of a broken tar stream can still make an image.
func importOrRemove(eng, img string, c, importCmd *exec.Cmd) error {
	err := pipe(c, importCmd)
	if err != nil {
		exec.Command(eng, "rmi", "-f", img).Run()
	}
	return err
}
