// Command qtmeta reads every Qt staticMetaObject in a 32-bit ARM Nickel
// library (libnickel.so.1.0.0) and prints the signals, slots and invokable
// methods of each class as JSON.
//
// It reads the moc data that Qt 5 stores in the binary (moc revision 7 and 8,
// firmware 4.x) and the moc data of Qt 6 (moc revision 9 and later, firmware
// 5.x and 6.x).
// Use it to find methods that a mod can call by name with
// QMetaObject::invokeMethod or connect with SIGNAL()/SLOT() strings, without
// knowing the C++ signature.
//
// `kfw qtmeta <libnickel.so.1.0.0>` prints it. `kfw index` runs it for each firmware.
package qtmeta

import (
	"debug/elf"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
)

// Builtin QMetaType ids of Qt 5.2 that moc writes as resolved types.
var builtinTypes = map[uint32]string{
	1: "bool", 2: "int", 3: "uint", 4: "qlonglong", 5: "qulonglong", 6: "double",
	7: "QChar", 8: "QVariantMap", 9: "QVariantList", 10: "QString", 11: "QStringList",
	12: "QByteArray", 13: "QBitArray", 14: "QDate", 15: "QTime", 16: "QDateTime",
	17: "QUrl", 18: "QLocale", 19: "QRect", 20: "QRectF", 21: "QSize", 22: "QSizeF",
	23: "QLine", 24: "QLineF", 25: "QPoint", 26: "QPointF", 27: "QRegExp",
	28: "QVariantHash", 29: "QEasingCurve", 30: "QUuid", 31: "void*", 32: "long",
	33: "short", 34: "char", 35: "ulong", 36: "ushort", 37: "uchar", 38: "float",
	39: "QObject*", 40: "signed char", 41: "QVariant", 42: "QModelIndex", 43: "void",
	44: "QRegularExpression", 45: "QJsonValue", 46: "QJsonObject", 47: "QJsonArray",
	48: "QJsonDocument", 64: "QFont", 65: "QPixmap", 66: "QBrush", 67: "QColor",
	68: "QPalette", 69: "QIcon", 70: "QImage", 71: "QPolygon", 72: "QRegion",
	73: "QBitmap", 74: "QCursor", 75: "QKeySequence", 76: "QPen", 77: "QTextLength",
	78: "QTextFormat", 79: "QMatrix", 80: "QTransform", 81: "QMatrix4x4",
	82: "QVector2D", 83: "QVector3D", 84: "QVector4D", 85: "QQuaternion",
	86: "QPolygonF", 121: "QSizePolicy",
}

// Builtin QMetaType ids of Qt 6 that differ from Qt 5. The core ids up to 48
// are the same, except 27 (QRegExp), which Qt 6 removed. Qt 6 moved the GUI
// types to 0x1000 and QSizePolicy to 0x2000.
var builtinTypes6 = map[uint32]string{
	49: "QByteArrayList", 50: "QPersistentModelIndex", 51: "std::nullptr_t",
	52: "QCborSimpleType", 56: "char16_t", 57: "char32_t", 58: "QVariantPair",
	0x1000: "QFont", 0x1001: "QPixmap", 0x1002: "QBrush", 0x1003: "QColor",
	0x1004: "QPalette", 0x1005: "QIcon", 0x1006: "QImage", 0x1007: "QPolygon",
	0x1008: "QRegion", 0x1009: "QBitmap", 0x100a: "QCursor", 0x100b: "QKeySequence",
	0x100c: "QPen", 0x100d: "QTextLength", 0x100e: "QTextFormat", 0x100f: "QTransform",
	0x1010: "QMatrix4x4", 0x1011: "QVector2D", 0x1012: "QVector3D", 0x1013: "QVector4D",
	0x1014: "QQuaternion", 0x1015: "QPolygonF", 0x1016: "QColorSpace",
	0x2000: "QSizePolicy",
}

type method struct {
	Signature string   `json:"signature"`
	Return    string   `json:"return"`
	Params    []string `json:"params,omitempty"`
	Access    string   `json:"access"`
}

type class struct {
	Super        string   `json:"super,omitempty"`
	Signals      []method `json:"signals,omitempty"`
	Slots        []method `json:"slots,omitempty"`
	Methods      []method `json:"methods,omitempty"`
	Constructors []method `json:"constructors,omitempty"`
}

