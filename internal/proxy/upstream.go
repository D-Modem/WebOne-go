package proxy

import (
	"compress/flate"
	"compress/gzip"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/D-Modem/webone-go/internal/config"
	"github.com/D-Modem/webone-go/internal/logx"
)

// upstream is how the proxy reaches servers (Program.HTTPClient): no
// redirects followed, no cookies kept, decompression of what it asked for.
type upstream struct {
	cfg    *config.Config
	h1     *http.Client // HTTP/1.1 only
	auto   *http.Client // HTTP/2 when the server offers it
	dialer *net.Dialer
	tlsCfg *tls.Config
	proxy  func(*http.Request) (*url.URL, error)
	h3     *h3up           // HTTP/3; nil when it can't be used
	h2     *http.Transport // HTTP/2 only, for gRPC and other HTTP/2-only clients
}

func newUpstream(cfg *config.Config, extraRoots []*x509.Certificate) (*upstream, error) {
	u := &upstream{cfg: cfg}
	timeout := time.Duration(cfg.ConnectionTimeout) * time.Second
	if timeout <= 0 {
		timeout = 100 * time.Second
	}
	u.dialer = &net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second}
	if cfg.DnsServer != "" {
		dns := cfg.DnsServer
		u.dialer.Resolver = &net.Resolver{
			PreferGo: true,
			Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
				d := net.Dialer{Timeout: 5 * time.Second}
				return d.DialContext(ctx, network, dns)
			},
		}
	}
	u.tlsCfg = &tls.Config{MinVersion: tls.VersionTLS10}
	if len(cfg.SecurityProtocols) > 0 {
		lo, hi := uint16(0xffff), uint16(0)
		for _, v := range cfg.SecurityProtocols {
			lo, hi = min(lo, v), max(hi, v)
		}
		u.tlsCfg.MinVersion, u.tlsCfg.MaxVersion = lo, hi
	}
	if len(extraRoots) > 0 {
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		for _, c := range extraRoots {
			pool.AddCert(c)
		}
		u.tlsCfg.RootCAs = pool
	}
	if !cfg.ValidateCertificates {
		u.tlsCfg.InsecureSkipVerify = true
	}

	var proxy func(*http.Request) (*url.URL, error)
	explicitProxy := false
	switch strings.ToLower(strings.TrimSpace(cfg.UpperProxy)) {
	case "":
		proxy = http.ProxyFromEnvironment
	case "no", "off", "disable", "false", "direct":
		proxy = nil
	default:
		pu, err := url.Parse(cfg.UpperProxy)
		if err != nil || pu.Host == "" {
			pu, err = url.Parse("http://" + cfg.UpperProxy)
		}
		if err != nil {
			return nil, fmt.Errorf("bad UpperProxy %q: %v", cfg.UpperProxy, err)
		}
		proxy = http.ProxyURL(pu)
		explicitProxy = true
	}

	mk := func(h2 bool) *http.Client {
		tr := &http.Transport{
			Proxy:                 proxy,
			DialContext:           u.dialer.DialContext,
			TLSClientConfig:       u.tlsCfg.Clone(),
			TLSHandshakeTimeout:   timeout,
			DisableCompression:    true, // decompressed here, as asked
			MaxIdleConnsPerHost:   8,
			IdleConnTimeout:       90 * time.Second,
			ExpectContinueTimeout: time.Second,
			ForceAttemptHTTP2:     h2,
		}
		if !h2 {
			tr.TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{}
		}
		return &http.Client{
			Transport: tr,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
	}
	u.h1 = mk(false)
	u.auto = mk(true)
	u.proxy = proxy
	u.h2 = mk(true).Transport.(*http.Transport)
	u.h2.Protocols = new(http.Protocols)
	u.h2.Protocols.SetHTTP2(true)
	u.h2.MaxIdleConnsPerHost = 2
	u.h3 = newH3(u.tlsCfg, u.dialer.Resolver, timeout, explicitProxy)
	return u, nil
}

// operation is one request to a server and its response (HttpOperation.cs).
type operation struct {
	Method      string
	URL         *url.URL
	Header      *Header
	HTTPVersion string // RemoteHttpVersion rule: "auto", "=1.1", ">2.0" ...
	Secure      bool   // the client came in over TLS: fetch over HTTPS
	Body        io.Reader
	log         logger

	Resp       *http.Response
	RespHeader *Header
	RespBody   io.ReadCloser
}

// hopHeader: not sent on to the server.
func hopHeader(name string) bool {
	n := strings.ToLower(name)
	return strings.HasPrefix(n, "proxy-") || strings.HasSuffix(n, "-encoding") || n == "host" ||
		n == "connection" || n == "keep-alive" || n == "upgrade" || n == "te" || n == "trailer"
}

