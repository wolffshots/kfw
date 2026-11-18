package modcheck

import (
	"regexp"
	"strconv"
	"strings"
)

// A source that builds for Qt 5 and Qt 6 keeps the code of one Qt major
// under #if QT_VERSION_MAJOR. qtLineMasks finds, for each line, the Qt majors
// whose build keeps the line, so that a Nickel name of the Qt 5 build is not
// checked against a Qt 6 firmware.

const qtAll uint8 = 1<<0 | 1<<1 // Qt 5 and Qt 6

// qtBit returns the mask bit of a Qt major, or 0 for an unknown major.
func qtBit(q int) uint8 {
	switch q {
	case 5:
		return 1 << 0
	case 6:
		return 1 << 1
	}
	return 0
}

var reDirective = regexp.MustCompile(`^\s*#\s*(\w+)\s*(.*)$`)

// qtLineMasks returns the Qt mask of each line of src. It evaluates #if and
// #elif conditions that use QT_VERSION_MAJOR, QT_VERSION and
// QT_VERSION_CHECK. Any other condition keeps its lines for both majors.
func qtLineMasks(src string) []uint8 {
	lines := strings.Split(src, "\n")
	masks := make([]uint8, len(lines))
	type frame struct{ cur, taken uint8 }
	var st []frame
	for i, l := range lines {
		if d := reDirective.FindStringSubmatch(l); d != nil {
			c, known := qtCond(d[2])
			switch d[1] {
			case "if":
				if known {
					st = append(st, frame{c, c})
				} else {
					st = append(st, frame{qtAll, 0})
				}
			case "ifdef", "ifndef":
				st = append(st, frame{qtAll, 0})
			case "elif":
				if n := len(st); n > 0 {
					f := &st[n-1]
					if known {
						f.cur = c &^ f.taken
						f.taken |= c
					} else {
						f.cur = qtAll &^ f.taken
					}
				}
			case "else":
				if n := len(st); n > 0 {
					st[n-1].cur = qtAll &^ st[n-1].taken
				}
			case "endif":
				if n := len(st); n > 0 {
					st = st[:n-1]
				}
			}
		}
		m := qtAll
		for _, f := range st {
			m &= f.cur
		}
		masks[i] = m
	}
	return masks
}

var (
	reVersionCheck = regexp.MustCompile(`QT_VERSION_CHECK\s*\(\s*(\d+)\s*,\s*(\d+)\s*,\s*(\d+)\s*\)`)
	reQtVersion    = regexp.MustCompile(`\bQT_VERSION\b`)
)

// qtCond evaluates a condition for Qt 5 and Qt 6. known is false if the
// condition does not use the Qt version or cannot be evaluated.
func qtCond(expr string) (mask uint8, known bool) {
	if !strings.Contains(expr, "QT_VERSION") {
		return 0, false
	}
	expr = reVersionCheck.ReplaceAllStringFunc(expr, func(s string) string {
		p := reVersionCheck.FindStringSubmatch(s)
		a, _ := strconv.Atoi(p[1])
		b, _ := strconv.Atoi(p[2])
		c, _ := strconv.Atoi(p[3])
		return strconv.Itoa(a<<16 | b<<8 | c)
	})
	for _, q := range []int{5, 6} {
		e := strings.ReplaceAll(expr, "QT_VERSION_MAJOR", strconv.Itoa(q))
		e = reQtVersion.ReplaceAllString(e, strconv.Itoa(q<<16))
		v, ok := evalCond(e)
		if !ok {
			return 0, false
		}
		if v != 0 {
			mask |= qtBit(q)
		}
	}
	return mask, true
}

// evalCond evaluates an integer preprocessor expression with numbers,
// comparisons, !, && , || and parentheses.
func evalCond(s string) (int, bool) {
	toks, ok := lexCond(s)
	if !ok {
		return 0, false
	}
	p := &condParser{toks: toks, ok: true}
	v := p.or()
	return v, p.ok && p.i == len(p.toks)
}

var reCondTok = regexp.MustCompile(`^\s*(0[xX][0-9a-fA-F]+|\d+|&&|\|\||==|!=|<=|>=|[<>!()])`)

func lexCond(s string) ([]string, bool) {
	var toks []string
	for s = strings.TrimSpace(s); s != ""; s = strings.TrimSpace(s) {
		m := reCondTok.FindStringSubmatch(s)
		if m == nil {
			return nil, false
		}
		toks = append(toks, m[1])
		s = s[len(m[0]):]
	}
	return toks, true
}

type condParser struct {
	toks []string
	i    int
	ok   bool
}

func (p *condParser) peek() string {
	if p.i < len(p.toks) {
		return p.toks[p.i]
	}
	return ""
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (p *condParser) or() int {
	v := p.and()
	for p.peek() == "||" {
		p.i++
		w := p.and()
		v = b2i(v != 0 || w != 0)
	}
	return v
}

func (p *condParser) and() int {
	v := p.cmp()
	for p.peek() == "&&" {
		p.i++
		w := p.cmp()
		v = b2i(v != 0 && w != 0)
	}
	return v
}

func (p *condParser) cmp() int {
	v := p.unary()
	for {
		switch op := p.peek(); op {
		case "==", "!=", "<", "<=", ">", ">=":
			p.i++
			w := p.unary()
			v = b2i(map[string]bool{"==": v == w, "!=": v != w, "<": v < w, "<=": v <= w, ">": v > w, ">=": v >= w}[op])
		default:
			return v
		}
	}
}

func (p *condParser) unary() int {
	switch t := p.peek(); {
	case t == "!":
		p.i++
		return b2i(p.unary() == 0)
	case t == "(":
		p.i++
		v := p.or()
		if p.peek() != ")" {
			p.ok = false
			return 0
		}
		p.i++
		return v
	case t != "" && (t[0] >= '0' && t[0] <= '9'):
		p.i++
		n, err := strconv.ParseInt(t, 0, 64)
		if err != nil {
			p.ok = false
		}
		return int(n)
	}
	p.ok = false
	return 0
}
