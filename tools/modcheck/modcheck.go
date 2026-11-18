// Command modcheck checks the Nickel names that a mod uses against each
// indexed firmware in ../firmware and writes a Markdown report.
//
// It reads the mod sources (.cc, .cpp, .c, .h, .hpp) and finds:
//   - mangled symbols ("_Z...") in the NickelHook nh_dlsym and nh_hook tables,
//     with .optional, and any other quoted mangled name (taken as required)
//   - SIGNAL(...) and SLOT(...) signatures (Qt string connect)
//   - QMetaObject::invokeMethod(obj, "name") method names
//   - class names in inherits("X"), qt_metacast("X"), className() compares
//     and ...ClassName("X") calls
//   - object names in findChild/findChildren("name") and
//     viewWithObjectName(..., "name")
//
// A name counts only if the baseline firmware has it, because a name that the
// baseline lacks is a Qt name, a name of the mod itself, or already broken.
// A required symbol always counts. The Qt major version of the mod must match
// the Qt major version of the firmware, or the mod does not load.
//
// Usage (kfw check passes its arguments to Main):
//
//	kfw check -index <firmware dir> -mods <mods.tsv> [-out report.md]
//	kfw check -index <firmware dir> [-baseline X.Y.Z[,X.Y.Z]] [-qt 5|6|5,6] <source file or dir>...
package modcheck

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ---- firmware index ----------------------------------------------------------

type method struct {
	Signature string `json:"signature"`
}

type metaClass struct {
	Signals []method `json:"signals"`
	Slots   []method `json:"slots"`
	Methods []method `json:"methods"`
}

type firmware struct {
	Version string
	Qt      int
	syms    map[string]string   // mangled -> demangled
	byName  map[string][]string // "Class::method" -> demangled overloads
	classes map[string]metaClass
	strs    map[string]bool
}

func loadFirmware(dir string) (*firmware, error) {
	fw := &firmware{Version: filepath.Base(dir), syms: map[string]string{}, byName: map[string][]string{}, strs: map[string]bool{}}
	var man struct {
		Qt int `json:"qt_major"`
	}
	if b, err := os.ReadFile(filepath.Join(dir, "manifest.json")); err == nil {
		json.Unmarshal(b, &man)
	}
	fw.Qt = man.Qt
	if fw.Qt == 0 && strings.HasPrefix(fw.Version, "4.") {
		fw.Qt = 5 // indexes made before qt_major existed are all firmware 4.x
	}
	if err := eachLine(filepath.Join(dir, "symbols.tsv"), func(l string) {
		f := strings.Split(l, "\t")
		if len(f) >= 6 && f[4] != "mangled" {
			fw.syms[f[4]] = f[5]
			// Constructor and destructor variants (C1, C2) demangle the same.
			if q := qualified(f[5]); !slices.Contains(fw.byName[q], f[5]) {
				fw.byName[q] = append(fw.byName[q], f[5])
			}
		}
	}); err != nil {
		return nil, err
	}
	b, err := os.ReadFile(filepath.Join(dir, "qtmeta.json"))
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &fw.classes); err != nil {
		return nil, err
	}
	if err := eachLine(filepath.Join(dir, "strings.txt"), func(l string) { fw.strs[l] = true }); err != nil {
		return nil, err
	}
	return fw, nil
}

func eachLine(path string, fn func(string)) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		fn(strings.TrimRight(sc.Text(), "\r"))
	}
	return sc.Err()
}

// qualified returns "Class::method" of a demangled name.
func qualified(demangled string) string {
	if i := strings.Index(demangled, "("); i > 0 {
		return demangled[:i]
	}
	return demangled
}

// classesWith returns the classes that have the method. kind is "signal",
// "slot" (slot or invokable method) or "name" (any method with that name).
func (fw *firmware) classesWith(kind, sig string) []string {
	var out []string
	for name, c := range fw.classes {
		var ms []method
		switch kind {
		case "signal":
			ms = c.Signals
		case "slot":
			ms = append(append(ms, c.Slots...), c.Methods...)
		default:
			ms = append(append(append(ms, c.Signals...), c.Slots...), c.Methods...)
		}
		for _, m := range ms {
			if (kind == "name" && methodName(m.Signature) == sig) || (kind != "name" && normalize(m.Signature) == sig) {
				out = append(out, name)
				break
			}
		}
	}
	sort.Strings(out)
	return out
}

