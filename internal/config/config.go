package config

import (
	"crypto/tls"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/D-Modem/webone-go/internal/logx"
	"github.com/D-Modem/webone-go/internal/rx"
	"github.com/D-Modem/webone-go/internal/textenc"
)

// Converter is a [Converters] line: an external program that converts
// content (ImageMagick's convert, ffmpeg ...), with %SRC%, %DEST%, %ARG1%,
// %ARG2%, %DESTEXT% and %SRCURL% in its command line.
type Converter struct {
	Executable   string
	CommandLine  string
	UseStdout    bool // no %DEST%: the result is on standard output
	UseStdin     bool // no %SRC%: the source goes to standard input
	SelfDownload bool // %SRCURL%: the program fetches the source itself
}

// ParseConverter parses a [Converters] line.
func ParseConverter(s string) (Converter, error) {
	i := strings.IndexByte(s, ' ')
	if i < 0 {
		return Converter{}, fmt.Errorf("converter line is invalid")
	}
	c := Converter{Executable: s[:i], CommandLine: s[i+1:]}
	c.UseStdout = !strings.Contains(c.CommandLine, "%DEST%")
	c.UseStdin = !strings.Contains(c.CommandLine, "%SRC%")
	c.SelfDownload = strings.Contains(c.CommandLine, "%SRCURL%")
	if c.UseStdin && c.SelfDownload {
		c.UseStdin = false
	}
	return c, nil
}

// Config is the whole configuration (ConfigFile.cs).
type Config struct {
	// [Server]
	Port                   int
	IP                     net.IP
	ForceHttps             []string
	ForceUtf8              []string
	TextTypes              []string
	OutputEncoding         *textenc.Encoding // nil: as is
	CodepageANSI           *textenc.Encoding
	CodepageASCII          *textenc.Encoding
	CodepageMac            *textenc.Encoding
	CodepageEBCDIC         *textenc.Encoding
	CodepageCulture        textenc.Culture
	Authenticate           []string
	AuthenticateMessage    string
	AuthenticateRealm      string
	OpenForLocalIPs        bool
	HideClientErrors       bool
	SearchInArchive        bool
	HideArchiveRedirect    bool
	ArchiveUrlSuffix       string
	ShortenArchiveErrors   bool
	ArchiveDateLimit       int
	Converters             []Converter
	UserAgent              string
	DefaultHostName        string
	ValidateCertificates   bool
	EditRules              []*EditSet
	TranslitTable          [][2]string
	TemporaryDirectory     string
	DisplayStatusPage      string
	IpBanList              []string
	IpWhiteList            []string
	UrlBlackList           []string
	UrlWhiteList           []string
	HostNames              []string
	UpperProxy             string
	PAC                    string
	MultipleHttp2Conns     bool
	RemoteHttpVersion      string
	WebVideoOptions        map[string]string
	WebVideoProfiles       map[string][]string
	AllowHttpCompression   bool
	MimeTypes              map[string]string
	EnableWebFtp           bool
	ShowFileIcons          bool
	Http10Only             []string
	EnableManualConverting bool
	ContentDirectory       string
	DontPreferHTTPS        bool
	ConnectionTimeout      int
	ActivexGalleryEmul     bool
	ActivexGalleryUrls     []string
	ActivexGalleryCLSIDs   map[string]string
	// DnsServer (Go port): resolve upstream names through this server
	// ("host:port"), such as the BBS's own DNS, rather than the system's.
	DnsServer string

	// [SecureProxy]
	SslEnable              bool
	SslCertificate         string
	SslPrivateKey          string
	SslProtocols           []uint16 // versions for clients; SSL 2.0 and 3.0 are retrossl's
	SslProtocolsSet        bool
	SslCipherSuites        []uint16
	SslHashAlgorithm       string   // MD5, SHA1, SHA256, SHA384, SHA512
	SslHashAlgorithmSet    bool     // given in the configuration
	SecurityProtocols      []uint16 // TLS versions for fetching from servers
	SslRootSubject         string
	SslRootValidAfter      time.Time
	SslRootValidBefore     time.Time
	SslCertValidBeforeNow  int
	SslCertValidAfterNow   int
	SslSiteCerts           string
	SslSiteCertGenerator   string
	AllowNonHttpsCONNECT   bool
	NonHttpSslServers      []string
	NonHttpConnectRedirect map[string]string
	ConnectPassThrough     []string
	NonHttpSslDecrypt      map[string]string
	// SslCaDirectory (Go port): use the CA in this directory (ca.crt and
	// ca.key, as the D-Modem BBS keeps its own); never made or replaced.
	SslCaDirectory string
	// SslTrustOwnCA (Go port): also trust the proxy's CA when fetching
	// from servers (for sites the BBS serves under its own CA).
	SslTrustOwnCA bool

	// Vars is [Variables] plus the built-in ones (WOVer, WOSystem,
	// WOConfigDir, Proxy, ProxyHost, ProxyPort).
	Vars *Vars

	// where the configuration came from
	ConfigFileName string
	CustomConfig   string
	LoadedFiles    []string
	rawEntries     [][2]string // location, line
	sections       []*Section

	// LogFile/AppendLogFile seen in [Server]; opened by the caller unless
	// the command line overrides it
	LogFile       string
	LogFileAppend bool
}

