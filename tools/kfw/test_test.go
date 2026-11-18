package main

import (
	"strings"
	"testing"
)

// fakeFirmware is a firmware with libc, one Qt core library, libnickel and
// the nickel executable.
func fakeFirmware(format string, qt int) *fwLibs {
	qtSoname := map[int]string{5: "libQt5Core.so.5", 6: "libQt6Core.so.6"}[qt]
	fw := &fwLibs{Version: "9.9.9", Format: format, QtMajor: qt, QtVersion: map[int]string{5: "5.2.1", 6: "6.5.2"}[qt],
		Libs: []Lib{
			{Path: "/lib/libc.so.6", Soname: "libc.so.6", Exports: []string{"memcpy@GLIBC_2.4", "free@GLIBC_2.4"}},
			{Path: "/usr/lib/" + qtSoname, Soname: qtSoname, Needed: []string{"libc.so.6"}, Exports: []string{"qt_func@Qt_" + string(rune('0'+qt))}},
			{Path: "/usr/local/Kobo/libnickel.so.1.0.0", Soname: "libnickel.so.1", Needed: []string{qtSoname}, Exports: []string{"_ZN6Nickel3fooEv"}},
			{Path: "/" + nickelExec, Needed: []string{"libnickel.so.1", qtSoname, "libc.so.6"}},
		},
		bySoname: map[string]*Lib{}, files: map[string]bool{"/usr/local/Kobo/nickel": true},
		nickelSyms: map[string]bool{"_ZN6Nickel3fooEv": true},
	}
	for i := range fw.Libs {
		if s := fw.Libs[i].Soname; s != "" {
			fw.bySoname[s] = &fw.Libs[i]
		}
	}
	return fw
}

func plugin(dest string, qt int, undef ...string) *pkgFile {
	e := &pkgELF{QtMajor: qt, QtMinor: 0, Lib: Lib{Path: dest, Needed: []string{map[int]string{5: "libQt5Core.so.5", 6: "libQt6Core.so.6"}[qt], "libc.so.6"}}}
	for _, u := range undef {
		n, v, _ := strings.Cut(u, "@")
		e.Undef = append(e.Undef, symRef{Name: n, Version: v})
	}
	e.Mangled = []string{"_ZN6Nickel3fooEv"}
	return &pkgFile{Dest: dest, ELF: e}
}

func levels(fs []finding) string {
	var b []string
	for _, f := range fs {
		b = append(b, f.Level+": "+f.Text)
	}
	return strings.Join(b, "\n")
}

func TestPackage(t *testing.T) {
	qt5 := plugin("/usr/local/Kobo/imageformats/libmod.so", 5, "memcpy@GLIBC_2.4", "qt_func@Qt_5", "_ZN6Nickel3fooEv")
	qt6 := plugin("/usr/local/Kobo/imageformats/libmod-qt6.so", 6, "memcpy@GLIBC_2.4", "qt_func@Qt_6")
	fw4, fw6 := fakeFirmware("koboroot", 5), fakeFirmware("rootfs", 6)

	// One Kobo.tgz with both builds works on both firmwares.
	for _, fw := range []*fwLibs{fw4, fw6} {
		fs := testFirmware([]*pkgFile{qt5, qt6}, "kobo", fw)
		if st := status(fs); st != "OK" {
			t.Errorf("dual Kobo.tgz on Qt %d: %s\n%s", fw.QtMajor, st, levels(fs))
		}
	}
	// 6.x does not install KoboRoot.tgz, and a Qt 5 build alone does not load on Qt 6.
	fs := testFirmware([]*pkgFile{qt5}, "koboroot", fw6)
	if st := status(fs); st != "FAIL" || !strings.Contains(levels(fs), "does not install KoboRoot.tgz") || !strings.Contains(levels(fs), "No plugin in the package is built for Qt 6") {
		t.Errorf("Qt 5 KoboRoot.tgz on 6.x:\n%s", levels(fs))
	}
	// A symbol version that the firmware lacks fails; a weak one does not.
	bad := plugin("/usr/local/Kobo/imageformats/libmod.so", 5, "memcpy@GLIBC_2.34")
	bad.ELF.Undef = append(bad.ELF.Undef, symRef{Name: "maybe", Weak: true})
	fs = testFirmware([]*pkgFile{bad}, "kobo", fw4)
	if !strings.Contains(levels(fs), "memcpy@GLIBC_2.34") || strings.Contains(levels(fs), "maybe") {
		t.Errorf("versioned symbol:\n%s", levels(fs))
	}
	// A missing library and a mangled name that libnickel lacks.
	miss := plugin("/usr/local/Kobo/imageformats/libmod.so", 5)
	miss.ELF.Needed = append(miss.ELF.Needed, "libgone.so.1")
	miss.ELF.Mangled = []string{"_ZN6Nickel3barEv"}
	fs = testFirmware([]*pkgFile{miss}, "kobo", fw4)
	if !strings.Contains(levels(fs), "needs `libgone.so.1`") || !strings.Contains(levels(fs), "WARN") {
		t.Errorf("missing library and name:\n%s", levels(fs))
	}
}
