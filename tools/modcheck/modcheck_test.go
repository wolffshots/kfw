package modcheck

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeIndex writes a minimal firmware index.
func writeIndex(t *testing.T, dir, version string, qt int, syms, meta, strs string) *firmware {
	t.Helper()
	d := filepath.Join(dir, version)
	os.MkdirAll(d, 0o755)
	os.WriteFile(filepath.Join(d, "manifest.json"), []byte(`{"qt_major": `+string(rune('0'+qt))+`}`), 0o644)
	os.WriteFile(filepath.Join(d, "symbols.tsv"), []byte("address\tsize\ttype\tbind\tmangled\tdemangled\n"+syms), 0o644)
	os.WriteFile(filepath.Join(d, "qtmeta.json"), []byte(meta), 0o644)
	os.WriteFile(filepath.Join(d, "strings.txt"), []byte(strs), 0o644)
	fw, err := loadFirmware(d)
	if err != nil {
		t.Fatal(err)
	}
	return fw
}

const modSource = `
class Mine : public QObject {
    Q_OBJECT
public slots:
    void onSync();
signals:
    void done();
};
void f(QWidget *w) {
    connect(sync, SIGNAL(finished()), this, SLOT(onSync()));    // Nickel signal, own slot
    connect(t, SIGNAL(timeout()), this, SIGNAL(done()));        // Qt signal, own signal
    if (w->inherits("ReadingView")) {}
    w->findChild<QLabel *>(QStringLiteral("settingsIcon"));
    QMetaObject::invokeMethod(w, "onResume");
}
static struct nh_dlsym D[] = {
    { .name = "_ZN4Sync4syncEv", .out = 0 },
    { .name = "_ZN4Sync4iconEv", .out = 0, .desc = "icon", .optional = true },
    {0},
};
`

func TestCheck(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "mod.cc")
	os.WriteFile(src, []byte(modSource), 0o644)
	m := &mod{Name: "test", Sources: []string{src}, Qts: []int{5}}
	if err := m.scan(); err != nil {
		t.Fatal(err)
	}

	syms := "0\t0\tFUNC\tGLOBAL\t_ZN4Sync4syncEv\tSync::sync()\n0\t0\tFUNC\tGLOBAL\t_ZN4Sync4iconEv\tSync::icon()\n"
	meta := `{"Sync": {"signals": [{"signature": "finished()"}]}, "ReadingView": {"slots": [{"signature": "onResume()"}]}}`
	base := writeIndex(t, dir, "4.0.1", 5, syms, meta, "settingsIcon\n")
	if r := check(m, base, base); r.Status != "OK" {
		t.Fatalf("baseline: %s %v", r.Status, r.Findings)
	}

	// The optional symbol has a new signature, the signal and the class are gone.
	syms2 := "0\t0\tFUNC\tGLOBAL\t_ZN4Sync4syncEv\tSync::sync()\n0\t0\tFUNC\tGLOBAL\t_ZN4Sync4iconEi\tSync::icon(int)\n"
	meta2 := `{"Sync": {"signals": [{"signature": "finished(int)"}]}, "Reader": {"slots": [{"signature": "onResume()"}]}}`
	fw := writeIndex(t, dir, "4.0.2", 5, syms2, meta2, "settingsIcon\n")
	r := check(m, base, fw)
	got := ""
	for _, f := range r.Findings {
		got += f.Text + "\n"
	}
	for _, want := range []string{"Missing optional symbol `Sync::icon()` (icon)", "Changed signature, the firmware has: `Sync::icon(int)`",
		"Missing signal `finished()`", "`Sync::finished(int)`", "Missing class `ReadingView`", "Missing method `onResume`"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "timeout") || strings.Contains(got, "onSync") || strings.Contains(got, "done()") {
		t.Errorf("reported a Qt or own name:\n%s", got)
	}
	if r.Status != "DEGRADED" {
		t.Errorf("status %s, want DEGRADED", r.Status)
	}

	// Qt 6 firmware without the required symbol fails.
	fw6 := writeIndex(t, dir, "6.0.1", 6, "", `{}`, "")
	if r := check(m, base, fw6); r.Status != "FAIL" || !strings.Contains(r.Findings[0].Text, "Qt ABI") || !strings.Contains(r.Findings[1].Text, "Missing required symbol `Sync::sync()`") {
		t.Errorf("qt6: %s %v", r.Status, r.Findings)
	}
}

func TestQtLineMasks(t *testing.T) {
	src := "a\n#if QT_VERSION_MAJOR >= 6\nb\n#else\nc\n#endif\n#if QT_VERSION < QT_VERSION_CHECK(6, 0, 0)\nd\n#elif defined(X)\ne\n#endif\n#ifdef X\nf\n#endif\n"
	got := qtLineMasks(src)
	want := map[int]uint8{0: qtAll, 2: qtBit(6), 4: qtBit(5), 7: qtBit(5), 9: qtBit(6), 12: qtAll}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("line %d (%q): mask %b, want %b", i+1, strings.Split(src, "\n")[i], got[i], w)
		}
	}
}

// A dual source: the Qt 5 branch uses a class that 6.x does not have.
func TestCheckDualQt(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "mod.cc")
	os.WriteFile(src, []byte("#if QT_VERSION_MAJOR < 6\nbool f(QObject *o) { return o->inherits(\"OldReader\"); }\n#endif\n"), 0o644)
	m := &mod{Name: "dual", Sources: []string{src}, Qts: parseQts("5,6")}
	if err := m.scan(); err != nil {
		t.Fatal(err)
	}
	fw5 := writeIndex(t, dir, "4.0.1", 5, "", `{"OldReader": {}}`, "")
	fw6 := writeIndex(t, dir, "6.0.1", 6, "", `{}`, "")
	bases := pickBaselines(m, []*firmware{fw6, fw5})
	if bases[5] != fw5 || bases[6] != fw6 {
		t.Fatalf("baselines: %v", bases)
	}
	if r := check(m, bases[6], fw6); r.Status != "OK" {
		t.Errorf("6.x: %s %v", r.Status, r.Findings)
	}
	if r := check(m, bases[5], fw5); r.Status != "OK" {
		t.Errorf("4.x: %s %v", r.Status, r.Findings)
	}
	m.Qts = []int{5} // a Qt 5 only build fails on 6.x for the ABI and the class
	if r := check(m, bases[5], fw6); r.Status != "FAIL" || len(r.Findings) != 2 {
		t.Errorf("qt5 on 6.x: %s %v", r.Status, r.Findings)
	}
}
