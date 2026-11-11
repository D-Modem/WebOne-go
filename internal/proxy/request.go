package proxy

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
)

// Request is a client's HTTP request, as WebOne's HttpRequest.
type Request struct {
	Method    string
	RawURL    string
	Proto     string // "HTTP/1.1"
	Major     int
	Minor     int
	Header    *Header
	URL       *url.URL // nil for CONNECT
	Kind      Kind
	Secure    bool // came through the proxy's own TLS (a CONNECT tunnel)
	Remote    *net.TCPAddr
	Local     *net.TCPAddr
	Body      io.Reader // the payload (empty reader if none)
	KeepAlive bool

	conn net.Conn      // the connection (or TLS tunnel)
	br   *bufio.Reader // its reader; CONNECT tunnels continue from it
}

// errDropped: the connection did not carry an HTTP request (logged already).
var errDropped = errors.New("dropped")

// errNonHTTPInTLS: a non-HTTP protocol inside a TLS tunnel.
var errNonHTTPInTLS = errors.New("write your domain:port to webone.conf/[NonHttpSslServers], please")

// readRequestHead reads the request line and headers, up to the blank line.
// Both CRLF and bare LF line ends are taken.
func readRequestHead(br *bufio.Reader) ([]string, error) {
	var lines []string
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			if err == io.EOF && line == "" && len(lines) == 0 {
				return nil, io.EOF
			}
			if err == io.EOF && line != "" {
				lines = append(lines, strings.TrimRight(line, "\r\n"))
				return lines, nil
			}
			return lines, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			if len(lines) == 0 {
				continue // stray blank lines before a request
			}
			return lines, nil
		}
		lines = append(lines, line)
		if len(lines) > 1000 {
			return nil, errors.New("too many header lines")
		}
	}
}

// parseRequest reads one request from the connection.
func (s *Server) parseRequest(br *bufio.Reader, conn net.Conn, secure bool, log logger) (*Request, error) {
	lines, err := readRequestHead(br)
	if err != nil {
		return nil, err
	}
	first := lines[0]
	parts := strings.Split(first, " ")
	if len(parts) != 3 || len(parts[2]) != 8 || !strings.HasPrefix(parts[2], "HTTP/") {
		if secure {
			log.Printf("<Dropped: Non-HTTPS connection: %s", first)
			return nil, errNonHTTPInTLS
		}
		log.Printf("<Dropped: Non-HTTP connection: %s", first)
		return nil, errDropped
	}
	r := &Request{
		Method: parts[0],
		RawURL: parts[1],
		Proto:  parts[2],
		Header: NewHeader(),
		Secure: secure,
		conn:   conn,
		br:     br,
	}
	r.Major, _ = strconv.Atoi(r.Proto[5:6])
	r.Minor, _ = strconv.Atoi(r.Proto[7:8])
	if ta, ok := conn.RemoteAddr().(*net.TCPAddr); ok {
		r.Remote = ta
	} else {
		r.Remote = &net.TCPAddr{IP: net.IPv4zero}
	}
	if ta, ok := conn.LocalAddr().(*net.TCPAddr); ok {
		r.Local = ta
	} else {
		r.Local = &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)}
	}
	for _, l := range lines[1:] {
		i := strings.IndexByte(l, ':')
		if i <= 0 {
			continue
		}
		name := strings.TrimSpace(l[:i])
		value := strings.TrimSpace(l[i+1:])
		r.Header.Add(name, value)
	}

	if strings.HasPrefix(r.RawURL, "ftp:/") && !strings.HasPrefix(r.RawURL, "ftp://") {
		log.Printf("<Dropped (bad FTP protocol address).") // IBM WebExplorer bug
		return nil, errDropped
	}

	// define the URL from the request line and the Host header
	r.Kind, err = s.kindOf(r.RawURL, r.Header.Get("Host"), r.Method == "CONNECT")
	if err != nil {
		log.Printf("<Dropped: %v", err)
		return nil, errDropped
	}
	host := r.Header.Get("Host")
	if host == "" {
		host = s.cfg.Vars.Value("Proxy")
	}
	switch r.Kind {
	case StandardProxy:
		r.URL, err = parseURL(r.RawURL)
	case StandardHttp, StandardRemote:
		r.URL, err = parseURL("http://" + host + r.RawURL)
		if err != nil {
			r.URL, err = parseURL(r.RawURL)
		}
	case AlternateProxy:
		u := r.RawURL[1:]
		if strings.Contains(u, ":/") && !strings.Contains(u, "://") {
			u = strings.Replace(u, ":/", "://", 1)
		}
		if strings.HasPrefix(u, "ttp://") {
			// alternate mode used through the proxy: "http://proxy:port/http://site/"
			if i := strings.Index(u[7:], "/"); i >= 0 {
				u = u[7+i+1:]
			}
			if i := strings.Index(r.RawURL[8:], "/"); i >= 0 {
				r.RawURL = r.RawURL[8+i:]
			}
		}
		r.URL, err = parseURL(u)
	}
	if err != nil {
		log.Printf("<Dropped: bad URL %s", r.RawURL)
		return nil, errDropped
	}

	// the payload
	if cl := r.Header.Get("Content-Length"); cl != "" && cl != "0" {
		n, err := strconv.ParseInt(strings.TrimSpace(cl), 10, 64)
		if err != nil || n < 0 {
			return nil, fmt.Errorf("bad Content-Length %q", cl)
		}
		r.Body = io.LimitReader(br, n)
	} else if strings.Contains(strings.ToLower(r.Header.Get("Transfer-Encoding")), "chunked") && r.Method != "CONNECT" {
		r.Body = httputil.NewChunkedReader(br)
	} else {
		r.Body = strings.NewReader("")
	}

	// persistent connections (Keep-Alive)
	// (WebOne keeps only connections that ask for it; HTTP/1.1 ones are
	// kept unless they say "close", as HTTP/1.1 has it)
	ch := strings.ToLower(r.Header.Get("Connection") + "," + r.Header.Get("Proxy-Connection"))
	switch {
	case strings.Contains(ch, "keep-alive"):
		r.KeepAlive = true
	case r.Major == 1 && r.Minor >= 1 && !strings.Contains(ch, "close"):
		r.KeepAlive = true
	}
	return r, nil
}

