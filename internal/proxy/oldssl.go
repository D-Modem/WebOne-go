package proxy

import (
	"crypto/tls"
	"net"
	"slices"
	"time"

	"github.com/D-Modem/retrossl"
)

// makeClientRetro is retrossl's configuration, for the clients Go's TLS
// can't take: SSL 2.0 and 3.0, and TLS 1.0 after an SSL 2.0-format hello.
// Without SslProtocols, SSL 3.0 and TLS 1.0 are on and SSL 2.0 is off.
func (s *Server) makeClientRetro() *retrossl.Config {
	has := func(v uint16) bool {
		if !s.cfg.SslProtocolsSet {
			return v != retrossl.VersionSSL20
		}
		return slices.Contains(s.cfg.SslProtocols, v)
	}
	var on []uint16
	for _, v := range []uint16{retrossl.VersionSSL20, retrossl.VersionSSL30, retrossl.VersionTLS10} {
		if has(v) {
			on = append(on, v)
		}
	}
	if len(on) == 0 {
		return nil
	}
	// One session cache for every site: a session only resumes with the
	// certificate it was made with.
	rc := &retrossl.Config{MinVersion: on[0], MaxVersion: on[len(on)-1],
		SessionCache: retrossl.NewSessionCache(0, 0)}
	if len(s.cfg.SslCipherSuites) > 0 {
		rc.CipherSuites = []uint16{}
		for _, cs := range retrossl.CipherSuites() {
			if slices.Contains(s.cfg.SslCipherSuites, cs.ID) {
				rc.CipherSuites = append(rc.CipherSuites, cs.ID)
			}
		}
	}
	return rc
}

// serveSecure runs the handshake with a client that CONNECTed, by Go's TLS
// or, for old clients, retrossl.  It gives the secure connection and what
// was agreed, for the log.
func (s *Server) serveSecure(c net.Conn, tc *tls.Config, rc *retrossl.Config) (net.Conn, string, error) {
	c.SetDeadline(time.Now().Add(time.Minute))
	var sc net.Conn
	switch {
	case rc == nil:
		sc = tls.Server(c, tc)
	case s.noGoTLS:
		sc = retrossl.Server(c, rc)
	default:
		var err error
		if sc, err = retrossl.Frontend(c, tc, rc); err != nil {
			return nil, "", err
		}
	}
	var desc string
	switch sc := sc.(type) {
	case *tls.Conn:
		if err := sc.Handshake(); err != nil {
			return nil, "", err
		}
		st := sc.ConnectionState()
		desc = tls.VersionName(st.Version) + " with " + tls.CipherSuiteName(st.CipherSuite)
	case *retrossl.Conn:
		if err := sc.Handshake(); err != nil {
			return nil, "", err
		}
		st := sc.ConnectionState()
		desc = retrossl.VersionName(st.Version) + " with " + st.CipherName
	}
	c.SetDeadline(time.Time{})
	return sc, desc, nil
}
