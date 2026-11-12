package proxy

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/D-Modem/webone-go/internal/config"
	"github.com/D-Modem/webone-go/internal/logx"
	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
)

// h3Broken is how long a server whose HTTP/3 failed is reached over TCP
// only.
const h3Broken = 10 * time.Minute

// h3up fetches from servers over HTTP/3 (QUIC). It learns which servers
// have HTTP/3 from their Alt-Svc headers, as browsers do.
type h3up struct {
	client   *http.Client
	tr       *http3.Transport
	resolver *net.Resolver

	sockOnce sync.Once
	sock     *quic.Transport // one UDP socket for every server
	sockErr  error

	mu     sync.Mutex
	alt    map[string]altSvc    // "host:port" → where its HTTP/3 is
	broken map[string]time.Time // "host:port" → TCP only until then
}

type altSvc struct {
	addr  string // "host:port" or ":port" (the same host)
	until time.Time
}

// newH3 makes the HTTP/3 client, or gives nil when HTTP/3 can't be used:
// through an upper proxy (QUIC can't go through one), or when
// SecurityProtocols leaves out TLS 1.3.
func newH3(base *tls.Config, resolver *net.Resolver, timeout time.Duration, viaProxy bool) *h3up {
	if viaProxy || (base.MaxVersion != 0 && base.MaxVersion < tls.VersionTLS13) {
		return nil
	}
	h := &h3up{
		resolver: resolver,
		alt:      map[string]altSvc{},
		broken:   map[string]time.Time{},
	}
	if h.resolver == nil {
		h.resolver = net.DefaultResolver
	}
	tc := base.Clone()
	tc.MinVersion, tc.MaxVersion = tls.VersionTLS13, 0
	h.tr = &http3.Transport{
		TLSClientConfig:    tc,
		QUICConfig:         &quic.Config{HandshakeIdleTimeout: min(timeout, 10*time.Second), MaxIdleTimeout: 90 * time.Second},
		DisableCompression: true, // decompressed in send, as asked
		Dial:               h.dial,
	}
	h.client = &http.Client{
		Transport: h.tr,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	return h
}

// dial opens a QUIC connection to addr, or to where its Alt-Svc said.
func (h *h3up) dial(ctx context.Context, addr string, tc *tls.Config, qc *quic.Config) (*quic.Conn, error) {
	h.sockOnce.Do(func() {
		var c *net.UDPConn
		if c, h.sockErr = net.ListenUDP("udp", nil); h.sockErr == nil {
			h.sock = &quic.Transport{Conn: c}
		}
	})
	if h.sockErr != nil {
		return nil, h.sockErr
	}
	target := addr
	if a, ok := h.altFor(addr); ok {
		target = a
	}
	host, port, err := net.SplitHostPort(target)
	if err != nil {
		return nil, err
	}
	pn, err := strconv.Atoi(port)
	if err != nil {
		return nil, err
	}
	ips, err := h.resolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	var last error
	for _, ip := range ips {
		ua := net.UDPAddrFromAddrPort(netip.AddrPortFrom(ip.Unmap(), uint16(pn)))
		qc, err := h.sock.DialEarly(ctx, ua, tc, qc)
		if err == nil {
			logx.Std.Printf(" HTTP/3 (QUIC) connection to %s.", target)
			return qc, nil
		}
		last = err
	}
	if last == nil {
		last = errors.New("no address for " + host)
	}
	return nil, last
}

// altFor is where host:port's HTTP/3 is, if it said so lately.
func (h *h3up) altFor(hostport string) (string, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	a, ok := h.alt[hostport]
	if !ok || time.Now().After(a.until) {
		delete(h.alt, hostport)
		return "", false
	}
	if strings.HasPrefix(a.addr, ":") {
		host, _, _ := net.SplitHostPort(hostport)
		return net.JoinHostPort(host, a.addr[1:]), true
	}
	return a.addr, true
}

// known: hostport has said it has HTTP/3, and it hasn't failed lately.
func (h *h3up) known(hostport string) bool {
	h.mu.Lock()
	until, bad := h.broken[hostport]
	if bad && time.Now().After(until) {
		delete(h.broken, hostport)
		bad = false
	}
	h.mu.Unlock()
	if bad {
		return false
	}
	_, ok := h.altFor(hostport)
	return ok
}

// learn reads a response's Alt-Svc headers for hostport.
func (h *h3up) learn(hostport string, values []string) {
	if len(values) == 0 {
		return
	}
	a, clear, ok := parseAltSvcH3(values, time.Now())
	h.mu.Lock()
	defer h.mu.Unlock()
	switch {
	case clear:
		delete(h.alt, hostport)
	case ok:
		h.alt[hostport] = a
	}
}

// failed marks hostport's HTTP/3 as not working for a while.
func (h *h3up) failed(hostport string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.alt, hostport)
	h.broken[hostport] = time.Now().Add(h3Broken)
}

// parseAltSvcH3 finds the "h3" entry in Alt-Svc headers (RFC 7838), such
// as `h3=":443"; ma=86400, h3-29=":443"`. clear is Alt-Svc: clear.
func parseAltSvcH3(values []string, now time.Time) (a altSvc, clear, ok bool) {
	for _, v := range values {
		for entry := range strings.SplitSeq(v, ",") {
			entry = strings.TrimSpace(entry)
			if strings.EqualFold(entry, "clear") {
				return altSvc{}, true, false
			}
			params := strings.Split(entry, ";")
			proto, auth, found := strings.Cut(strings.TrimSpace(params[0]), "=")
			if !found || proto != "h3" {
				continue
			}
			auth = strings.Trim(strings.TrimSpace(auth), `"`)
			if _, _, err := net.SplitHostPort(auth); err != nil {
				continue
			}
			maxAge := 24 * time.Hour
			for _, p := range params[1:] {
				k, val, _ := strings.Cut(strings.TrimSpace(p), "=")
				if strings.EqualFold(k, "ma") {
					if n, err := strconv.Atoi(strings.Trim(val, `"`)); err == nil {
						maxAge = time.Duration(n) * time.Second
					}
				}
			}
			if maxAge <= 0 {
				continue
			}
			return altSvc{addr: auth, until: now.Add(maxAge)}, false, true
		}
	}
	return altSvc{}, false, false
}

// httpVersions reads a RemoteHttpVersion rule ("auto", "=1.1", ">2.0",
// "<3.0" ...) as the HTTP versions it lets the proxy use toward servers.
func httpVersions(rule string) (h1, h2, h3 bool) {
	if rule == "" || rule[0] == 'a' || len(rule) < 2 {
		return true, true, true
	}
	maj, minor, ok := config.ParseVersion(rule[1:])
	if !ok {
		return true, true, true
	}
	want := maj*10 + minor
	in := func(v int) bool {
		switch rule[0] {
		case '>':
			return v > want
		case '<':
			return v < want
		default:
			return v == want
		}
	}
	h1, h2, h3 = in(10) || in(11), in(20), in(30)
	if !h1 && !h2 && !h3 {
		return true, false, false
	}
	return h1, h2, h3
}

// hostPort is u's "host:port", with the scheme's default port.
func hostPort(scheme, host string) string {
	if _, _, err := net.SplitHostPort(host); err == nil {
		return host
	}
	if scheme == "https" {
		return net.JoinHostPort(host, "443")
	}
	return net.JoinHostPort(host, "80")
}
