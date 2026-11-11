// Package textenc is the set of text encodings (code pages) WebOne can
// convert content to and from, named as .NET and Windows name them
// ("windows-1251", "ibm866", "macintosh", 1251, 866 ...), and the default
// code pages of a language (Windows ANSI, DOS OEM, Mac, EBCDIC, ISO).
package textenc

import (
	"bytes"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/htmlindex"
	"golang.org/x/text/encoding/ianaindex"
	"golang.org/x/text/encoding/japanese"
	"golang.org/x/text/encoding/korean"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/traditionalchinese"
	"golang.org/x/text/encoding/unicode"
)

// Encoding is a code page.
type Encoding struct {
	Name     string // web name, as in a charset= parameter
	CodePage int
	Display  string
	enc      encoding.Encoding // nil for UTF-8
	cm       *charmap.Charmap
}

func (e *Encoding) String() string { return e.Name }

// IsUTF8 is the UTF-8 encoding.
func (e *Encoding) IsUTF8() bool { return e.enc == nil }

// Decode turns text in this encoding into a Go (UTF-8) string.
func (e *Encoding) Decode(b []byte) string {
	if e.enc == nil {
		return string(bytes.ToValidUTF8(b, []byte("�")))
	}
	if e.cm != nil {
		// byte by byte; bytes the code page leaves undefined become the
		// control characters of the same number, as .NET does, so that
		// decoding and encoding again gives the same bytes
		var sb strings.Builder
		sb.Grow(len(b))
		for _, c := range b {
			r := e.cm.DecodeByte(c)
			if r == utf8.RuneError && c >= 0x80 {
				r = rune(c)
			}
			sb.WriteRune(r)
		}
		return sb.String()
	}
	out, err := e.enc.NewDecoder().Bytes(b)
	if err != nil {
		return string(b)
	}
	return string(out)
}

// Encode turns a string into this encoding; characters it has not are '?'.
func (e *Encoding) Encode(s string) []byte {
	if e.enc == nil {
		return []byte(s)
	}
	if e.cm != nil {
		out := make([]byte, 0, len(s))
		for _, r := range s {
			if b, ok := e.cm.EncodeRune(r); ok {
				out = append(out, b)
			} else if r >= 0x80 && r < 0x100 && e.cm.DecodeByte(byte(r)) == utf8.RuneError {
				out = append(out, byte(r))
			} else {
				out = append(out, '?')
			}
		}
		return out
	}
	if out, err := e.enc.NewEncoder().Bytes([]byte(s)); err == nil {
		return out
	}
	// slow path: rune by rune, '?' for what does not fit
	var out []byte
	enc := e.enc.NewEncoder()
	buf := make([]byte, utf8.UTFMax)
	for _, r := range s {
		n := utf8.EncodeRune(buf, r)
		b, err := enc.Bytes(buf[:n])
		if err != nil {
			out = append(out, '?')
			enc.Reset()
			continue
		}
		out = append(out, b...)
	}
	return out
}

// Preamble is the byte order mark of the Unicode encodings (nil otherwise).
func (e *Encoding) Preamble() []byte {
	switch e.CodePage {
	case 65001:
		return []byte{0xEF, 0xBB, 0xBF}
	case 1200:
		return []byte{0xFF, 0xFE}
	case 1201:
		return []byte{0xFE, 0xFF}
	}
	return nil
}

var (
	UTF8    = &Encoding{Name: "utf-8", CodePage: 65001, Display: "Unicode (UTF-8)"}
	UTF16LE = &Encoding{Name: "utf-16", CodePage: 1200, Display: "Unicode", enc: unicode.UTF16(unicode.LittleEndian, unicode.IgnoreBOM)}
	UTF16BE = &Encoding{Name: "utf-16BE", CodePage: 1201, Display: "Unicode (Big-Endian)", enc: unicode.UTF16(unicode.BigEndian, unicode.IgnoreBOM)}
)

type entry struct {
	name    string
	cp      int
	display string
	enc     encoding.Encoding
	aliases []string
}