// overloads returns the signatures of methods with the same name in a class.
func (fw *firmware) overloads(class, kind, name string) []string {
	c, ok := fw.classes[class]
	if !ok {
		return nil
	}
	ms := c.Signals
	if kind == "slot" {
		ms = append(append([]method{}, c.Slots...), c.Methods...)
	}
	var out []string
	for _, m := range ms {
		if methodName(m.Signature) == name {
			out = append(out, m.Signature)
		}
	}
	return out
}

func methodName(sig string) string {
	if i := strings.Index(sig, "("); i >= 0 {
		return sig[:i]
	}
	return sig
}

// normalize makes a signature comparable to the moc signature, as
// QMetaObject::normalizedSignature does for the common cases.
func normalize(sig string) string {
	i := strings.Index(sig, "(")
	j := strings.LastIndex(sig, ")")
	if i < 0 || j < i {
		return strings.TrimSpace(sig)
	}
	var params []string
	for _, p := range splitParams(sig[i+1 : j]) {
		p = strings.TrimSpace(p)
		p = strings.TrimPrefix(p, "const ")
		p = strings.TrimSpace(strings.TrimSuffix(p, "&"))
		p = strings.ReplaceAll(p, " *", "*")
		p = strings.ReplaceAll(p, " >", ">")
		p = strings.ReplaceAll(p, "< ", "<")
		p = strings.ReplaceAll(p, ", ", ",")
		if p != "" && p != "void" {
			params = append(params, p)
		}
	}
	return strings.TrimSpace(sig[:i]) + "(" + strings.Join(params, ",") + ")"
}

func splitParams(s string) []string {
	var out []string
	depth, start := 0, 0
	for i, r := range s {
		switch r {
		case '<', '(':
			depth++
		case '>', ')':
			depth--
		case ',':
			if depth == 0 {
				out = append(out, s[start:i])
				start = i + 1
			}
		}
	}
	return append(out, s[start:])
}

// ---- mod sources -------------------------------------------------------------

type use struct {
	Kind     string // symbol, signal, slot, invoke, class, object
	Name     string // mangled name, normalized signature, method, class or object name
	Optional bool
	Desc     string
	Where    string // file:line of the first use
	Qt       uint8  // the Qt majors whose build has the use (qtBit), from #if QT_VERSION_MAJOR
}

type mod struct {
	Name     string
	Sources  []string
	Qts      []int  // the Qt majors that the mod builds for, for example 5 and 6
	Baseline string // one firmware, or one for each Qt major, separated by commas
	Info     bool
	uses     []use
}

// stripComments blanks out comments and keeps strings and line numbers.
func stripComments(src string) string {
	b := []byte(src)
	const (
		code = iota
		str
		chr
		line
		block
	)
	st := code
	for i := 0; i < len(b); i++ {
		c := b[i]
		switch st {
		case code:
			switch {
			case c == '"':
				st = str
			case c == '\'':
				st = chr
			case c == '/' && i+1 < len(b) && b[i+1] == '/':
				st = line
				b[i] = ' '
			case c == '/' && i+1 < len(b) && b[i+1] == '*':
				st = block
				b[i] = ' '
			}
		case str, chr:
			if c == '\\' {
				i++
			} else if (st == str && c == '"') || (st == chr && c == '\'') {
				st = code
			}
		case line:
			if c == '\n' {
				st = code
			} else {
				b[i] = ' '
			}
		case block:
			if c == '*' && i+1 < len(b) && b[i+1] == '/' {
				b[i], b[i+1] = ' ', ' '
				i++
				st = code
			} else if c != '\n' {
				b[i] = ' '
			}
		}
	}
	return string(b)
}

