package proxy

import (
	"bufio"
	"crypto/tls"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/D-Modem/webone-go/internal/logx"
	"github.com/D-Modem/webone-go/internal/rx"
)

// bufConn is a connection whose first bytes may already sit in a reader.
type bufConn struct {
	net.Conn
	r io.Reader
}

func (c *bufConn) Read(p []byte) (int, error) { return c.r.Read(p) }

// connect handles CONNECT host:port (the HTTPS proxy part of
// ProcessTransit, with HttpSecureServer, HttpSecurePassthroughServer,
// HttpSecureNonHttpServer and HttpSecureNonHttpDecryptServer).
func (t *transit) connect() {
	raw := t.req.RawURL
	if !strings.Contains(raw, ":") {
		t.log.Printf(" Invalid CONNECT target: %s", raw)
		t.sendError(400, "Invalid request. Correct format is <pre>CONNECT example.com:443 HTTP/1.1</pre>")
		return
	}
	switch {
	case rx.AnyMatches(raw, t.cfg.ConnectPassThrough):
		t.connectPassThrough()
	case t.cfg.NonHttpSslDecrypt[raw] != "":
		t.connectDecrypt(t.cfg.NonHttpSslDecrypt[raw])
	case strings.HasSuffix(raw, ":443"):
		t.connectSecure()
	default:
		t.connectNonHttp(containsAny(strings.ToLower(raw), t.cfg.NonHttpSslServers))
	}
}

// established answers "200 Connection established" and gives the
// connection over to the tunnel.
func (t *transit) established(via bool) net.Conn {
	w := t.resp
	w.SetVersion(1, 1)
	w.Status = 200
	w.StatusMessage = "Connection established"
	if via {
		w.Header.Add("Via", "1.1 WebOne/"+t.cfg.Vars.Value("WOVer"))
	}
	w.Header.Set("Connection", "Keep-Alive")
	w.KeepAlive = true
	w.SendHeaders(Version)
	t.hijacked = true
	return &bufConn{Conn: t.req.conn, r: t.req.br}
}

// onlyHTTPS refuses a tunnel to a port other than 443.
func (t *transit) onlyHTTPS() {
	const msg = "This proxy is performing only HTTP and HTTPS tunneling."
	t.resp.SetVersion(1, 1)
	t.resp.KeepAlive = false
	t.resp.Header.Set("Connection", "Close")
	t.sendBytes(502, "text/plain", []byte(msg))
	t.log.Printf("<Not a HTTPS CONNECT, goodbye.")
}

// sslDisabled answers a secure CONNECT when SslEnable is off.
func (t *transit) sslDisabled() {
	t.log.Printf("<Secure CONNECT is disabled.")
	html := "<HTML><HEAD><TITLE>WebOne: SSL is not supported</TITLE></HEAD>" +
		"<BODY><P>Incoming HTTPS connections support is disabled on this server.</P>" + t.s.infoString() + "</BODY></HTML>"
	if b, err := os.ReadFile(filepath.Join(t.cfg.ContentDirectory, "Err-SslDisabled.htm")); err == nil {
		html = string(b)
	}
	t.resp.SetVersion(1, 1)
	t.sendBytes(501, "text/html", []byte(html))
}

func (t *transit) sslAvailable() bool { return t.cfg.SslEnable && t.s.CA != nil }

// connectSecure is the HTTPS proxy: the proxy answers TLS with a
// certificate for the host and handles the requests inside the tunnel as
// any other (HttpSecureServer).
func (t *transit) connectSecure() {
	if !t.sslAvailable() {
		t.sslDisabled()
		return
	}
	host := t.req.RawURL[:strings.LastIndexByte(t.req.RawURL, ':')]
	cfg, err := t.s.tlsFor(host)
	if err != nil {
		t.log.Printf(" Cannot made SSL connection: %v", err)
		t.sendError(501, "Sorry, an error occured on creating client SSL tunnel: "+err.Error())
		return
	}
	c := t.established(true)
	tc := tls.Server(c, cfg)
	tc.SetDeadline(time.Now().Add(time.Minute))
	if err := tc.Handshake(); err != nil {
		t.log.Printf("!SSL Handshake failed: %s", handshakeError(err))
		c.Close()
		return
	}
	tc.SetDeadline(time.Time{})
	st := tc.ConnectionState()
	log := logx.New()
	log.Printf(">SSL %s: %s with %s", host, tls.VersionName(st.Version), tls.CipherSuiteName(st.CipherSuite))
	t.s.handleConn(tc, bufio.NewReaderSize(tc, 16*1024), true, host, log)
	tc.Close()
	log.Printf("<Close SSL.")
}

// handshakeError makes Go's TLS errors say what old clients do wrong.
func handshakeError(err error) string {
	msg := err.Error()
	if strings.Contains(msg, "unsupported SSLv2 handshake") || strings.Contains(msg, "oversized record") {
		return msg + " (the client speaks SSL 2.0 or 3.0, which this proxy cannot)"
	}
	return msg
}