type image struct {
	f     *elf.File
	progs []*elf.Prog
	// relocs maps an address to the dynamic symbol of an ABS32/GLOB_DAT relocation.
	relocs map[uint32]string
	// metaAt maps the address of each local staticMetaObject to its symbol.
	metaAt map[uint32]string
	// qt6 is true when the moc data has the Qt 6 layout.
	qt6 bool
}

func (im *image) read(addr uint32, n int) ([]byte, error) {
	for _, p := range im.progs {
		if uint64(addr) >= p.Vaddr && uint64(addr)+uint64(n) <= p.Vaddr+p.Filesz {
			b := make([]byte, n)
			_, err := p.ReadAt(b, int64(uint64(addr)-p.Vaddr))
			return b, err
		}
	}
	return nil, fmt.Errorf("address 0x%x not in a loaded segment", addr)
}

func (im *image) u32(addr uint32) (uint32, error) {
	b, err := im.read(addr, 4)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint32(b), nil
}

// str returns string i of the moc string data.
// Qt 5: a QByteArrayData array (16 bytes each on 32-bit: ref, size, alloc,
// offset). The bytes start at the entry address plus offset.
// Qt 6: a uint array of (offset, size) pairs. The offset is from the start of
// the array.
func (im *image) str(stringdata uint32, i uint32) (string, error) {
	e, sizeAt, offAt := stringdata+16*i, uint32(4), uint32(12)
	if im.qt6 {
		e, sizeAt, offAt = stringdata, 8*i+4, 8*i
	}
	size, err := im.u32(e + sizeAt)
	if err != nil {
		return "", err
	}
	off, err := im.u32(e + offAt)
	if err != nil {
		return "", err
	}
	if size > 4096 {
		return "", fmt.Errorf("string %d too long", i)
	}
	b, err := im.read(e+off, int(size))
	return string(b), err
}

func (im *image) typeName(stringdata uint32, t uint32) string {
	if t&0x80000000 != 0 {
		s, err := im.str(stringdata, t&0x7fffffff)
		if err != nil {
			return "?"
		}
		return s
	}
	if im.qt6 {
		if n, ok := builtinTypes6[t]; ok {
			return n
		}
		if t == 27 || t >= 64 {
			return fmt.Sprintf("QMetaType(%d)", t)
		}
	}
	if n, ok := builtinTypes[t]; ok {
		return n
	}
	return fmt.Sprintf("QMetaType(%d)", t)
}

func (im *image) parse(mo uint32) (string, class, error) {
	var c class
	stringdata, err := im.u32(mo + 4)
	if err != nil {
		return "", c, err
	}
	data, err := im.u32(mo + 8)
	if err != nil {
		return "", c, err
	}
	// superdata is a relocation to another library (for example QWidget in
	// libQt5Widgets) or a link-time address inside this library.
	if name, ok := im.relocs[mo]; ok {
		c.Super = strings.TrimSuffix(demangleMeta(name), "::staticMetaObject")
	} else if sup, _ := im.u32(mo); sup != 0 {
		if name, ok := im.metaAt[sup]; ok {
			c.Super = strings.TrimSuffix(demangleMeta(name), "::staticMetaObject")
		} else {
			c.Super = fmt.Sprintf("@0x%x", sup)
		}
	}
	hdr := make([]uint32, 14)
	for i := range hdr {
		if hdr[i], err = im.u32(data + uint32(4*i)); err != nil {
			return "", c, err
		}
	}
	if hdr[0] < 7 {
		return "", c, fmt.Errorf("moc revision %d not supported", hdr[0])
	}
	// Qt 6 (revision 9 and later) adds a metatype offset to each method entry.
	im.qt6 = hdr[0] >= 9
	entry := uint32(5)
	if im.qt6 {
		entry = 6
	}
	className, err := im.str(stringdata, hdr[1])
	if err != nil {
		return "", c, err
	}
	readMethods := func(count, index uint32, ctor bool) error {
		for m := uint32(0); m < count; m++ {
			base := data + 4*(index+entry*m)
			var v [5]uint32
			for i := range v {
				if v[i], err = im.u32(base + uint32(4*i)); err != nil {
					return err
				}
			}
			name, err := im.str(stringdata, v[0])
			if err != nil {
				return err
			}
			argc, params, flags := v[1], v[2], v[4]
			md := method{Access: [...]string{"private", "protected", "public", "?"}[flags&3]}
			p := data + 4*params
			if !ctor {
				rt, err := im.u32(p)
				if err != nil {
					return err
				}
				md.Return = im.typeName(stringdata, rt)
				p += 4
			}
			var types []string
			for a := uint32(0); a < argc; a++ {
				t, err := im.u32(p + 4*a)
				if err != nil {
					return err
				}
				types = append(types, im.typeName(stringdata, t))
			}
			for a := uint32(0); a < argc; a++ {
				ni, err := im.u32(p + 4*(argc+a))
				if err != nil {
					return err
				}
				pn, _ := im.str(stringdata, ni)
				md.Params = append(md.Params, strings.TrimSpace(types[a]+" "+pn))
			}
			md.Signature = name + "(" + strings.Join(types, ",") + ")"
			switch {
			case ctor:
				c.Constructors = append(c.Constructors, md)
			case flags&0x0c == 0x04:
				c.Signals = append(c.Signals, md)
			case flags&0x0c == 0x08:
				c.Slots = append(c.Slots, md)
			default:
				c.Methods = append(c.Methods, md)
			}
		}
		return nil
	}
	if err := readMethods(hdr[4], hdr[5], false); err != nil {
		return "", c, err
	}
	if err := readMethods(hdr[10], hdr[11], true); err != nil {
		return "", c, err
	}
	return className, c, nil
}

