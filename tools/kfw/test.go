package main

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"debug/elf"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"nickelsymbols/modcheck"
)

// A package is a Kobo.tgz or a KoboRoot.tgz. The firmware extracts
// /mnt/onboard/.kobo/Kobo.tgz into /usr/local/Kobo (4.x rcS and the 6.x ota
// script) and /mnt/onboard/.kobo/KoboRoot.tgz into / (4.x only: 6.x has no
// code for it).

type pkgFile struct {
	Dest string // the install path on the device
	ELF  *pkgELF
}

type pkgELF struct {
	Lib
	Undef    []symRef
	QtMajor  int // of the .qtmetadata section; 0 if the file is no Qt plugin
	QtMinor  int // -1 if the metadata does not say
	Mangled  []string
	DevBuild bool     // a NickelHook build without a version: it logs "version: dev"
	Problems []string // architecture problems
	Static   bool
}

type symRef struct {
	Name, Version string
	Weak          bool
}

type finding struct {
	Level, Text string // Level is FAIL, WARN or INFO
}

// fwLibs is the library index of one firmware.
type fwLibs struct {
	Version, Format string
	QtMajor         int
	QtVersion       string
	Libs            []Lib
	bySoname        map[string]*Lib
	files           map[string]bool
	nickelSyms      map[string]bool
}

func loadFwLibs(dir string) (*fwLibs, error) {
	var m manifest
	b, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	fw := &fwLibs{Version: filepath.Base(dir), Format: m.Format, QtMajor: m.QtMajor, QtVersion: m.QtVersion,
		bySoname: map[string]*Lib{}, files: map[string]bool{}, nickelSyms: map[string]bool{}}
	if err := readGzipJSON(filepath.Join(dir, "libs.json.gz"), &fw.Libs); err != nil {
		return nil, err
	}
	// Prefer the library in the default search path when a soname is twice.
	rank := func(p string) int {
		for i, d := range []string{"/usr/local/Kobo/", "/lib/", "/usr/lib/", "/usr/local/Qt-5.2.1-arm/lib/"} {
			if path.Dir(p)+"/" == d {
				return i
			}
		}
		return 9
	}
	for i := range fw.Libs {
		l := &fw.Libs[i]
		if l.Soname == "" {
			continue
		}
		if o, ok := fw.bySoname[l.Soname]; !ok || rank(l.Path) < rank(o.Path) {
			fw.bySoname[l.Soname] = l
		}
	}
	if err := eachLineOf(filepath.Join(dir, "files.txt"), func(l string) { fw.files[l] = true }); err != nil {
		return nil, err
	}
	err = eachLineOf(filepath.Join(dir, "symbols.tsv"), func(l string) {
		if f := strings.Split(l, "\t"); len(f) >= 5 {
			fw.nickelSyms[f[4]] = true
		}
	})
	return fw, err
}

func eachLineOf(p string, fn func(string)) error {
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 1<<20), 1<<20)
	for s.Scan() {
		fn(s.Text())
	}
	return s.Err()
}

// readPackage reads the tar.gz at p. kind is "kobo" or "koboroot".
func readPackage(p, kind string) ([]*pkgFile, []finding, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return nil, nil, err
	}
	var files []*pkgFile
	var fs []finding
	tr := tar.NewReader(zr)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, nil, err
		}
		name := h.Name
		clean := path.Clean("/" + name)
		if strings.HasPrefix(name, "/") || slices.Contains(strings.Split(name, "/"), "..") {
			fs = append(fs, finding{"FAIL", fmt.Sprintf("`%s`: an absolute path or `..`. The firmware extracts it as root.", name)})
			continue
		}
		dest := clean
		if kind == "kobo" {
			dest = path.Join("/usr/local/Kobo", clean)
		}
		switch h.Typeflag {
		case tar.TypeDir:
			continue
		case tar.TypeReg:
		case tar.TypeSymlink:
			if path.IsAbs(h.Linkname) || strings.HasPrefix(path.Clean(path.Join(path.Dir(dest), h.Linkname)), "/mnt/onboard") {
				fs = append(fs, finding{"INFO", fmt.Sprintf("`%s` links to `%s`.", dest, h.Linkname)})
			}
			files = append(files, &pkgFile{Dest: dest})
			continue
		default:
			fs = append(fs, finding{"WARN", fmt.Sprintf("`%s`: tar entry type %q. Use only files, folders and symlinks.", name, h.Typeflag)})
			continue
		}
		if h.Mode&0o6000 != 0 {
			fs = append(fs, finding{"WARN", fmt.Sprintf("`%s` has the setuid or setgid bit.", dest)})
		}
		pf := &pkgFile{Dest: dest}
		files = append(files, pf)
		if h.Size < 52 || h.Size > maxELF {
			continue
		}
		b, err := io.ReadAll(tr)
		if err != nil {
			return nil, nil, err
		}
		if bytes.HasPrefix(b, []byte("\x7fELF")) {
			pf.ELF = analyzeELF(dest, b)
		}
	}
	return files, fs, nil
}