// DefaultCASubject is the subject of a CA WebOne makes; %number% becomes a
// random number (two CAs of one name confuse Firefox).
const DefaultCASubject = "CN=WebOne Certificate Authority %number%,OU=This is not really secure connection,O=MITM Proxy,C=SU"

// Default PAC script parts.
const (
	DefaultPACHeader = "function FindProxyForURL(url, host){\n"
	DefaultPACHttp   = "if (url.substring(0, 5) == 'http:')\n{ return 'PROXY %PACProxy%'; }\n"
	DefaultPACHttps  = "if (url.substring(0, 6) == 'https:')\n{ return 'PROXY %PACProxy%'; }\n"
	DefaultPACFtp    = "if (url.substring(0, 4) == 'ftp:')\n{ return 'PROXY %PACProxy%'; }\n"
	DefaultPACFooter = "} /*WebOne PAC*/ "
)

// New is the default configuration (before any file).
func New() *Config {
	wd, _ := os.Getwd()
	host, _ := os.Hostname()
	lang := os.Getenv("LC_ALL")
	if lang == "" {
		lang = os.Getenv("LANG")
	}
	return &Config{
		Port:                   80,
		TextTypes:              []string{"text/", "javascript"},
		CodepageCulture:        textenc.CultureFor(lang),
		AuthenticateMessage:    "<p>Hello! This Web 2.0-to-1.0 proxy server is private. Please reload this page and enter your credentials in browser's pop-up window.</p>",
		AuthenticateRealm:      "WebOne",
		UserAgent:              "%Original% WebOne/%WOVer%",
		DefaultHostName:        host,
		ValidateCertificates:   true,
		TemporaryDirectory:     wd + string(os.PathSeparator),
		DisplayStatusPage:      "full",
		MultipleHttp2Conns:     true,
		RemoteHttpVersion:      "auto",
		WebVideoOptions:        map[string]string{},
		WebVideoProfiles:       map[string][]string{},
		AllowHttpCompression:   true,
		MimeTypes:              map[string]string{},
		EnableWebFtp:           true,
		ShowFileIcons:          true,
		EnableManualConverting: true,
		ContentDirectory:       "./html",
		ConnectionTimeout:      100,
		ActivexGalleryEmul:     true,
		ActivexGalleryCLSIDs:   map[string]string{},
		SslEnable:              true,
		SslCertificate:         "ssl.crt",
		SslPrivateKey:          "ssl.key",
		SslHashAlgorithm:       "SHA1",
		SslRootSubject:         DefaultCASubject,
		SslCertValidBeforeNow:  -7,
		SslCertValidAfterNow:   7,
		AllowNonHttpsCONNECT:   true,
		NonHttpConnectRedirect: map[string]string{},
		NonHttpSslDecrypt:      map[string]string{},
		Vars:                   NewVars(),
	}
}