// connectPassThrough connects the client straight to the server
// (HttpSecurePassthroughServer).
func (t *transit) connectPassThrough() {
	raw := t.req.RawURL
	if !strings.HasSuffix(raw, ":443") && !t.cfg.AllowNonHttpsCONNECT {
		t.onlyHTTPS()
		return
	}
	c := t.established(false)
	t.log.Printf(">Passthrough: %s", raw)
	remote, err := t.s.up.dialer.Dial("tcp", raw)
	if err != nil {
		t.log.Printf(" PT Connection failed: %v.", err)
		io.WriteString(c, "The proxy server is unable to connect pass-though: "+err.Error()+"\r\n")
		c.Close()
		return
	}
	t.log.Printf(" PT tunnel established.")
	pipe(c, remote)
	t.log.Printf(" PT connection to %s closed.", raw)
}

// connectNonHttp is a tunnel to some other protocol, decrypting the
// client's TLS and re-encrypting to the server when useTLS
// (HttpSecureNonHttpServer).
func (t *transit) connectNonHttp(useTLS bool) {
	if !t.cfg.AllowNonHttpsCONNECT {
		t.onlyHTTPS()
		return
	}
	target := t.req.RawURL
	parts := strings.Split(target, ":")
	if len(parts) != 2 {
		t.sendError(400, "Invalid `domain:port` pair supplied for CONNECT method.")
		return
	}
	if _, err := strconv.Atoi(parts[1]); err != nil {
		t.sendError(400, "Invalid port number supplied for CONNECT method.")
		return
	}
	var cfg *tls.Config
	if useTLS && t.sslAvailable() {
		var err error
		if cfg, err = t.s.tlsFor(parts[0]); err != nil {
			t.log.Printf(" Cannot made SSL connection: %v", err)
			t.sendError(501, "Sorry, an error occured on creating client SSL tunnel: "+err.Error())
			return
		}
	}
	c := t.established(false)
	t.log.Printf(">Non-HTTP: %s", target)
	defer c.Close()
	if containsAny(target, t.cfg.UrlBlackList) {
		t.log.Printf(" Blacklisted target, goodbye.")
		return
	}
	if len(t.cfg.UrlWhiteList) > 0 && !containsAny(target, t.cfg.UrlWhiteList) {
		t.log.Printf(" Non-allowed target, goodbye.")
		return
	}
	for src, dst := range t.cfg.NonHttpConnectRedirect {
		if strings.Contains(t.req.RawURL, src) {
			target = dst
			t.log.Printf(" Redirect connection to %s.", dst)
		}
	}
	if useTLS {
		if cfg == nil {
			t.log.Printf("<SSL is disabled, goodbye.")
			return
		}
		tc := tls.Server(c, cfg)
		if err := tc.Handshake(); err != nil {
			t.log.Printf("!SSL client handshake failed: %s", handshakeError(err))
			return
		}
		c = tc
	}
	remote, err := t.s.up.dialer.Dial("tcp", target)
	if err == nil && useTLS {
		rc := tls.Client(remote, t.upstreamTLS(parts[0]))
		if err = rc.Handshake(); err != nil {
			remote.Close()
		}
		remote = rc
	}
	if err != nil {
		t.log.Printf(" Connection failed: %v.", err)
		io.WriteString(c, "The proxy server is unable to connect: "+err.Error()+"\r\n")
		return
	}
	if useTLS {
		t.log.Printf(" Secure tunnel established.")
	} else {
		t.log.Printf(" Tunnel established.")
	}
	pipe(c, remote)
	t.log.Printf(" Connection to %s closed.", t.req.RawURL)
}

// connectDecrypt is a plain tunnel from the client that the proxy
// encrypts toward the server (HttpSecureNonHttpDecryptServer).
func (t *transit) connectDecrypt(server string) {
	if !t.cfg.AllowNonHttpsCONNECT {
		t.onlyHTTPS()
		return
	}
	host := server
	if i := strings.LastIndexByte(server, ':'); i >= 0 {
		host = server[:i]
	}
	c := t.established(false)
	defer c.Close()
	t.log.Printf(">Decrypt: %s", server)
	remote, err := t.s.up.dialer.Dial("tcp", server)
	if err == nil {
		t.log.Printf(" D tunnel connected.")
		rc := tls.Client(remote, t.upstreamTLS(host))
		if err = rc.Handshake(); err != nil {
			remote.Close()
		}
		remote = rc
	}
	if err != nil {
		t.log.Printf(" D connection failed: %v.", err)
		io.WriteString(c, "The proxy server is unable to connect with decryption: "+err.Error()+"\r\n")
		return
	}
	t.log.Printf(" D tunnel established.")
	pipe(c, remote)
	t.log.Printf(" D connection to %s closed.", t.req.RawURL)
}

// upstreamTLS is the TLS client configuration toward a server.
func (t *transit) upstreamTLS(host string) *tls.Config {
	c := t.s.up.tlsCfg.Clone()
	c.ServerName = host
	return c
}

// pipe copies both ways until one side ends, then closes both.
func pipe(a, b net.Conn) {
	var once sync.Once
	done := make(chan struct{})
	closeBoth := func() {
		once.Do(func() {
			a.Close()
			b.Close()
			close(done)
		})
	}
	go func() { io.Copy(b, a); closeBoth() }()
	go func() { io.Copy(a, b); closeBoth() }()
	<-done
}