// send makes the request.
func (s *Server) send(op *operation) error {
	if op.HTTPVersion == "" {
		op.HTTPVersion = s.cfg.RemoteHttpVersion
	}
	target := *op.URL
	if op.Secure || s.hostForcesHttps(target.Hostname()) {
		target.Scheme = "https"
		if target.Port() == "80" {
			target.Host = target.Hostname()
		}
		op.URL = &target
	}
	var body io.Reader
	if op.Body != nil {
		body = op.Body
	}
	req, err := http.NewRequest(op.Method, target.String(), body)
	if err != nil {
		return err
	}
	if target.RawQuery == "" && strings.HasSuffix(op.URL.String(), "?") {
		req.URL.ForceQuery = true
	}
	op.Header.Each(func(k, v string) {
		if strings.HasSuffix(strings.ToLower(k), "-encoding") && strings.Contains(v, "xpress") && s.cfg.AllowHttpCompression {
			op.log.Printf(" Warning: This connection to server may use unsupported 'xpress' compression algorithm. Consider set AllowHttpCompression=false to get out some possible errors.")
		}
		if hopHeader(k) {
			return
		}
		if strings.EqualFold(k, "Content-Length") {
			return
		}
		req.Header.Add(k, v)
	})
	if cl := op.Header.Get("Content-Length"); cl != "" && body != nil {
		var n int64
		fmt.Sscan(cl, &n)
		req.ContentLength = n
	} else if body == nil {
		req.ContentLength = 0
	}
	if s.cfg.AllowHttpCompression {
		req.Header.Set("Accept-Encoding", "gzip, deflate")
	}
	if req.Header.Get("User-Agent") == "" {
		req.Header["User-Agent"] = []string{""} // Go would send its own
	}

	resp, err := s.up.do(req, op.HTTPVersion, op.log)
	if err != nil {
		return err
	}
	op.Resp = resp
	op.RespBody = resp.Body
	switch strings.ToLower(resp.Header.Get("Content-Encoding")) {
	case "gzip", "x-gzip":
		zr, err := gzip.NewReader(resp.Body)
		if err == nil {
			op.RespBody = struct {
				io.Reader
				io.Closer
			}{zr, resp.Body}
			resp.Header.Del("Content-Encoding")
			resp.Header.Del("Content-Length")
			resp.ContentLength = -1
		} else if !errors.Is(err, io.EOF) {
			resp.Body.Close()
			return &decodeError{err}
		}
	case "deflate":
		op.RespBody = struct {
			io.Reader
			io.Closer
		}{flate.NewReader(resp.Body), resp.Body}
		resp.Header.Del("Content-Encoding")
		resp.Header.Del("Content-Length")
		resp.ContentLength = -1
	}
	op.RespHeader = FromHTTP(resp.Header)
	for k, vs := range resp.Trailer {
		for _, v := range vs {
			op.RespHeader.Add(k, v)
		}
	}
	return nil
}

// do sends req over the HTTP versions rule allows: HTTP/3 when the
// server has said it has it (or when the rule allows nothing else), HTTP/2
// or HTTP/1.1 otherwise.
func (u *upstream) do(req *http.Request, rule string, log logger) (*http.Response, error) {
	h1, h2, h3 := httpVersions(rule)
	hp := hostPort(req.URL.Scheme, req.URL.Host)
	if h3 && u.useH3(req) && ((!h1 && !h2) || u.h3.known(hp)) {
		resp, err := u.h3.client.Do(req)
		if err == nil || (!h1 && !h2) {
			return resp, err
		}
		u.h3.failed(hp)
		log.Printf(" HTTP/3 to %s failed (%v), using TCP.", hp, err)
		if req.Body != nil && req.Body != http.NoBody {
			if req.GetBody == nil {
				return nil, err
			}
			if req.Body, err = req.GetBody(); err != nil {
				return nil, err
			}
		}
	}
	client := u.auto
	if h1 && !h2 {
		client = u.h1
	}
	resp, err := client.Do(req)
	if err == nil && h3 && u.h3 != nil {
		u.h3.learn(hp, resp.Header.Values("Alt-Svc"))
	}
	return resp, err
}

// useH3: HTTP/3 can carry req (HTTPS, and no upper proxy for it).
func (u *upstream) useH3(req *http.Request) bool {
	if u.h3 == nil || req.URL.Scheme != "https" {
		return false
	}
	if u.proxy != nil {
		if p, err := u.proxy(req); err != nil || p != nil {
			return false
		}
	}
	return true
}

// decodeError: the server's compressed content cannot be read
// (InvalidDataException).
type decodeError struct{ err error }

func (e *decodeError) Error() string { return "cannot decode HTTP data: " + e.err.Error() }

// fetch is a plain GET (for the Web Archive and converters).
func (s *Server) fetch(rawURL string) (*http.Response, error) {
	req, err := http.NewRequest("GET", rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "WebOne/"+s.cfg.Vars.Value("WOVer"))
	resp, err := s.up.do(req, s.cfg.RemoteHttpVersion, logx.Std)
	if err != nil {
		logx.Std.Printf(" Fetch %s failed: %v", rawURL, err)
	}
	return resp, err
}