var (
	reEntry     = regexp.MustCompile(`\{[^{}]*?\.(name|sym)\s*=\s*"(_Z\w+)"[^{}]*\}`)
	reOptional  = regexp.MustCompile(`\.optional\s*=\s*true`)
	reDesc      = regexp.MustCompile(`\.desc\s*=\s*"([^"]*)"`)
	reMangled   = regexp.MustCompile(`"(_Z\w+)"`)
	reInvoke    = regexp.MustCompile(`invokeMethod\(\s*[^,;]+,\s*"(\w+)"`)
	reClass     = regexp.MustCompile(`(?:\binherits|\bqt_metacast|ClassName)\(\s*"(\w+)"`)
	reClassCmp  = regexp.MustCompile(`className\(\)\)?\s*(?:,|==)\s*"(\w+)"`)
	reObject    = regexp.MustCompile(`(?:findChild(?:ren)?\s*<[^>]*>\s*\(|viewWithObjectName\([^,;]*,)\s*(?:QStringLiteral\(\s*)?"([^"]+)"`)
	reSetObject = regexp.MustCompile(`setObjectName\(\s*(?:QStringLiteral\(|QString::fromLatin1\(|QString::fromUtf8\()?\s*"([^"]+)"`)
	reSection   = regexp.MustCompile(`^\s*(?:(signals|Q_SIGNALS)|(?:public|private|protected)?\s*(slots|Q_SLOTS)|(public|private|protected))\s*:`)
	reDeclName  = regexp.MustCompile(`(\w+)\s*\(`)
	reInvokable = regexp.MustCompile(`Q_INVOKABLE\s+[^(;]*?(\w+)\s*\(`)
)

