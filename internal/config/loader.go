package config

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/D-Modem/retrossl"
	"github.com/D-Modem/webone-go/internal/logx"
	"github.com/D-Modem/webone-go/internal/rx"
	"github.com/D-Modem/webone-go/internal/textenc"
)

// configDir is %WOConfigDir%: the directory of a custom configuration
// file, "." for one named ./webone.conf, else the system's.
func (c *Config) configDir() string {
	if c.CustomConfig != "" {
		return filepath.Dir(c.CustomConfig)
	}
	if strings.HasPrefix(c.ConfigFileName, "./") || strings.HasPrefix(c.ConfigFileName, `.\`) {
		return "."
	}
	return defaultConfigDir()
}

// LoadFile reads a configuration file (and what it includes).
func (c *Config) LoadFile(path string) error {
	if _, ok := c.Vars.Get("WOConfigDir"); !ok {
		c.Vars.Set("WOConfigDir", c.configDir())
	}
	path = c.Expand(path, nil)
	if !isFile(path) {
		return fmt.Errorf("file not found: %s", path)
	}
	full, _ := filepath.Abs(path)
	for _, f := range c.LoadedFiles {
		if f == full {
			return nil // already loaded: no include loops
		}
	}
	c.LoadedFiles = append(c.LoadedFiles, full)
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	short := strings.TrimPrefix(strings.TrimPrefix(path, "./"), `.\`)
	c.LoadContent(data, short)
	return nil
}

// LoadContent adds a configuration file's lines.
func (c *Config) LoadContent(data []byte, name string) {
	logx.Server(true, "Using configuration file %s.", name)
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	n := 0
	for sc.Scan() {
		n++
		loc := fmt.Sprintf("%s, line %d", name, n)
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[Include:") {
			if err := c.loadInclude(line); err != nil {
				logx.Server(true, "Error in %s: %v Line ignored.", loc, err)
			}
			continue
		}
		c.rawEntries = append(c.rawEntries, [2]string{loc, line})
	}
}

func (c *Config) loadInclude(line string) error {
	if !strings.HasSuffix(line, "]") {
		return fmt.Errorf("incorrect include section")
	}
	inc := line[len("[Include:") : len(line)-1]
	c.Vars.Set("WOConfigDir", c.configDir())
	inc = c.Expand(inc, nil)
	if strings.ContainsAny(inc, "*?") {
		dir, mask := filepath.Split(inc)
		if dir == "" {
			dir = "."
		}
		return c.loadDirectory(dir, mask)
	}
	if isDir(inc) {
		return c.loadDirectory(inc, "*")
	}
	return c.LoadFile(inc)
}

func (c *Config) loadDirectory(dir, mask string) error {
	if !isDir(dir) {
		return fmt.Errorf("directory not found: %s", dir)
	}
	files, err := filepath.Glob(filepath.Join(dir, mask))
	if err != nil {
		return err
	}
	sort.Strings(files)
	for _, f := range files {
		if !isFile(f) {
			continue
		}
		if err := c.LoadFile(f); err != nil {
			return fmt.Errorf("can't load included %s: %v", f, err)
		}
	}
	return nil
}

func (c *Config) warn(format string, args ...any) { logx.Server(true, "Warning: "+format, args...) }

// Process turns the loaded lines into settings. Unknown options and
// sections are warnings; a bad regular expression or an option outside any
// section is an error.
func (c *Config) Process() error {
	for _, e := range c.rawEntries {
		loc, line := e[0], e[1]
		if strings.HasPrefix(line, "[") {
			s, err := ParseSection(line, loc)
			if err != nil {
				return fmt.Errorf("%s: %v", loc, err)
			}
			c.sections = append(c.sections, s)
			continue
		}
		if len(c.sections) == 0 {
			return fmt.Errorf("found option outside section (%s)", loc)
		}
		o, err := ParseOption(line, loc)
		if err != nil {
			c.warn("%v at %s. Line ignored.", err, loc)
			continue
		}
		s := c.sections[len(c.sections)-1]
		s.Options = append(s.Options, o)
	}

	for _, s := range c.sections {
		if err := c.processSection(s); err != nil {
			return err
		}
	}

	if c.SslRootValidAfter.IsZero() || c.SslRootValidAfter.Year() <= 1 {
		c.SslRootValidAfter = mustDate("1970-01-01T00:00:00Z")
	}
	if c.SslRootValidBefore.IsZero() || c.SslRootValidBefore.Year() <= 1 {
		c.SslRootValidBefore = mustDate("2070-12-31T23:59:59Z")
	}
	c.SetProxyVars()
	logx.Server(true, "Configuration load complete.")
	for _, f := range c.LoadedFiles {
		logx.Server(false, "Configuration file %s load complete.", f)
	}
	return nil
}

// SetProxyVars sets %Proxy%, %ProxyHost% and %ProxyPort%.
func (c *Config) SetProxyVars() {
	c.Vars.Set("Proxy", c.DefaultHostName+":"+strconv.Itoa(c.Port))
	c.Vars.Set("ProxyHost", c.DefaultHostName)
	c.Vars.Set("ProxyPort", strconv.Itoa(c.Port))
}

func mustDate(s string) (t time.Time) {
	t, _ = ParseDate(s)
	return t
}

func (c *Config) processSection(s *Section) error {
	switch s.Kind {
	case "Server":
		if s.Mask != "" {
			c.warn("Instead of condition-based setup use Edit Sets. Section ignored. (%s).", s.Location)
			return nil
		}
		for _, o := range s.Options {
			if err := c.serverOption(o); err != nil {
				return err
			}
		}
	case "ForceHttps":
		for _, o := range s.Options {
			c.ForceHttps = append(c.ForceHttps, o.Raw)
		}
	case "TextTypes":
		for _, o := range s.Options {
			c.TextTypes = append(c.TextTypes, o.Raw)
		}
	case "ForceUtf8":
		for _, o := range s.Options {
			c.ForceUtf8 = append(c.ForceUtf8, o.Raw)
		}
	case "Converters":
		for _, o := range s.Options {
			cv, err := ParseConverter(o.Raw)
			if err != nil {
				c.warn("%v at %s.", err, o.Location)
				continue
			}
			c.Converters = append(c.Converters, cv)
		}
	case "Edit":
		e, err := NewEditSet(s, c)
		if err != nil {
			return err
		}
		c.EditRules = append(c.EditRules, e)
	case "Translit":
		for _, o := range s.Options {
			if o.HasKeyValue() {
				c.TranslitTable = append(c.TranslitTable, [2]string{o.Key, o.Value})
			}
		}
	case "IpBanList":
		for _, o := range s.Options {
			c.IpBanList = append(c.IpBanList, o.Raw)
		}
	case "IpWhiteList":
		for _, o := range s.Options {
			c.IpWhiteList = append(c.IpWhiteList, o.Raw)
		}
	case "UrlBlackList":
		for _, o := range s.Options {
			c.UrlBlackList = append(c.UrlBlackList, o.Raw)
		}
	case "UrlWhiteList":
		for _, o := range s.Options {
			c.UrlWhiteList = append(c.UrlWhiteList, o.Raw)
		}
	case "HostNames":
		for _, o := range s.Options {
			c.HostNames = append(c.HostNames, c.Expand(o.Raw, nil))
		}
	case "Authenticate":
		for _, o := range s.Options {
			if len(strings.Split(o.Raw, ":")) == 2 && !strings.Contains(o.Raw, " ") {
				c.Authenticate = append(c.Authenticate, o.Raw)
				continue
			}
			if !o.HasKeyValue() {
				c.warn("Invalid authentication credentials at %s.", o.Location)
				continue
			}
			switch o.Key {
			case "AuthenticateMessage":
				c.AuthenticateMessage = o.Value
			case "AuthenticateRealm":
				c.AuthenticateRealm = o.Value
			case "OpenForLocalIPs":
				c.OpenForLocalIPs = ToBool(o.Value)
			default:
				c.warn("Invalid authentication option at %s.", o.Location)
			}
		}
	case "MimeTypes":
		for _, o := range s.Options {
			if o.HasKeyValue() {
				c.MimeTypes[strings.ToLower(o.Key)] = strings.ToLower(o.Value)
			} else {
				c.warn("Invalid content type definition at %s.", o.Location)
			}
		}
	case "PAC":
		var b strings.Builder
		for _, o := range s.Options {
			b.WriteString(o.Raw + "\n")
		}
		c.PAC = b.String()
	case "WebVideoOptions":
		for _, o := range s.Options {
			if o.HasKeyValue() {
				c.WebVideoOptions[o.Key] = o.Value
			} else {
				c.warn("Incorrect online video convert option at %s.", o.Location)
			}
		}
	case "WebVideoProfiles":
		for _, o := range s.Options {
			if o.HasKeyValue() && len(o.Values) == 2 {
				c.WebVideoProfiles[o.Key] = o.Values
			} else {
				c.warn("Incorrect online video convert profile at %s.", o.Location)
			}
		}
	case "SecureProxy":
		for _, o := range s.Options {
			c.secureOption(o)
		}
	case "NonHttpSslServers":
		for _, o := range s.Options {
			c.NonHttpSslServers = append(c.NonHttpSslServers, strings.ToLower(o.Raw))
		}
	case "NonHttpConnectRedirect", "NonHttpSslDecrypt":
		for _, o := range s.Options {
			parts := strings.Split(o.Value, ":")
			okTarget := o.HasKeyValue() && len(parts) == 2 && strings.TrimSpace(parts[0]) != ""
			if okTarget {
				_, err := strconv.Atoi(parts[1])
				okTarget = err == nil
			}
			if !okTarget {
				c.warn("Incorrect non-HTTP connection redirect rule at %s.", o.Location)
				continue
			}
			if s.Kind == "NonHttpConnectRedirect" {
				c.NonHttpConnectRedirect[o.Key] = o.Value
			} else {
				c.NonHttpSslDecrypt[o.Key] = o.Value
			}
		}
	case "ConnectPassThrough":
		for _, o := range s.Options {
			c.ConnectPassThrough = append(c.ConnectPassThrough, strings.ToLower(o.Raw))
		}
	case "Http10Only":
		for _, o := range s.Options {
			if err := checkRegExp(o.Raw, o.Location); err != nil {
				return err
			}
			c.Http10Only = append(c.Http10Only, o.Raw)
		}
	case "ActivexGallery", "ActiveXGallery":
		c.activexSection(s)
	case "Variables":
		for _, o := range s.Options {
			if o.HasKeyValue() {
				c.Vars.Set(o.Key, o.Value)
			}
		}
	case "FixableURL", "FixableType", "ContentPatch":
		c.warn("%s section at %s is no longer supported.", s.Kind, s.Location)
		logx.Server(true, "See https://github.com/atauenis/webone/wiki/Configuration-file about [%s].", s.Kind)
	default:
		c.warn("Unknown section %s in %s.", s.Kind, s.Location)
	}
	return nil
}

func (c *Config) serverOption(o Option) error {
	switch o.Key {
	case "Port":
		p := c.Expand(o.Value, nil)
		n, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil {
			return fmt.Errorf("Port: %q is not a TCP/IP port number", p)
		}
		if n < 0 || n > 65535 {
			return fmt.Errorf("Port: %q is out of the range of valid values", p)
		}
		c.Port = n
	case "Port2", "HttpPort", "HttpsPort", "FtpPort":
		c.warn("Use of '%s' is deprecated, use 'Port' at %s.", o.Key, o.Location)
	case "IP":
		ip := net.ParseIP(strings.Trim(strings.TrimSpace(o.Value), "[]"))
		if ip == nil {
			return fmt.Errorf("IP: %q is not an IP address", o.Value)
		}
		c.IP = ip
	case "OutputEncoding":
		c.OutputEncoding = c.CodePage(c.Expand(o.Value, nil))
	case "Authenticate":
		c.Authenticate = append(c.Authenticate, o.Value)
	case "AuthenticateMessage":
		c.AuthenticateMessage = o.Value
	case "AuthenticateRealm":
		c.AuthenticateRealm = o.Value
	case "HideClientErrors":
		c.HideClientErrors = ToBool(o.Value)
	case "SearchInArchive":
		c.SearchInArchive = ToBool(o.Value)
	case "HideArchiveRedirect":
		c.HideArchiveRedirect = ToBool(o.Value)
	case "ShortenArchiveErrors":
		c.ShortenArchiveErrors = ToBool(o.Value)
	case "ArchiveUrlSuffix":
		c.ArchiveUrlSuffix = o.Value
	case "UserAgent":
		c.UserAgent = o.Value
	case "DefaultHostName":
		sugg := NewVars()
		for i, ip := range LocalIPs() {
			sugg.Set("IP"+strconv.Itoa(i), ip.String())
		}
		host, _ := os.Hostname()
		sugg.Set("HostName", host)
		c.DefaultHostName = c.Expand(o.Value, sugg)
		valid := strings.EqualFold(host, c.DefaultHostName)
		for _, ip := range LocalIPs() {
			if ip.String() == c.DefaultHostName {
				valid = true
			}
		}
		if !valid {
			if addrs, err := net.LookupHost(c.DefaultHostName); err == nil && len(addrs) > 0 {
				valid = true
			}
		}
		if !valid {
			c.warn("DefaultHostName setting is not applicable to this computer.")
		}
	case "ValidateCertificates":
		c.ValidateCertificates = ToBool(o.Value)
	case "TemporaryDirectory":
		c.TemporaryDirectory = c.Expand(o.Value, nil)
		if !isDir(c.TemporaryDirectory) {
			c.warn("Temporary directory %s does not exists.", o.Value)
		}
	case "LogFile", "AppendLogFile":
		c.LogFile = strings.ReplaceAll(strings.ReplaceAll(o.Value, `\\`, `\`), "//", "/")
		c.LogFileAppend = o.Key == "AppendLogFile"
	case "DisplayStatusPage":
		c.DisplayStatusPage = c.Expand(o.Value, nil)
	case "ArchiveDateLimit":
		n, _ := strconv.Atoi(strings.TrimSpace(o.Value))
		if n > 10000000 && n < 99990000 {
			c.ArchiveDateLimit = n
		} else {
			c.warn("The ArchiveDateLimit must be in YYYYMMDD format.")
		}
	case "UpperProxy":
		c.UpperProxy = c.Expand(o.Value, nil)
	case "MultipleHttp2Connections":
		c.MultipleHttp2Conns = ToBool(o.Value)
	case "SecurityProtocols":
		c.SecurityProtocols = nil
		for _, n := range strings.Split(o.Value, ",") {
			n = strings.ToLower(strings.TrimSpace(n))
			if v, ok := tlsVersionNames[n]; ok {
				c.SecurityProtocols = append(c.SecurityProtocols, v)
			} else if n != "" {
				c.warn("SecurityProtocols: %s is not available, at %s.", n, o.Location)
			}
		}
	case "RemoteHttpVersion":
		if v, ok := ParseHTTPVersionRule(o.Value); ok {
			c.RemoteHttpVersion = v
		} else {
			c.warn("Incorrect RemoteHttpVersion '%s'.", o.Value)
		}
	case "AllowHttpCompression":
		c.AllowHttpCompression = ToBool(o.Value)
	case "EnableWebFtp":
		c.EnableWebFtp = ToBool(o.Value)
	case "ShowFileIcons":
		c.ShowFileIcons = ToBool(o.Value)
	case "UseMsHttpApi":
		if ToBool(o.Value) {
			c.warn("UseMsHttpApi is a Windows HTTP.SYS option of the .NET WebOne; ignored.")
		}
	case "EnableManualConverting":
		c.EnableManualConverting = ToBool(o.Value)
	case "ContentDirectory":
		dir := c.Expand(o.Value, nil)
		alt := dir
		if exe, err := os.Executable(); err == nil {
			alt = filepath.Join(filepath.Dir(exe), dir)
		}
		switch {
		case isDir(dir):
			c.ContentDirectory = dir
		case isDir(alt):
			c.ContentDirectory = alt
		case isDir("." + dir):
			c.ContentDirectory = "." + dir
		default:
			c.warn("Incorrect ContentDirectory '%s'.", dir)
		}
	case "DontPreferHTTPS":
		c.DontPreferHTTPS = ToBool(o.Value)
	case "ConnectionTimeout":
		n, err := strconv.Atoi(strings.TrimSpace(o.Value))
		if err != nil {
			c.warn("Incorrect ConnectionTimeout '%s'.", o.Value)
		} else {
			c.ConnectionTimeout = n
		}
	case "CodepageCulture":
		c.CodepageCulture = textenc.CultureFor(o.Value)
	case "CodepageANSI":
		c.CodepageANSI = textenc.Lookup(o.Value)
	case "CodepageASCII":
		c.CodepageASCII = textenc.Lookup(o.Value)
	case "CodepageMac":
		c.CodepageMac = textenc.Lookup(o.Value)
	case "CodepageEBCDIC":
		c.CodepageEBCDIC = textenc.Lookup(o.Value)
	case "DnsServer":
		c.DnsServer = c.Expand(strings.TrimSpace(o.Value), nil)
		if c.DnsServer != "" {
			if _, _, err := net.SplitHostPort(c.DnsServer); err != nil {
				c.DnsServer = net.JoinHostPort(strings.Trim(c.DnsServer, "[]"), "53")
			}
		}
	default:
		c.warn("Unknown server option %s in %s.", o.Key, o.Location)
	}
	return nil
}

var tlsVersionNames = map[string]uint16{
	"tls": tls.VersionTLS10, "tls10": tls.VersionTLS10, "tls11": tls.VersionTLS11,
	"tls12": tls.VersionTLS12, "tls13": tls.VersionTLS13,
}

func cleanPath(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, `\\`, `\`), "//", "/")
}

func (c *Config) secureOption(o Option) {
	switch o.Key {
	case "SslEnable":
		c.SslEnable = ToBool(o.Value)
	case "SslCertificate":
		c.SslCertificate = cleanPath(c.Expand(o.Value, nil))
	case "SslPrivateKey":
		c.SslPrivateKey = cleanPath(c.Expand(o.Value, nil))
	case "SslCaDirectory":
		c.SslCaDirectory = cleanPath(c.Expand(o.Value, nil))
	case "SslTrustOwnCA":
		c.SslTrustOwnCA = ToBool(o.Value)
	case "SslProtocols":
		c.SslProtocols = nil
		c.SslProtocolsSet = true
		for _, p := range strings.Split(o.Value, ",") {
			p = strings.ToLower(strings.TrimSpace(p))
			switch p {
			case "", "none", "default":
				c.SslProtocolsSet = p == "" && c.SslProtocolsSet
			case "ssl2": // served by retrossl
				c.SslProtocols = append(c.SslProtocols, retrossl.VersionSSL20)
			case "ssl3":
				c.SslProtocols = append(c.SslProtocols, retrossl.VersionSSL30)
			default:
				if v, ok := tlsVersionNames[p]; ok {
					c.SslProtocols = append(c.SslProtocols, v)
				} else {
					c.warn("incorrect SSL/TLS protocol set '%s', at %s.", o.Value, o.Location)
				}
			}
		}
		if len(c.SslProtocols) == 0 {
			c.SslProtocolsSet = false
		}
	case "SslCipherSuites":
		names := map[string]uint16{}
		for _, cs := range tls.CipherSuites() {
			names[cs.Name] = cs.ID
		}
		for _, cs := range tls.InsecureCipherSuites() {
			names[cs.Name] = cs.ID
		}
		for _, cs := range retrossl.CipherSuites() { // SSL 3.0's DES and export suites
			names[cs.Name] = cs.ID
		}
		// .NET's names for the TLS 1.3 suites and others are the IANA ones
		for _, n := range strings.Split(o.Value, ",") {
			n = strings.TrimSpace(n)
			if id, ok := names[n]; ok {
				c.SslCipherSuites = append(c.SslCipherSuites, id)
			} else {
				c.warn("incorrect cipher suite '%s', at %s.", n, o.Location)
			}
		}
	case "SslHashAlgorithm":
		c.SslHashAlgorithmSet = true
		switch strings.ToUpper(strings.TrimSpace(o.Value)) {
		case "MD5", "1.2.840.113549.2.5":
			c.SslHashAlgorithm = "MD5"
		case "SHA", "SHA1", "1.3.14.3.2.26":
			c.SslHashAlgorithm = "SHA1"
		case "SHA2", "SHA256", "2.16.840.1.101.3.4.2.1":
			c.SslHashAlgorithm = "SHA256"
		case "SHA3", "SHA384", "2.16.840.1.101.3.4.2.2":
			c.SslHashAlgorithm = "SHA384"
		case "SHA5", "SHA512", "2.16.840.1.101.3.4.2.3":
			c.SslHashAlgorithm = "SHA512"
		default:
			c.warn("'%s' is not a valid hash algorithm name, at %s.", o.Value, o.Location)
		}
	case "SslRootSubject":
		if !strings.Contains(o.Value, "CN=") {
			c.warn("'%s' is not a valid X.500 distinguished subject name, at %s.", o.Value, o.Location)
		} else {
			c.SslRootSubject = c.Expand(o.Value, nil)
		}
	case "SslRootValidAfter", "SslRootValidBefore":
		t, err := ParseDate(o.Value)
		if err != nil {
			c.warn("%v, at %s.", err, o.Location)
			return
		}
		if o.Key == "SslRootValidAfter" {
			c.SslRootValidAfter = t
		} else {
			c.SslRootValidBefore = t
		}
	case "SslCertVaildBeforeNow", "SslCertValidBeforeNow":
		n, err := strconv.Atoi(strings.TrimSpace(o.Value))
		if err == nil && n <= 0 {
			c.SslCertValidBeforeNow = n
		} else {
			c.warn("SslCertVaildBeforeNow must be a negative whole number of days, at %s.", o.Location)
		}
	case "SslCertVaildAfterNow", "SslCertValidAfterNow":
		n, err := strconv.Atoi(strings.TrimSpace(o.Value))
		if err == nil && n >= 1 {
			c.SslCertValidAfterNow = n
		} else {
			c.warn("SslCertVaildAfterNow must be a positive whole number of days, at %s.", o.Location)
		}
	case "SslSiteCerts":
		c.SslSiteCerts = cleanPath(c.Expand(o.Value, nil))
	case "SslSiteCertGenerator":
		c.SslSiteCertGenerator = cleanPath(c.Expand(o.Value, nil))
	case "AllowNonHttpsCONNECT":
		c.AllowNonHttpsCONNECT = ToBool(o.Value)
	}
}

func (c *Config) activexSection(s *Section) {
	for _, o := range s.Options {
		if o.HasKeyValue() {
			if strings.EqualFold(o.Key, "ActiveXGalleryEmulation") {
				c.ActivexGalleryEmul = ToBool(o.Value)
				continue
			}
			clsid := strings.ToUpper(strings.NewReplacer("{", "", "}", "").Replace(o.Key))
			lower := strings.ToLower(clsid)
			if !rx.Match("[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}", lower) &&
				!rx.Match(`[a-z0-9.\-_]*/[a-z0-9.\-_]`, lower) {
				c.warn("Incorrect Internet Component GUID or type at %s.", o.Location)
				continue
			}
			if strings.HasSuffix(o.Key, ",latest") {
				ver := strings.ReplaceAll(o.Value, ",", ".")
				if !rx.Match(`^\d+(\.\d+){1,3}$`, ver) {
					c.warn("Incorrect Internet Component latest version at %s.", o.Location)
					continue
				}
				c.ActivexGalleryCLSIDs[clsid] = ver
				continue
			}
			c.ActivexGalleryCLSIDs[clsid] = o.Value
			continue
		}
		if rx.Match("^http://", o.Raw) {
			c.ActivexGalleryUrls = append(c.ActivexGalleryUrls, o.Raw)
		} else {
			c.warn("Incorrect Microsoft codebase search engine option at %s.", o.Location)
		}
	}
}

// LocalIPs is this machine's addresses, and 10.0.2.2 (the host as QEMU,
// SheepShaver and Basilisk II's SLIRP networking sees it).
func LocalIPs() []net.IP {
	var ips []net.IP
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok {
				ips = append(ips, n.IP)
			}
		}
	}
	ips = append(ips, net.ParseIP("10.0.2.2"))
	return ips
}