// ToBool reads a yes/no value: 1/0, y/n, yes/no, on/off, enable/disable,
// true/false. Anything else is false (WebOne throws; a warning is logged
// by callers that care).
func ToBool(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "y", "yes", "on", "enable", "true":
		return true
	}
	return false
}

// IsBool: s is one of the words ToBool knows.
func IsBool(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "y", "yes", "on", "enable", "true", "0", "n", "no", "off", "disable", "false", "":
		return true
	}
	return false
}

// ParseVersion reads "1.1" or "2".
func ParseVersion(s string) (major, minor int, ok bool) {
	s = strings.TrimSpace(s)
	parts := strings.Split(s, ".")
	if len(parts) == 0 || len(parts) > 4 {
		return 0, 0, false
	}
	var err error
	if major, err = strconv.Atoi(parts[0]); err != nil {
		return 0, 0, false
	}
	if len(parts) > 1 {
		if minor, err = strconv.Atoi(parts[1]); err != nil {
			return 0, 0, false
		}
	}
	return major, minor, true
}

// ParseHTTPVersionRule reads a RemoteHttpVersion / AddRequestHttpVersion
// value: "1.1" (exactly), "=1.1", ">1.1", "<2.0" or "auto".
func ParseHTTPVersionRule(s string) (string, bool) {
	if rx.Match(`[\d][\.][\d]`, s) && !rx.Match(`^[=<>]`, s) {
		return "=" + s, true
	}
	if rx.Match(`([=><a])[u0-3][t\.][o0-9]`, s) {
		return s, true
	}
	return "", false
}