func (m *mod) scan() error {
	own := map[string]bool{} // "signal:x", "slot:x", "object:x"
	type file struct{ path, text string }
	var files []file
	for _, src := range m.Sources {
		err := filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return err
			}
			switch strings.ToLower(filepath.Ext(p)) {
			case ".cc", ".cpp", ".c", ".h", ".hpp":
				b, err := os.ReadFile(p)
				if err != nil {
					return err
				}
				files = append(files, file{p, stripComments(string(b))})
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	// Pass 1: what the mod declares itself.
	for _, f := range files {
		section := ""
		for _, l := range strings.Split(f.text, "\n") {
			if s := reSection.FindStringSubmatch(l); s != nil {
				switch {
				case s[1] != "":
					section = "signal"
				case s[2] != "":
					section = "slot"
				default:
					section = ""
				}
				l = l[len(s[0]):]
			}
			if strings.Contains(l, "};") {
				section = ""
			}
			if section != "" {
				if d := reDeclName.FindStringSubmatch(l); d != nil {
					own[section+":"+d[1]] = true
				}
			}
			if d := reInvokable.FindStringSubmatch(l); d != nil {
				own["slot:"+d[1]] = true
			}
			for _, s := range reSetObject.FindAllStringSubmatch(l, -1) {
				own["object:"+s[1]] = true
			}
		}
	}
	// Pass 2: what the mod uses from Nickel.
	seen := map[string]int{}
	for _, f := range files {
		rel := f.path
		masks := qtLineMasks(f.text)
		lineAt := func(off int) string {
			return fmt.Sprintf("%s:%d", filepath.ToSlash(rel), strings.Count(f.text[:off], "\n")+1)
		}
		add := func(u use) {
			n, _ := strconv.Atoi(u.Where[strings.LastIndex(u.Where, ":")+1:])
			u.Qt = masks[n-1]
			if i, ok := seen[u.Kind+":"+u.Name]; ok {
				m.uses[i].Qt |= u.Qt
				return
			}
			seen[u.Kind+":"+u.Name] = len(m.uses)
			m.uses = append(m.uses, u)
		}
		for _, e := range reEntry.FindAllStringSubmatchIndex(f.text, -1) {
			body := f.text[e[0]:e[1]]
			u := use{Kind: "symbol", Name: f.text[e[4]:e[5]], Optional: reOptional.MatchString(body), Where: lineAt(e[0])}
			if d := reDesc.FindStringSubmatch(body); d != nil {
				u.Desc = d[1]
			}
			if f.text[e[2]:e[3]] == "sym" {
				u.Desc = strings.TrimSpace("hook " + u.Desc)
			}
			add(u)
		}
		for _, e := range reMangled.FindAllStringSubmatchIndex(f.text, -1) {
			add(use{Kind: "symbol", Name: f.text[e[2]:e[3]], Where: lineAt(e[0])})
		}
		for _, macro := range []string{"SIGNAL(", "SLOT("} {
			for off := 0; ; {
				i := strings.Index(f.text[off:], macro)
				if i < 0 {
					break
				}
				start := off + i + len(macro)
				// Skip the definitions of the macros and identifiers that end in SIGNAL or SLOT.
				if p := off + i - 1; p >= 0 && (isIdent(f.text[p]) || strings.HasSuffix(strings.TrimRight(f.text[:off+i], " "), "#define")) {
					off = start
					continue
				}
				depth, j := 1, start
				for ; j < len(f.text) && depth > 0; j++ {
					switch f.text[j] {
					case '(':
						depth++
					case ')':
						depth--
					}
				}
				sig := normalize(f.text[start : j-1])
				kind := "signal"
				if macro == "SLOT(" {
					kind = "slot"
				}
				off = j
				if !strings.Contains(sig, "(") || own[kind+":"+methodName(sig)] {
					continue
				}
				add(use{Kind: kind, Name: sig, Where: lineAt(start)})
			}
		}
		for _, e := range reInvoke.FindAllStringSubmatchIndex(f.text, -1) {
			if n := f.text[e[2]:e[3]]; !own["slot:"+n] && !own["signal:"+n] {
				add(use{Kind: "invoke", Name: n, Where: lineAt(e[0])})
			}
		}
		for _, re := range []*regexp.Regexp{reClass, reClassCmp} {
			for _, e := range re.FindAllStringSubmatchIndex(f.text, -1) {
				add(use{Kind: "class", Name: f.text[e[2]:e[3]], Where: lineAt(e[0])})
			}
		}
		for _, e := range reObject.FindAllStringSubmatchIndex(f.text, -1) {
			if n := f.text[e[2]:e[3]]; !own["object:"+n] {
				add(use{Kind: "object", Name: n, Where: lineAt(e[0])})
			}
		}
	}
	return nil
}

func isIdent(c byte) bool {
	return c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// ---- checks ------------------------------------------------------------------

type finding struct {
	Fatal bool
	Text  string
}

type result struct {
	Status   string // OK, DEGRADED, FAIL
	Findings []finding
}

// minObjectName is the shortest string that strings.txt holds (strings -n 6).
const minObjectName = 6

// expected reports whether the baseline has the name. Only those names count.
func expected(u use, base *firmware) bool {
	switch u.Kind {
	case "symbol":
		_, ok := base.syms[u.Name]
		return ok || !u.Optional
	case "signal", "slot":
		return len(base.classesWith(u.Kind, u.Name)) > 0
	case "invoke":
		return len(base.classesWith("name", u.Name)) > 0
	case "class":
		_, ok := base.classes[u.Name]
		return ok
	case "object":
		return len(u.Name) >= minObjectName && base.strs[u.Name]
	}
	return false
}

func describe(u use, fw *firmware) string {
	if d, ok := fw.syms[u.Name]; ok {
		return d
	}
	return u.Name
}

// columnQt returns the Qt major whose build of the mod runs on fw: the Qt of
// the firmware if the mod builds for it, else the first Qt of the mod.
func columnQt(m *mod, fw *firmware) int {
	if slices.Contains(m.Qts, fw.Qt) {
		return fw.Qt
	}
	return m.Qts[0]
}

// check checks the build of m for the Qt of fw against fw. base is the
// baseline firmware of that build.
func check(m *mod, base, fw *firmware) result {
	var r result
	q := columnQt(m, fw)
	if fw.Qt != 0 && q != fw.Qt {
		r.Findings = append(r.Findings, finding{true, fmt.Sprintf("Qt ABI: the mod is built for Qt %s and the firmware has only Qt %d. The mod does not load. Rebuild it for Qt %d.", qtList(m.Qts), fw.Qt, fw.Qt)})
	}
	for _, u := range m.uses {
		if u.Qt&qtBit(q) == 0 || !expected(u, base) {
			continue
		}
		switch u.Kind {
		case "symbol":
			if _, ok := fw.syms[u.Name]; ok {
				continue
			}
			name := describe(u, base)
			what := "Missing required symbol"
			if u.Optional {
				what = "Missing optional symbol"
			}
			t := fmt.Sprintf("%s `%s`", what, name)
			if u.Desc != "" {
				t += " (" + u.Desc + ")"
			}
			t += " at " + u.Where
			if alts := fw.byName[qualified(name)]; len(alts) > 0 {
				t += ". Changed signature, the firmware has: `" + strings.Join(limit(alts, 3), "`, `") + "`"
			}
			r.Findings = append(r.Findings, finding{!u.Optional, t})
		case "signal", "slot":
			was := base.classesWith(u.Kind, u.Name)
			now := fw.classesWith(u.Kind, u.Name)
			if overlap(was, now) {
				continue
			}
			t := fmt.Sprintf("Missing %s `%s` at %s. The baseline has it in %s", u.Kind, u.Name, u.Where, strings.Join(limit(was, 3), ", "))
			var alts []string
			for _, c := range was {
				if _, ok := fw.classes[c]; !ok {
					t += fmt.Sprintf(". Class %s is gone", c)
					continue
				}
				for _, o := range fw.overloads(c, u.Kind, methodName(u.Name)) {
					alts = append(alts, c+"::"+o)
				}
			}
			if len(alts) > 0 {
				t += ". Changed signature, the firmware has: `" + strings.Join(limit(alts, 3), "`, `") + "`"
			} else if len(now) > 0 {
				t += ". Other classes have it: " + strings.Join(limit(now, 3), ", ")
			}
			r.Findings = append(r.Findings, finding{false, t})
		case "invoke":
			was := base.classesWith("name", u.Name)
			if overlap(was, fw.classesWith("name", u.Name)) {
				continue
			}
			r.Findings = append(r.Findings, finding{false, fmt.Sprintf("Missing method `%s` for invokeMethod at %s. The baseline has it in %s", u.Name, u.Where, strings.Join(limit(was, 3), ", "))})
		case "class":
			if _, ok := fw.classes[u.Name]; !ok {
				r.Findings = append(r.Findings, finding{false, fmt.Sprintf("Missing class `%s` (class name lookup) at %s", u.Name, u.Where)})
			}
		case "object":
			if !fw.strs[u.Name] {
				r.Findings = append(r.Findings, finding{false, fmt.Sprintf("Missing object name `%s` (findChild or viewWithObjectName) at %s", u.Name, u.Where)})
			}
		}
	}
	r.Status = "OK"
	for _, f := range r.Findings {
		if f.Fatal {
			r.Status = "FAIL"
			break
		}
		r.Status = "DEGRADED"
	}
	return r
}

func overlap(a, b []string) bool {
	for _, x := range a {
		for _, y := range b {
			if x == y {
				return true
			}
		}
	}
	return false
}

func limit(s []string, n int) []string {
	if len(s) > n {
		return append(s[:n:n], fmt.Sprintf("%d more", len(s)-n))
	}
	return s
}

// ---- versions and report -----------------------------------------------------

// VersionLess reports whether firmware version a is older than b.
func VersionLess(a, b string) bool {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(pa) && i < len(pb); i++ {
		x, _ := strconv.Atoi(pa[i])
		y, _ := strconv.Atoi(pb[i])
		if x != y {
			return x < y
		}
	}
	return len(pa) < len(pb)
}

// pickBaselines returns the baseline firmware of each Qt build of m: a
// firmware of that Qt in m.Baseline, else the newest firmware of that Qt.
func pickBaselines(m *mod, fws []*firmware) map[int]*firmware {
	bases := map[int]*firmware{}
	listed := strings.Split(m.Baseline, ",")
	for _, q := range m.Qts {
		for _, fw := range fws {
			if fw.Qt == q && slices.Contains(listed, fw.Version) {
				bases[q] = fw
			}
		}
		for _, fw := range fws { // newest first
			if bases[q] == nil && fw.Qt == q {
				bases[q] = fw
			}
		}
		if bases[q] == nil { // no firmware of that Qt is indexed
			bases[q] = fws[0]
		}
	}
	return bases
}

func qtList(qs []int) string {
	s := make([]string, len(qs))
	for i, q := range qs {
		s[i] = strconv.Itoa(q)
	}
	return strings.Join(s, " and ")
}

func report(w io.Writer, mods []*mod, fws []*firmware) (failed bool) {
	fmt.Fprintf(w, "# Mod compatibility report\n\nGenerated %s by `tools/modcheck`. Each cell is the result of the mod on that firmware:\n\n", time.Now().UTC().Format("2006-01-02 15:04 UTC"))
	fmt.Fprintln(w, "- OK: every Nickel name that the mod uses on its baseline firmware is present.")
	fmt.Fprintln(w, "- DEGRADED: an optional symbol, a signal, a slot, a class or an object name is missing. The mod loads, but a feature can fail.")
	fmt.Fprintln(w, "- FAIL: the Qt version differs or a required symbol is missing. NickelHook does not load the mod.")
	fmt.Fprintln(w)
	bases := map[*mod]map[int]*firmware{}
	results := map[*mod]map[string]result{}
	for _, m := range mods {
		bases[m] = pickBaselines(m, fws)
		results[m] = map[string]result{}
		for _, fw := range fws {
			results[m][fw.Version] = check(m, bases[m][columnQt(m, fw)], fw)
		}
	}
	fmt.Fprint(w, "| Firmware | Qt |")
	for _, m := range mods {
		n := m.Name
		if m.Info {
			n += " (info)"
		}
		fmt.Fprintf(w, " %s |", n)
	}
	fmt.Fprint(w, "\n|---|---|")
	for range mods {
		fmt.Fprint(w, "---|")
	}
	fmt.Fprintln(w)
	for _, fw := range fws {
		fmt.Fprintf(w, "| %s | %d |", fw.Version, fw.Qt)
		for _, m := range mods {
			r := results[m][fw.Version]
			cell := r.Status
			if n := len(r.Findings); n > 0 {
				cell += fmt.Sprintf(" (%d)", n)
			}
			if fw == bases[m][fw.Qt] {
				cell += ", baseline"
			}
			fmt.Fprintf(w, " %s |", cell)
			if r.Status == "FAIL" && !m.Info {
				failed = true
			}
		}
		fmt.Fprintln(w)
	}
	for _, m := range mods {
		counts := map[string]int{}
		var skipped, baseNames []string
		for _, u := range m.uses {
			counts[u.Kind]++
		}
		for _, q := range m.Qts {
			b := bases[m][q]
			baseNames = append(baseNames, fmt.Sprintf("%s (Qt %d)", b.Version, q))
			for _, u := range m.uses {
				if u.Qt&qtBit(q) != 0 && !expected(u, b) {
					skipped = append(skipped, fmt.Sprintf("%s `%s` (%s)", u.Kind, u.Name, b.Version))
				}
			}
		}
		fmt.Fprintf(w, "\n## %s\n\nSources: %s. Built for Qt %s. Baseline firmware: %s.\n", m.Name, strings.Join(m.Sources, ", "), qtList(m.Qts), strings.Join(baseNames, ", "))
		fmt.Fprintf(w, "Checked names: %d symbols, %d signals, %d slots, %d invokeMethod names, %d class names, %d object names.\n",
			counts["symbol"], counts["signal"], counts["slot"], counts["invoke"], counts["class"], counts["object"])
		for _, fw := range fws {
			r := results[m][fw.Version]
			if len(r.Findings) == 0 {
				continue
			}
			fmt.Fprintf(w, "\n### %s on %s: %s\n\n", m.Name, fw.Version, r.Status)
			for _, f := range r.Findings {
				fmt.Fprintf(w, "- %s\n", f.Text)
			}
		}
		if len(skipped) > 0 {
			sort.Strings(skipped)
			fmt.Fprintf(w, "\nNot checked, because the baseline does not have them (Qt names, names of the mod itself, object names shorter than %d characters, or names that are already broken): %s.\n",
				minObjectName, strings.Join(skipped, ", "))
		}
	}
	return failed
}

func readMods(path string) ([]*mod, error) {
	var mods []*mod
	dir := filepath.Dir(path)
	err := eachLine(path, func(l string) {
		if strings.HasPrefix(l, "#") || strings.TrimSpace(l) == "" {
			return
		}
		f := strings.Split(l, "\t")
		if len(f) < 5 || f[0] == "name" {
			return
		}
		m := &mod{Name: f[0], Baseline: f[3], Info: f[4] == "info", Qts: parseQts(f[2])}
		for _, s := range strings.Split(f[1], ",") {
			m.Sources = append(m.Sources, filepath.Join(dir, strings.TrimSpace(s)))
		}
		mods = append(mods, m)
	})
	return mods, err
}

// Main runs the check with the command line arguments args. It returns the
// exit code: 0 if no mod fails, 1 if a mod with role check fails, 2 for an
// error.
func Main(args []string) int {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	index := fs.String("index", "firmware", "the firmware index directory")
	modsFile := fs.String("mods", "", "a mods.tsv file (name, sources, qt, baseline, role)")
	out := fs.String("out", "", "write the report to this file instead of stdout")
	baseline := fs.String("baseline", "", "the baseline firmware for sources on the command line (default: the newest with the same Qt)")
	qt := fs.String("qt", "5", "the Qt majors of sources on the command line: 5, 6, or 5,6 for a source that builds for both")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	var mods []*mod
	if *modsFile != "" {
		var err error
		if mods, err = readMods(*modsFile); err != nil {
			return fail(err)
		}
	}
	if fs.NArg() > 0 {
		mods = append(mods, &mod{Name: strings.Join(fs.Args(), ", "), Sources: fs.Args(), Qts: parseQts(*qt), Baseline: *baseline})
	}
	if len(mods) == 0 {
		return fail(fmt.Errorf("usage: kfw check [-index <dir>] (-mods <mods.tsv> | <source>...)"))
	}
	for _, m := range mods {
		if err := m.scan(); err != nil {
			return fail(err)
		}
	}
	dirs, _ := filepath.Glob(filepath.Join(*index, "*", "symbols.tsv"))
	var fws []*firmware
	for _, d := range dirs {
		fw, err := loadFirmware(filepath.Dir(d))
		if err != nil {
			return fail(err)
		}
		fws = append(fws, fw)
	}
	if len(fws) == 0 {
		return fail(fmt.Errorf("no firmware index in %s", *index))
	}
	sort.Slice(fws, func(i, j int) bool { return VersionLess(fws[j].Version, fws[i].Version) })

	w := io.WriteCloser(os.Stdout)
	if *out != "" {
		var err error
		if w, err = os.Create(*out); err != nil {
			return fail(err)
		}
	}
	failed := report(w, mods, fws)
	if err := w.Close(); err != nil {
		return fail(err)
	}
	if failed {
		return 1
	}
	return 0
}

func fail(err error) int {
	fmt.Fprintln(os.Stderr, "check:", err)
	return 2
}

func parseQts(s string) []int {
	var qs []int
	for _, f := range strings.Split(s, ",") {
		if q, err := strconv.Atoi(strings.TrimSpace(f)); err == nil && qtBit(q) != 0 {
			qs = append(qs, q)
		}
	}
	if len(qs) == 0 {
		qs = []int{5}
	}
	return qs
}