var table = []entry{
	{"IBM037", 37, "IBM EBCDIC (US-Canada)", charmap.CodePage037, []string{"cp037", "ebcdic-cp-us"}},
	{"IBM437", 437, "OEM United States", charmap.CodePage437, []string{"cp437", "437"}},
	{"IBM850", 850, "Western European (DOS)", charmap.CodePage850, []string{"cp850"}},
	{"IBM852", 852, "Central European (DOS)", charmap.CodePage852, []string{"cp852"}},
	{"IBM855", 855, "OEM Cyrillic", charmap.CodePage855, []string{"cp855"}},
	{"IBM00858", 858, "OEM Multilingual Latin I", charmap.CodePage858, []string{"cp858"}},
	{"IBM860", 860, "Portuguese (DOS)", charmap.CodePage860, []string{"cp860"}},
	{"IBM862", 862, "Hebrew (DOS)", charmap.CodePage862, []string{"cp862", "dos-862"}},
	{"IBM863", 863, "French Canadian (DOS)", charmap.CodePage863, []string{"cp863"}},
	{"IBM865", 865, "Nordic (DOS)", charmap.CodePage865, []string{"cp865"}},
	{"cp866", 866, "Cyrillic (DOS)", charmap.CodePage866, []string{"ibm866", "866"}},
	{"IBM01047", 1047, "IBM Latin-1", charmap.CodePage1047, []string{"cp1047"}},
	{"IBM01140", 1140, "IBM EBCDIC (US-Canada-Euro)", charmap.CodePage1140, []string{"cp1140"}},
	{"windows-874", 874, "Thai (Windows)", charmap.Windows874, []string{"cp874", "tis-620"}},
	{"shift_jis", 932, "Japanese (Shift-JIS)", japanese.ShiftJIS, []string{"sjis", "ms_kanji", "csshiftjis", "x-sjis"}},
	{"gb2312", 936, "Chinese Simplified (GB2312)", simplifiedchinese.GBK, []string{"gbk", "cp936"}},
	{"ks_c_5601-1987", 949, "Korean", korean.EUCKR, []string{"euc-kr", "cp949"}},
	{"big5", 950, "Chinese Traditional (Big5)", traditionalchinese.Big5, []string{"cp950"}},
	{"windows-1250", 1250, "Central European (Windows)", charmap.Windows1250, []string{"cp1250"}},
	{"windows-1251", 1251, "Cyrillic (Windows)", charmap.Windows1251, []string{"cp1251"}},
	{"windows-1252", 1252, "Western European (Windows)", charmap.Windows1252, []string{"cp1252"}},
	{"windows-1253", 1253, "Greek (Windows)", charmap.Windows1253, []string{"cp1253"}},
	{"windows-1254", 1254, "Turkish (Windows)", charmap.Windows1254, []string{"cp1254"}},
	{"windows-1255", 1255, "Hebrew (Windows)", charmap.Windows1255, []string{"cp1255"}},
	{"windows-1256", 1256, "Arabic (Windows)", charmap.Windows1256, []string{"cp1256"}},
	{"windows-1257", 1257, "Baltic (Windows)", charmap.Windows1257, []string{"cp1257"}},
	{"windows-1258", 1258, "Vietnamese (Windows)", charmap.Windows1258, []string{"cp1258"}},
	{"macintosh", 10000, "Western European (Mac)", charmap.Macintosh, []string{"x-mac-roman", "mac"}},
	{"x-mac-cyrillic", 10007, "Cyrillic (Mac)", charmap.MacintoshCyrillic, []string{"x-mac-ukrainian"}},
	{"koi8-r", 20866, "Cyrillic (KOI8-R)", charmap.KOI8R, []string{"koi8"}},
	{"koi8-u", 21866, "Cyrillic (KOI8-U)", charmap.KOI8U, nil},
	{"iso-8859-1", 28591, "Western European (ISO)", charmap.ISO8859_1, []string{"latin1", "l1"}},
	{"iso-8859-2", 28592, "Central European (ISO)", charmap.ISO8859_2, []string{"latin2"}},
	{"iso-8859-3", 28593, "Latin 3 (ISO)", charmap.ISO8859_3, []string{"latin3"}},
	{"iso-8859-4", 28594, "Baltic (ISO)", charmap.ISO8859_4, []string{"latin4"}},
	{"iso-8859-5", 28595, "Cyrillic (ISO)", charmap.ISO8859_5, nil},
	{"iso-8859-6", 28596, "Arabic (ISO)", charmap.ISO8859_6, nil},
	{"iso-8859-7", 28597, "Greek (ISO)", charmap.ISO8859_7, nil},
	{"iso-8859-8", 28598, "Hebrew (ISO-Visual)", charmap.ISO8859_8, nil},
	{"iso-8859-9", 28599, "Turkish (ISO)", charmap.ISO8859_9, []string{"latin5"}},
	{"iso-8859-10", 28600, "Nordic (ISO)", charmap.ISO8859_10, []string{"latin6"}},
	{"iso-8859-13", 28603, "Estonian (ISO)", charmap.ISO8859_13, []string{"latin7"}},
	{"iso-8859-14", 28604, "Celtic (ISO)", charmap.ISO8859_14, []string{"latin8"}},
	{"iso-8859-15", 28605, "Latin 9 (ISO)", charmap.ISO8859_15, []string{"latin9", "latin0"}},
	{"iso-8859-16", 28606, "South-Eastern European (ISO)", charmap.ISO8859_16, []string{"latin10"}},
	{"euc-jp", 51932, "Japanese (EUC)", japanese.EUCJP, nil},
	{"iso-2022-jp", 50220, "Japanese (JIS)", japanese.ISO2022JP, nil},
	{"gb18030", 54936, "Chinese Simplified (GB18030)", simplifiedchinese.GB18030, nil},
	{"hz-gb-2312", 52936, "Chinese Simplified (HZ)", simplifiedchinese.HZGB2312, nil},
}