// demangleMeta turns _ZN<len><name>...16staticMetaObjectE into "A::B::staticMetaObject".
func demangleMeta(s string) string {
	if !strings.HasPrefix(s, "_ZN") || !strings.HasSuffix(s, "E") {
		return s
	}
	s = s[3 : len(s)-1]
	var parts []string
	for len(s) > 0 {
		n := 0
		for len(s) > 0 && s[0] >= '0' && s[0] <= '9' {
			n = n*10 + int(s[0]-'0')
			s = s[1:]
		}
		if n == 0 || n > len(s) {
			return s
		}
		parts = append(parts, s[:n])
		s = s[n:]
	}
	return strings.Join(parts, "::")
}

// Run writes the Qt meta data of the library at path to w as JSON. It
// writes one line for each class that it cannot read, and a count, to log.
func Run(path string, w, log io.Writer) error {
	f, err := elf.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	im := &image{f: f, relocs: map[uint32]string{}, metaAt: map[uint32]string{}}
	for _, p := range f.Progs {
		if p.Type == elf.PT_LOAD {
			im.progs = append(im.progs, p)
		}
	}
	syms, err := f.DynamicSymbols()
	if err != nil {
		return err
	}
	for _, s := range syms {
		if strings.HasSuffix(s.Name, "16staticMetaObjectE") && s.Section != elf.SHN_UNDEF {
			im.metaAt[uint32(s.Value)] = s.Name
		}
	}
	if sec := f.Section(".rel.dyn"); sec != nil {
		b, err := sec.Data()
		if err != nil {
			return err
		}
		for i := 0; i+8 <= len(b); i += 8 {
			off := binary.LittleEndian.Uint32(b[i:])
			info := binary.LittleEndian.Uint32(b[i+4:])
			typ, sym := elf.R_ARM(info&0xff), info>>8
			if (typ == elf.R_ARM_ABS32 || typ == elf.R_ARM_GLOB_DAT) && sym > 0 && int(sym) <= len(syms) {
				im.relocs[off] = syms[sym-1].Name
			}
		}
	}
	out := map[string]class{}
	var failed []string
	for _, s := range syms {
		// A QMetaObject is 24 bytes in Qt 5 and 28 bytes in Qt 6 (32-bit).
		if !strings.HasSuffix(s.Name, "16staticMetaObjectE") || s.Section == elf.SHN_UNDEF || (s.Size != 24 && s.Size != 28) {
			continue
		}
		name, c, err := im.parse(uint32(s.Value))
		if err != nil {
			failed = append(failed, demangleMeta(s.Name)+": "+err.Error())
			continue
		}
		out[name] = c
	}
	sort.Strings(failed)
	for _, m := range failed {
		fmt.Fprintln(log, "skip:", m)
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		return err
	}
	fmt.Fprintf(log, "classes: %d, skipped: %d\n", len(out), len(failed))
	return nil
}
