// Package proxy is WebOne's HTTP proxy server: it takes requests from old
// browsers (HTTP/1.0 or 1.1, as a proxy, as a web server, or in
// "alternate" /http://site/ form), fetches the pages over today's HTTPS,
// edits them as the configuration's edit sets say, and sends them back in
// a form and code page the browser can read.
package proxy

import (
	"bufio"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/D-Modem/retrossl"
	"github.com/D-Modem/webone-go/internal/certs"
	"github.com/D-Modem/webone-go/internal/config"
	"github.com/D-Modem/webone-go/internal/logx"
)

// Version is WebOne's version this port follows, marked as the Go port.
const Version = "0.19.0-go"

// logger is what request handling logs through.
type logger interface {
	Printf(format string, args ...any)
}

// Server is a running WebOne proxy.
type Server struct {
	cfg    *config.Config
	CA     *certs.CA // signs site certificates; nil when HTTPS is off
	RootCA *certs.CA // what clients install (the CA itself, or the root above it)
	issuer *certs.Issuer

	up *upstream

	ln      net.Listener
	working atomic.Bool

	opened atomic.Int64 // open connections
	busy   atomic.Int64 // connections with a request in progress

	forceMu    sync.RWMutex
	forceHttps []string

	lastMu          sync.Mutex
	lastURL         string
	lastCode        int
	lastContentType string

	clientTLS   *tls.Config
	clientRetro *retrossl.Config // nil: no SSL 2.0, SSL 3.0 or SSLv2-format hellos
	noGoTLS     bool             // SslProtocols has only SSL 2.0 and 3.0
	started     time.Time
}

// New makes a server for a loaded configuration. ca may be nil (HTTPS off).
func New(cfg *config.Config, ca, root *certs.CA) (*Server, error) {
	s := &Server{
		cfg:             cfg,
		CA:              ca,
		RootCA:          root,
		forceHttps:      append([]string(nil), cfg.ForceHttps...),
		lastURL:         "http://999.999.999.999/CON",
		lastCode:        200,
		lastContentType: "not-a-carousel",
		started:         time.Now(),
	}
	if s.RootCA == nil {
		s.RootCA = ca
	}
	if ca != nil {
		s.issuer = &certs.Issuer{
			CA:             ca,
			Hash:           cfg.SslHashAlgorithm,
			ValidBeforeNow: cfg.SslCertValidBeforeNow,
			ValidAfterNow:  cfg.SslCertValidAfterNow,
			SiteCerts:      cfg.SslSiteCerts,
			Generator:      cfg.SslSiteCertGenerator,
			Expand: func(str, subject string) string {
				v := config.NewVars()
				v.Set("Subject", subject)
				return cfg.Expand(str, v)
			},
			Log: func(f string, a ...any) { logx.Std.Printf(f, a...) },
		}
		s.clientTLS = s.makeClientTLS()
		s.clientRetro = s.makeClientRetro()
	}
	var extraRoots []*x509.Certificate
	if cfg.SslTrustOwnCA && s.RootCA != nil {
		extraRoots = append(extraRoots, s.RootCA.Cert)
		if ca != nil && ca != s.RootCA {
			extraRoots = append(extraRoots, ca.Cert)
		}
	}
	up, err := newUpstream(cfg, extraRoots)
	if err != nil {
		return nil, err
	}
	s.up = up
	return s, nil
}

// Config is the server's configuration.
func (s *Server) Config() *config.Config { return s.cfg }

// makeClientTLS is the TLS configuration for clients that CONNECT: old
// versions and ciphers on, as old browsers need.
func (s *Server) makeClientTLS() *tls.Config {
	c := &tls.Config{
		MinVersion: tls.VersionTLS10,
		MaxVersion: tls.VersionTLS13,
		GetCertificate: func(h *tls.ClientHelloInfo) (*tls.Certificate, error) {
			return nil, errors.New("no certificate")
		},
	}
	if s.cfg.SslProtocolsSet && len(s.cfg.SslProtocols) > 0 {
		lo, hi := uint16(0xffff), uint16(0)
		for _, v := range s.cfg.SslProtocols {
			if v >= tls.VersionTLS10 { // SSL 2.0 and 3.0 are retrossl's
				lo, hi = min(lo, v), max(hi, v)
			}
		}
		if hi != 0 {
			c.MinVersion, c.MaxVersion = lo, hi
		} else {
			s.noGoTLS = true
		}
	}
	if len(s.cfg.SslCipherSuites) > 0 {
		c.CipherSuites = s.cfg.SslCipherSuites
	} else {
		// everything Go has, the old and weak last: an old browser picks
		// what it knows
		for _, cs := range tls.CipherSuites() {
			c.CipherSuites = append(c.CipherSuites, cs.ID)
		}
		for _, cs := range tls.InsecureCipherSuites() {
			c.CipherSuites = append(c.CipherSuites, cs.ID)
		}
	}
	return c
}