// ParseDate reads a date as .NET's DateTimeOffset.Parse would for the forms
// in webone.conf ("2019-05-20 00:00:00+0"), or "now".
func ParseDate(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if strings.EqualFold(s, "now") {
		return time.Now(), nil
	}
	// "+0" and "+3" offsets: make them "+00:00"
	if m := rx.FindGroups(`^(\d{4}-\d{2}-\d{2}[ T]\d{2}:\d{2}:\d{2})([+-])(\d{1,2})$`, s); m != nil {
		h, _ := strconv.Atoi(m[3])
		s = fmt.Sprintf("%s%s%02d:00", m[1], m[2], h)
	}
	layouts := []string{
		"2006-01-02 15:04:05-07:00", "2006-01-02T15:04:05-07:00", "2006-01-02 15:04:05Z07:00",
		time.RFC3339, "2006-01-02 15:04:05", "2006-01-02T15:04:05", "2006-01-02",
		time.RFC1123, time.RFC1123Z, time.RFC850, time.ANSIC, "01/02/2006 15:04:05", "01/02/2006",
	}
	for _, l := range layouts {
		if t, err := time.Parse(l, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("cannot read date %q", s)
}

// CodePage resolves an OutputEncoding value: AsIs (nil), Windows/ANSI,
// DOS/OEM, Mac/Apple, EBCDIC/IBM, ISO, or a code page name or number.
func (c *Config) CodePage(cp string) *textenc.Encoding {
	switch strings.ToLower(strings.TrimSpace(cp)) {
	case "windows", "win", "ansi":
		if c.CodepageANSI != nil {
			return c.CodepageANSI
		}
		return textenc.FromCodePageOr(c.CodepageCulture.ANSI, textenc.ByCodePage(1252))
	case "dos", "oem", "ascii":
		if c.CodepageASCII != nil {
			return c.CodepageASCII
		}
		return textenc.FromCodePageOr(c.CodepageCulture.OEM, textenc.ByCodePage(437))
	case "mac", "apple":
		if c.CodepageMac != nil {
			return c.CodepageMac
		}
		return textenc.FromCodePageOr(c.CodepageCulture.Mac, textenc.ByCodePage(10000))
	case "ebcdic", "ibm":
		if c.CodepageEBCDIC != nil {
			return c.CodepageEBCDIC
		}
		return textenc.FromCodePageOr(c.CodepageCulture.EBCDIC, textenc.ByCodePage(37))
	case "iso", "iso-8859", "iso8859":
		return textenc.ISO(c.CodepageCulture.Lang)
	case "0", "asis", "":
		return nil
	}
	if e := textenc.Lookup(cp); e != nil {
		return e
	}
	logx.Server(true, "Warning: Unknown codepage %s, using AsIs.", cp)
	return nil
}

// Expand expands variables in s (global ones, then extra).
func (c *Config) Expand(s string, extra *Vars) string { return Expand(s, c.Vars, extra) }

// configDirs is where WebOne looks for webone.conf and webone.conf.d.
func defaultConfigDir() string {
	dir := "."
	switch runtime.GOOS {
	case "windows":
		if d := os.Getenv("ProgramData"); d != "" && isDir(filepath.Join(d, "WebOne")) {
			dir = filepath.Join(d, "WebOne")
		}
		if d, err := os.UserConfigDir(); err == nil && isDir(filepath.Join(d, "WebOne")) {
			dir = filepath.Join(d, "WebOne")
		}
	default:
		home, _ := os.UserHomeDir()
		for _, d := range []string{"/etc/webone.conf.d/", filepath.Join(home, ".config/webone/"),
			"/Library/Application Support/WebOne/", filepath.Join(home, "Library/Application Support/WebOne/")} {
			if isDir(d) {
				dir = d
			}
		}
	}
	return dir
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

func isFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// FindConfigFile is the configuration file to use: custom if given, else
// webone.conf in the current directory, the program's directory, the
// user's configuration directory, or the system's.
func FindConfigFile(custom string) (string, error) {
	if custom != "" {
		if isFile(custom) {
			abs, _ := filepath.Abs(custom)
			return abs, nil
		}
		return "", fmt.Errorf("custom configuration file is not found: %s", custom)
	}
	exeDir := "."
	if exe, err := os.Executable(); err == nil {
		exeDir = filepath.Dir(exe)
	}
	home, _ := os.UserHomeDir()
	var cands []string
	switch runtime.GOOS {
	case "windows":
		cands = []string{`.\webone.conf`, filepath.Join(exeDir, "webone.conf")}
		if d, err := os.UserConfigDir(); err == nil {
			cands = append(cands, filepath.Join(d, "WebOne", "webone.conf"))
		}
		if d := os.Getenv("ProgramData"); d != "" {
			cands = append(cands, filepath.Join(d, "WebOne", "webone.conf"))
		}
	default:
		user := filepath.Join(home, ".config/webone/webone.conf")
		common := "/etc/webone.conf"
		if p := filepath.Join(home, "Library/Application Support/WebOne/webone.conf"); isFile(p) {
			user = p
		}
		if p := "/Library/Application Support/WebOne/webone.conf"; isFile(p) {
			common = p
		}
		cands = []string{"./webone.conf", filepath.Join(exeDir, "webone.conf"), user, common}
	}
	for _, p := range cands {
		if isFile(p) {
			return p, nil
		}
	}
	return "", fmt.Errorf("cannot guess configuration file name; run webone with /full/path/to/webone.conf or create webone.conf in program directory")
}

// TLSVersionName names a TLS version for the log.
func TLSVersionName(v uint16) string {
	switch v {
	case tls.VersionTLS10:
		return "Tls"
	case tls.VersionTLS11:
		return "Tls11"
	case tls.VersionTLS12:
		return "Tls12"
	case tls.VersionTLS13:
		return "Tls13"
	}
	return fmt.Sprintf("0x%04x", v)
}