var (
	byName = map[string]*Encoding{}
	byCP   = map[int]*Encoding{}
	all    []*Encoding
)

func init() {
	add := func(e *Encoding, aliases ...string) {
		byName[strings.ToLower(e.Name)] = e
		for _, a := range aliases {
			byName[strings.ToLower(a)] = e
		}
		byCP[e.CodePage] = e
		all = append(all, e)
	}
	add(UTF8, "utf8")
	add(UTF16LE, "utf-16le", "unicode")
	add(UTF16BE, "unicodefffe")
	for _, t := range table {
		e := &Encoding{Name: t.name, CodePage: t.cp, Display: t.display, enc: t.enc}
		if cm, ok := t.enc.(*charmap.Charmap); ok {
			e.cm = cm
		}
		add(e, t.aliases...)
	}
	sort.Slice(all, func(i, j int) bool { return strings.ToLower(all[i].Display) < strings.ToLower(all[j].Display) })
}

// All is every known encoding, by display name.
func All() []*Encoding { return all }

// ByCodePage is the encoding of a Windows code page number (nil if unknown).
func ByCodePage(cp int) *Encoding { return byCP[cp] }

// Lookup finds an encoding by name (charset label) or code page number;
// nil if unknown.
func Lookup(name string) *Encoding {
	n := strings.ToLower(strings.TrimSpace(name))
	if n == "" {
		return nil
	}
	if e := byName[n]; e != nil {
		return e
	}
	if cp, err := strconv.Atoi(n); err == nil {
		return byCP[cp]
	}
	// any other label the WHATWG or IANA registries know
	if enc, err := htmlindex.Get(n); err == nil {
		return fromEncoding(n, enc)
	}
	if enc, err := ianaindex.IANA.Encoding(n); err == nil && enc != nil {
		return fromEncoding(n, enc)
	}
	return nil
}

func fromEncoding(label string, enc encoding.Encoding) *Encoding {
	if enc == unicode.UTF8 || enc == encoding.Nop {
		return UTF8
	}
	for _, e := range all {
		if e.enc == enc {
			return e
		}
	}
	name, _ := htmlindex.Name(enc)
	if name == "" {
		name = label
	}
	e := &Encoding{Name: name, Display: name, enc: enc}
	if cm, ok := enc.(*charmap.Charmap); ok {
		e.cm = cm
	}
	return e
}