// tlsFor is the TLS configuration that presents host's certificate,
// with retrossl's for old clients (nil when they are off).
func (s *Server) tlsFor(host string) (*tls.Config, *retrossl.Config, error) {
	cert, err := s.issuer.Get(host)
	if err != nil {
		return nil, nil, err
	}
	c := s.clientTLS.Clone()
	c.GetCertificate = nil
	c.Certificates = []tls.Certificate{*cert}
	var rc *retrossl.Config
	if s.clientRetro != nil {
		r := *s.clientRetro
		r.GetCertificate = func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return cert, nil }
		rc = &r
	}
	return c, rc, nil
}

// ListenAndServe listens on the configured address and serves until Stop.
func (s *Server) ListenAndServe() error {
	addr := net.JoinHostPort("", strconv.Itoa(s.cfg.Port))
	if s.cfg.IP != nil {
		addr = net.JoinHostPort(s.cfg.IP.String(), strconv.Itoa(s.cfg.Port))
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	return s.Serve(ln)
}

// Serve serves connections from ln until Stop.
func (s *Server) Serve(ln net.Listener) error {
	s.ln = ln
	s.working.Store(true)
	proto := " (plain)"
	if s.cfg.SslEnable && s.CA != nil {
		proto = ", HTTPS"
	}
	logx.Server(true, "Supported protocols: HTTP%s, FTP via Web browser.", proto)
	for {
		c, err := ln.Accept()
		if err != nil {
			if !s.working.Load() {
				return nil
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				time.Sleep(50 * time.Millisecond)
				continue
			}
			return err
		}
		go s.serveConn(c)
	}
}

// Addr is the listening address.
func (s *Server) Addr() net.Addr {
	if s.ln == nil {
		return nil
	}
	return s.ln.Addr()
}

// Working: the server is serving.
func (s *Server) Working() bool { return s.working.Load() }

// Stop stops listening.
func (s *Server) Stop() {
	if s.working.Swap(false) && s.ln != nil {
		s.ln.Close()
		logx.Server(true, "HTTP/HTTPS/CERN Server stopped.")
	}
}

func (s *Server) serveConn(c net.Conn) {
	s.opened.Add(1)
	defer s.opened.Add(-1)
	defer c.Close()
	log := logx.New()
	defer func() {
		if r := recover(); r != nil {
			log.Printf("Oops: %v.", r)
		}
	}()
	s.handleConn(c, bufio.NewReaderSize(c, 16*1024), false, "", log)
}

// handleConn serves the requests of one connection (or of one TLS tunnel),
// one after another while the connection is kept alive.
func (s *Server) handleConn(c net.Conn, br *bufio.Reader, secure bool, prefix string, log *logx.Writer) {
	for {
		c.SetReadDeadline(time.Now().Add(5 * time.Minute))
		req, err := s.parseRequest(br, c, secure, log)
		if err != nil {
			if errors.Is(err, errNonHTTPInTLS) {
				log.Printf("SslOops: %v.", err)
			} else if !errors.Is(err, io.EOF) && !errors.Is(err, errDropped) && !isClosedErr(err) {
				if !s.cfg.HideClientErrors {
					log.Printf("Oops: %v.", err)
				}
			}
			return
		}
		c.SetReadDeadline(time.Time{})
		resp := newResponse(c, req.KeepAlive)
		resp.NoBody = req.Method == "HEAD"
		s.busy.Add(1)
		t := newTransit(s, req, resp, log)
		if secure {
			log.Printf(">[%s] %s %s (%s)", prefix, req.Method, req.RawURL, t.clientID())
		} else {
			log.Printf(">%s %s (%s)", req.Method, req.RawURL, t.clientID())
		}
		t.process()
		s.busy.Add(-1)
		if t.hijacked {
			return
		}
		resp.Close(Version)
		req.drainBody()
		if req.KeepAlive && resp.KeepAlive {
			log.Printf("<Done.")
			log = logx.New()
			prefix = req.Header.Get("Host")
			if prefix == "" {
				prefix = "Keep-Alive, no Host"
			}
			continue
		}
		log.Printf("<Done (connection close).")
		return
	}
}

func isClosedErr(err error) bool {
	if errors.Is(err, net.ErrClosed) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "connection reset") || strings.Contains(msg, "broken pipe")
}