var reMangled = regexp.MustCompile(`^_Z[A-Za-z0-9_]{4,}$`)

func analyzeELF(dest string, b []byte) *pkgELF {
	e := &pkgELF{QtMinor: -1}
	f, err := elf.NewFile(bytes.NewReader(b))
	if err != nil {
		e.Problems = append(e.Problems, "not a valid ELF file: "+err.Error())
		return e
	}
	defer f.Close()
	if f.Class != elf.ELFCLASS32 || f.Machine != elf.EM_ARM {
		e.Problems = append(e.Problems, fmt.Sprintf("built for %s %s, not 32-bit ARM", f.Class, f.Machine))
		return e
	}
	// e_flags is at offset 0x24 of a 32-bit ELF header.
	flags := binary.LittleEndian.Uint32(b[0x24:])
	if flags&0x200 != 0 { // EF_ARM_ABI_FLOAT_SOFT
		e.Problems = append(e.Problems, "built for soft-float. Kobo is armhf (hard-float).")
	}
	l, _ := readLib(dest, b, true)
	l.Exports = nil
	e.Lib = l
	if f.Section(".dynamic") == nil {
		e.Static = true
		return e
	}
	own := map[string]bool{}
	syms, _ := f.DynamicSymbols()
	for _, s := range syms {
		if s.Name == "" {
			continue
		}
		if s.Section != elf.SHN_UNDEF {
			own[s.Name] = true
			e.Exports = append(e.Exports, s.Name)
			continue
		}
		e.Undef = append(e.Undef, symRef{s.Name, s.Version, elf.ST_BIND(s.Info) == elf.STB_WEAK})
	}
	e.QtMajor, e.QtMinor = qtPluginVersion(f)
	// Mangled names in the read-only data: NickelHook resolves them with
	// dlsym in libnickel.
	if s := f.Section(".rodata"); s != nil {
		if d, err := s.Data(); err == nil {
			ws := bytes.Split(d, []byte{0})
			// NickelHook.h defines NH_VERSION as "dev" when the build passes
			// no VERSION (a build image without git).
			e.DevBuild = bytes.Contains(d, []byte("(NickelHook) initializing")) &&
				slices.ContainsFunc(ws, func(w []byte) bool { return string(w) == "dev" })
			for _, w := range ws {
				n := string(w)
				if reMangled.MatchString(n) && !own[n] && !slices.ContainsFunc(e.Undef, func(r symRef) bool { return r.Name == n }) {
					e.Mangled = append(e.Mangled, n)
				}
			}
		}
	}
	sort.Strings(e.Mangled)
	e.Mangled = slices.Compact(e.Mangled)
	return e
}