// drainBody reads what is left of the payload, so the next request on the
// connection starts where it should.
func (r *Request) drainBody() {
	if r.Body != nil {
		io.Copy(io.Discard, io.LimitReader(r.Body, 64<<20))
	}
}

// ProtoVersion is "1.1".
func (r *Request) ProtoVersion() string { return fmt.Sprintf("%d.%d", r.Major, r.Minor) }

// parseURL parses an absolute URL as .NET's Uri would take it: spaces are
// escaped first.
func parseURL(s string) (*url.URL, error) {
	s = strings.ReplaceAll(s, " ", "%20")
	u, err := url.Parse(s)
	if err != nil {
		return nil, err
	}
	if u.Scheme == "" {
		return nil, fmt.Errorf("not an absolute URL: %s", s)
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	if (u.Scheme == "http" || u.Scheme == "https" || u.Scheme == "ftp") && u.Path == "" && u.Opaque == "" {
		u.Path = "/"
	}
	return u, nil
}

// absURL is a URL as .NET's Uri.AbsoluteUri gives it: default ports left
// out, a path always present.
func absURL(u *url.URL) string {
	if u == nil {
		return ""
	}
	c := *u
	if p := c.Port(); (c.Scheme == "http" && p == "80") || (c.Scheme == "https" && p == "443") || (c.Scheme == "ftp" && p == "21") {
		c.Host = c.Hostname()
		if strings.Contains(c.Host, ":") {
			c.Host = "[" + c.Host + "]"
		}
	}
	if c.Path == "" && c.Opaque == "" && c.Host != "" {
		c.Path = "/"
	}
	return c.String()
}

// pathAndQuery is the path with "?query" when there is one.
func pathAndQuery(u *url.URL) string {
	p := u.EscapedPath()
	if p == "" {
		p = "/"
	}
	if u.RawQuery != "" || u.ForceQuery {
		p += "?" + u.RawQuery
	}
	return p
}

// segments is .NET's Uri.Segments: "/a/b/c" -> ["/", "a/", "b/", "c"].
func segments(u *url.URL) []string {
	p := u.EscapedPath()
	if p == "" {
		return []string{"/"}
	}
	var out []string
	start := 0
	for i := 0; i < len(p); i++ {
		if p[i] == '/' {
			out = append(out, p[start:i+1])
			start = i + 1
		}
	}
	if start < len(p) {
		out = append(out, p[start:])
	}
	return out
}