// Culture is a language's default code pages, as Windows' locale tables
// give them.
type Culture struct {
	Lang   string // two-letter ISO 639 code
	ANSI   int
	OEM    int
	Mac    int
	EBCDIC int
}

var cultures = map[string]Culture{
	"en": {"en", 1252, 437, 10000, 37},
	"de": {"de", 1252, 850, 10000, 20273},
	"fr": {"fr", 1252, 850, 10000, 20297},
	"es": {"es", 1252, 850, 10000, 20284},
	"it": {"it", 1252, 850, 10000, 20280},
	"pt": {"pt", 1252, 850, 10000, 37},
	"nl": {"nl", 1252, 850, 10000, 500},
	"sv": {"sv", 1252, 850, 10000, 20278},
	"da": {"da", 1252, 850, 10000, 20277},
	"no": {"no", 1252, 850, 10000, 20277},
	"nb": {"nb", 1252, 850, 10000, 20277},
	"fi": {"fi", 1252, 850, 10000, 20278},
	"is": {"is", 1252, 850, 10079, 20871},
	"ru": {"ru", 1251, 866, 10007, 20880},
	"uk": {"uk", 1251, 866, 10017, 500},
	"be": {"be", 1251, 866, 10007, 500},
	"bg": {"bg", 1251, 866, 10007, 500},
	"sr": {"sr", 1251, 855, 10007, 500},
	"mk": {"mk", 1251, 866, 10007, 500},
	"pl": {"pl", 1250, 852, 10029, 20880},
	"cs": {"cs", 1250, 852, 10029, 500},
	"sk": {"sk", 1250, 852, 10029, 500},
	"hu": {"hu", 1250, 852, 10029, 500},
	"sl": {"sl", 1250, 852, 10029, 500},
	"hr": {"hr", 1250, 852, 10082, 500},
	"ro": {"ro", 1250, 852, 10010, 20880},
	"el": {"el", 1253, 737, 10006, 20273},
	"tr": {"tr", 1254, 857, 10081, 20905},
	"he": {"he", 1255, 862, 10005, 500},
	"ar": {"ar", 1256, 720, 10004, 20420},
	"et": {"et", 1257, 775, 10029, 500},
	"lv": {"lv", 1257, 775, 10029, 500},
	"lt": {"lt", 1257, 775, 10029, 500},
	"vi": {"vi", 1258, 1258, 10000, 500},
	"th": {"th", 874, 874, 10021, 20838},
	"ja": {"ja", 932, 932, 10001, 20290},
	"zh": {"zh", 936, 936, 10008, 500},
	"ko": {"ko", 949, 949, 10003, 20833},
}

// CultureFor is the culture of a language tag ("ru", "ru-RU", "ru_RU.UTF-8");
// English when unknown.
func CultureFor(tag string) Culture {
	t := strings.ToLower(tag)
	if i := strings.IndexAny(t, "-_.@"); i >= 0 {
		t = t[:i]
	}
	if c, ok := cultures[t]; ok {
		return c
	}
	return cultures["en"]
}

// ISO is the ISO 8859 part WebOne picks for a language (Program.GetCodePage).
func ISO(lang string) *Encoding {
	switch strings.ToLower(lang) {
	case "bs", "pl", "cr", "cz", "sk", "sl", "hu":
		return Lookup("iso-8859-2")
	case "mt", "eo":
		return Lookup("iso-8859-3")
	case "kl", "se":
		return Lookup("iso-8859-4")
	case "be", "bu", "mk", "ru", "sr", "ua":
		return Lookup("iso-8859-5")
	case "ar":
		return Lookup("iso-8859-6")
	case "gr":
		return Lookup("iso-8859-7")
	case "hw":
		return Lookup("iso-8859-8")
	case "tr":
		return Lookup("iso-8859-9")
	case "lv", "lt":
		return Lookup("iso-8859-13")
	case "fr", "fi", "es":
		return Lookup("iso-8859-15")
	}
	return Lookup("iso-8859-1")
}

// FromCodePageOr is ByCodePage(cp), or fallback when Go does not know cp.
func FromCodePageOr(cp int, fallback *Encoding) *Encoding {
	if e := ByCodePage(cp); e != nil {
		return e
	}
	return fallback
}