// qtPluginVersion reads the Qt version from the plugin metadata of f. It
// returns major 0 if f is no Qt plugin, and minor -1 if the metadata does
// not give it. The Qt plugin loader reads the same data without dlopen,
// and refuses a plugin of another major version:
//   - Qt 5.0 to 5.12: section .qtmetadata, "QTMETADATA  " and binary JSON.
//   - Qt 5.13 to 6.1: section .qtmetadata, "QTMETADATA !", then the bytes
//     version, Qt major, Qt minor, architecture, then CBOR.
//   - Qt 6.2 and later: ELF note "qt-project!" in .note.qt.metadata, with
//     the same 4 header bytes, then CBOR.
func qtPluginVersion(f *elf.File) (major, minor int) {
	if s := f.Section(".note.qt.metadata"); s != nil {
		if d, err := s.Data(); err == nil && len(d) >= 12 {
			namesz := binary.LittleEndian.Uint32(d[0:])
			off := 12 + (int(namesz)+3)&^3
			if namesz == 12 && string(d[12:23]) == "qt-project!" && off+3 <= len(d) {
				return int(d[off+1]), int(d[off+2])
			}
		}
	}
	if s := f.Section(".qtmetadata"); s != nil {
		if d, err := s.Data(); err == nil {
			if i := bytes.Index(d, []byte("QTMETADATA !")); i >= 0 && i+15 <= len(d) {
				return int(d[i+13]), int(d[i+14])
			}
			if bytes.Contains(d, []byte("QTMETADATA  ")) {
				return 5, -1
			}
		}
	}
	return 0, -1
}

// pluginDir reports whether Qt scans dir for plugins on this firmware. Qt
// scans the folder of the executable (/usr/local/Kobo) and its own plugin
// folder.
func pluginDir(dir string, fw *fwLibs) bool {
	for _, base := range []string{"/usr/local/Kobo/", "/usr/local/Qt-5.2.1-arm/plugins/", "/usr/lib/plugins/"} {
		if strings.HasPrefix(dir+"/", base) && strings.Count(strings.TrimPrefix(dir+"/", base), "/") == 1 {
			return true
		}
	}
	return false
}

// resolver finds libraries by soname: the package first, then the firmware.
type resolver struct {
	fw  *fwLibs
	pkg map[string]*Lib
}

func (r *resolver) find(soname string) *Lib {
	if l, ok := r.pkg[soname]; ok {
		return l
	}
	return r.fw.bySoname[soname]
}

// closure returns the libraries that loading the roots brings in, and the
// sonames that are missing.
func (r *resolver) closure(roots ...[]string) (libs []*Lib, missing []string) {
	seen := map[string]bool{}
	var queue []string
	for _, n := range roots {
		queue = append(queue, n...)
	}
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		if seen[n] {
			continue
		}
		seen[n] = true
		l := r.find(n)
		if l == nil {
			missing = append(missing, n)
			continue
		}
		libs = append(libs, l)
		queue = append(queue, l.Needed...)
	}
	return libs, missing
}