// serverName is "host:port" (or "host" on port 80) as clients reach the
// proxy: DefaultHostName, or the address the request came in on when
// DefaultHostName is just the machine's name.
func (s *Server) serverName(localIP string) string {
	host, _ := os.Hostname()
	name := s.cfg.DefaultHostName
	if strings.EqualFold(name, host) && localIP != "" {
		name = localIP
	}
	if s.cfg.Port == 80 {
		return name
	}
	return name + ":" + strconv.Itoa(s.cfg.Port)
}

// infoString is the footer of WebOne's own pages.
func (s *Server) infoString() string {
	return "<hr>WebOne Proxy Server " + s.cfg.Vars.Value("WOVer") + "<br>on " + s.cfg.Vars.Value("WOSystem")
}

// isForceHttps: s contains one of the [ForceHttps] entries (or the hosts
// learned to be HTTPS-only).
func (s *Server) isForceHttps(str string) bool {
	s.forceMu.RLock()
	defer s.forceMu.RUnlock()
	for _, f := range s.forceHttps {
		if strings.Contains(str, f) {
			return true
		}
	}
	return false
}

func (s *Server) hostForcesHttps(host string) bool {
	s.forceMu.RLock()
	defer s.forceMu.RUnlock()
	for _, f := range s.forceHttps {
		if strings.HasPrefix(host, f) {
			return true
		}
	}
	return false
}

func (s *Server) addForceHttps(host string) {
	s.forceMu.Lock()
	defer s.forceMu.Unlock()
	for _, f := range s.forceHttps {
		if f == host {
			return
		}
	}
	s.forceHttps = append(s.forceHttps, host)
}

// processURIMasks expands variables in a value about a URL
// (Program.ProcessUriMasks): %URL%, %Url% (escaped), %UrlDomain%,
// %UrlNoDomain%, %UrlNoQuery%, %UrlNoPort%, %UrlHttps%, %UrlHttp%, then
// extra variables and the global ones.
func (s *Server) processURIMasks(masked, possibleURL string, dontTouch bool, extra *config.Vars) string {
	if possibleURL == "" {
		possibleURL = "http://webone.github.io:80/index.htm"
	}
	u, err := url.Parse(possibleURL)
	if err != nil || u.Host == "" {
		u, _ = url.Parse("http://webone.github.io/index.htm")
	}
	if s.isForceHttps(possibleURL) && !dontTouch {
		c := *u
		c.Scheme = "https"
		if c.Port() == "80" {
			c.Host = c.Hostname()
		}
		u = &c
	}
	full := absURL(u)
	host := u.Hostname()
	noDomain := pathAndQuery(u)
	vars := config.NewVars()
	vars.Set("URL", full)
	vars.Set("Url", url.QueryEscape(full))
	vars.Set("UrlDomain", host)
	vars.Set("UrlNoDomain", noDomain)
	vars.Set("UrlNoQuery", u.Scheme+"://"+host+u.EscapedPath())
	vars.Set("UrlNoPort", u.Scheme+"://"+host+noDomain)
	vars.Set("UrlHttps", "https://"+host+noDomain)
	vars.Set("UrlHttp", "http://"+host+noDomain)
	if extra != nil {
		for _, k := range extra.Keys() {
			vars.TryAdd(k, extra.Value(k))
		}
	}
	return s.cfg.Expand(masked, vars)
}

// userAgent is the User-Agent sent upstream ([Server] UserAgent with
// %Original% the client's).
func (s *Server) userAgent(client string) string {
	v := config.NewVars()
	if client == "" {
		client = "Mozilla/5.0 (Kundryuchy-Leshoz)"
	}
	v.Set("Original", client)
	return s.cfg.Expand(s.cfg.UserAgent, v)
}

// usedMemoryMB is the memory the process holds, for the status page.
func usedMemoryMB() int {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return int(m.Sys / 1024 / 1024)
}

// pac is the proxy auto-configuration script.
func (s *Server) pac(localHost string) string {
	v := config.NewVars()
	v.Set("PACProxy", localHost)
	return s.processURIMasks(s.cfg.PAC, localHost, false, v)
}

// String describes the server for the log.
func (s *Server) String() string {
	return fmt.Sprintf("WebOne @ %s:%d [%d/%d]", s.cfg.DefaultHostName, s.cfg.Port, s.opened.Load(), s.busy.Load())
}