func testFirmware(files []*pkgFile, kind string, fw *fwLibs) []finding {
	var fs []finding
	if kind == "koboroot" && fw.Format == "rootfs" {
		fs = append(fs, finding{"FAIL", fmt.Sprintf("Firmware %s does not install KoboRoot.tgz: its boot script reads only `.kobo/Kobo.tgz` (into /usr/local/Kobo) and `.kobo/update.tar`.", fw.Version)})
	}
	r := &resolver{fw: fw, pkg: map[string]*Lib{}}
	for _, f := range files {
		if f.ELF != nil && f.ELF.Soname != "" {
			l := f.ELF.Lib
			l.Exports = f.ELF.Exports
			r.pkg[l.Soname] = &l
		}
		if fw.files[f.Dest] {
			fs = append(fs, finding{"WARN", fmt.Sprintf("`%s` replaces a file of the firmware.", f.Dest)})
		}
	}
	nickel := fw.bySoname[""]
	for i := range fw.Libs {
		if fw.Libs[i].Path == "/"+nickelExec {
			nickel = &fw.Libs[i]
		}
	}
	plugins, matching := 0, 0
	for _, f := range files {
		e := f.ELF
		if e == nil {
			continue
		}
		for _, p := range e.Problems {
			fs = append(fs, finding{"FAIL", fmt.Sprintf("`%s`: %s", f.Dest, p)})
		}
		if len(e.Problems) > 0 || e.Static {
			continue
		}
		var roots [][]string
		isPlugin := e.QtMajor > 0 && pluginDir(path.Dir(f.Dest), fw)
		if isPlugin {
			plugins++
			if e.QtMajor != fw.QtMajor {
				fs = append(fs, finding{"INFO", fmt.Sprintf("`%s` is a Qt %d plugin. Qt %d skips it without loading it.", f.Dest, e.QtMajor, fw.QtMajor)})
				continue
			}
			if minor := qtMinor(fw.QtVersion); e.QtMinor > minor && minor >= 0 {
				fs = append(fs, finding{"FAIL", fmt.Sprintf("`%s` is built for Qt %d.%d. The firmware has Qt %s, and Qt refuses a plugin of a newer minor version.", f.Dest, e.QtMajor, e.QtMinor, fw.QtVersion)})
				continue
			}
			matching++
			if e.DevBuild {
				fs = append(fs, finding{"WARN", fmt.Sprintf("`%s` is a NickelHook build with version `dev`. Pass `VERSION=<tag>` to make (the build image has no git).", f.Dest)})
			}
			// A plugin loads into Nickel: Nickel's libraries are in scope.
			if nickel != nil {
				roots = append(roots, nickel.Needed, []string{"libnickel.so.1"})
			}
		}
		roots = append(roots, e.Needed)
		libs, missing := r.closure(roots...)
		for _, n := range missing {
			fs = append(fs, finding{"FAIL", fmt.Sprintf("`%s` needs `%s`, which the firmware does not have.", f.Dest, n)})
		}
		exact, names := map[string]bool{}, map[string]bool{}
		for _, l := range libs {
			for _, x := range l.Exports {
				exact[x] = true
				n, _, _ := strings.Cut(x, "@")
				names[n] = true
			}
		}
		for _, x := range e.Exports {
			names[x] = true
		}
		var unresolved []string
		for _, s := range e.Undef {
			ok := names[s.Name]
			if s.Version != "" {
				ok = exact[s.Name+"@"+s.Version]
			}
			if !ok && !s.Weak {
				n := s.Name
				if s.Version != "" {
					n += "@" + s.Version
				}
				unresolved = append(unresolved, n)
			}
		}
		if len(unresolved) > 0 {
			fs = append(fs, finding{"FAIL", fmt.Sprintf("`%s` uses %d symbols that the firmware libraries do not export: %s", f.Dest, len(unresolved), list(unresolved, 8))})
		}
		if isPlugin {
			var gone []string
			for _, n := range e.Mangled {
				if !fw.nickelSyms[n] && !names[n] {
					gone = append(gone, n)
				}
			}
			if len(gone) > 0 {
				fs = append(fs, finding{"WARN", fmt.Sprintf("`%s` has %d mangled names that libnickel does not have. If NickelHook resolves them with dlsym, a required one stops the mod and an optional one turns a feature off. Run `kfw check` on the source to know which: %s", f.Dest, len(gone), list(gone, 6))})
			}
		}
	}
	if plugins > 0 && matching == 0 {
		fs = append(fs, finding{"FAIL", fmt.Sprintf("No plugin in the package is built for Qt %d, so Nickel loads nothing of the mod.", fw.QtMajor)})
	}
	return fs
}

func qtMinor(v string) int {
	p := strings.Split(v, ".")
	if len(p) < 2 {
		return -1
	}
	n := 0
	for _, c := range p[1] {
		n = n*10 + int(c-'0')
	}
	return n
}

func list(s []string, n int) string {
	q := make([]string, 0, n)
	for i, x := range s {
		if i == n {
			q = append(q, fmt.Sprintf("and %d more", len(s)-n))
			break
		}
		q = append(q, "`"+x+"`")
	}
	return strings.Join(q, ", ")
}

func status(fs []finding) string {
	st := "OK"
	for _, f := range fs {
		if f.Level == "FAIL" {
			return "FAIL"
		}
		if f.Level == "WARN" {
			st = "WARN"
		}
	}
	return st
}

func cmdTest(args []string) (int, error) {
	flags := flag.NewFlagSet("test", flag.ContinueOnError)
	as := flags.String("as", "", "the install file: kobo (Kobo.tgz, into /usr/local/Kobo) or koboroot (KoboRoot.tgz, into /). Default: from the file name.")
	only := flags.String("fw", "", "test only these firmware versions (comma-separated)")
	out := flags.String("out", "", "write the report to this file instead of stdout")
	if err := flags.Parse(args); err != nil {
		return 2, nil
	}
	if flags.NArg() != 1 {
		return 2, fmt.Errorf("usage: kfw test [-as kobo|koboroot] [-fw X.Y.Z,...] <Kobo.tgz|KoboRoot.tgz>")
	}
	pkg := flags.Arg(0)
	kind, err := pkgKind(pkg, *as)
	if err != nil {
		return 2, err
	}
	dir, err := root()
	if err != nil {
		return 2, err
	}
	files, global, err := readPackage(pkg, kind)
	if err != nil {
		return 2, err
	}
	idx, _ := filepath.Glob(filepath.Join(dir, "firmware", "*", "libs.json.gz"))
	var fws []*fwLibs
	var skipped []string
	all, _ := filepath.Glob(filepath.Join(dir, "firmware", "*", "manifest.json"))
	for _, m := range all {
		v := filepath.Base(filepath.Dir(m))
		if !slices.ContainsFunc(idx, func(p string) bool { return filepath.Base(filepath.Dir(p)) == v }) {
			skipped = append(skipped, v)
		}
	}
	for _, p := range idx {
		v := filepath.Base(filepath.Dir(p))
		if *only != "" && !slices.Contains(strings.Split(*only, ","), v) {
			continue
		}
		fw, err := loadFwLibs(filepath.Dir(p))
		if err != nil {
			return 2, err
		}
		fws = append(fws, fw)
	}
	if len(fws) == 0 {
		return 2, fmt.Errorf("no firmware with a library index (libs.json.gz): run kfw index first")
	}
	sort.Slice(fws, func(i, j int) bool { return modcheck.VersionLess(fws[j].Version, fws[i].Version) })

	w := io.Writer(os.Stdout)
	if *out != "" {
		f, err := os.Create(*out)
		if err != nil {
			return 2, err
		}
		defer f.Close()
		w = f
	}
	installAs := map[string]string{"kobo": "Kobo.tgz, extracted into /usr/local/Kobo", "koboroot": "KoboRoot.tgz, extracted into /"}[kind]
	fmt.Fprintf(w, "# Package test: %s\n\nInstall file: %s. Files: %d.\n\n", filepath.Base(pkg), installAs, len(files))
	fmt.Fprintln(w, "| Firmware | Qt | Result |\n|---|---|---|")
	results := map[string][]finding{}
	failed := false
	for _, fw := range fws {
		fs := append(append([]finding{}, global...), testFirmware(files, kind, fw)...)
		results[fw.Version] = fs
		st := status(fs)
		failed = failed || st == "FAIL"
		fmt.Fprintf(w, "| %s | %s | %s |\n", fw.Version, fw.QtVersion, st)
	}
	fmt.Fprint(w, "\n## Files\n\n")
	for _, f := range files {
		d := ""
		if e := f.ELF; e != nil {
			d = " (ELF"
			if e.QtMajor > 0 {
				d += fmt.Sprintf(", Qt %d plugin", e.QtMajor)
			}
			if len(e.Needed) > 0 {
				d += ", needs " + strings.Join(e.Needed, " ")
			}
			d += ")"
		}
		fmt.Fprintf(w, "- `%s`%s\n", f.Dest, d)
	}
	for _, fw := range fws {
		if len(results[fw.Version]) == 0 {
			continue
		}
		fmt.Fprintf(w, "\n## %s: %s\n\n", fw.Version, status(results[fw.Version]))
		for _, f := range results[fw.Version] {
			fmt.Fprintf(w, "- %s: %s\n", f.Level, f.Text)
		}
	}
	if len(skipped) > 0 {
		fmt.Fprintf(w, "\nNot tested (no library index; run `kfw index` with the firmware file): %s.\n", strings.Join(skipped, ", "))
	}
	if failed {
		return 1, nil
	}
	return 0, nil
}
