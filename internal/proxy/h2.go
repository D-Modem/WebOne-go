package proxy

import (
	"crypto/tls"
	"net"
	"net/http"
	"net/http/httputil"
	"slices"
	"strings"
	"time"

	"github.com/D-Modem/webone-go/internal/logx"
	"golang.org/x/net/http2"
)

// HTTP/2 inside CONNECT tunnels, for gRPC.
//
// Old browsers and today's ones offer HTTP/1.1 (or nothing) in ALPN, and
// they go through the proxy's usual handling, edits and all. gRPC needs
// HTTP/2 end to end, with trailers, and gRPC clients offer only "h2". Those
// clients get HTTP/2 from the proxy, and their requests go on to the server
// over HTTP/2 untouched, streamed both ways.

// h2Only: a client's ALPN list asks for HTTP/2 and not HTTP/1.1.
func h2Only(protos []string) bool {
	return slices.Contains(protos, "h2") && !slices.Contains(protos, "http/1.1")
}

// withH2 lets clients that speak only HTTP/2 have it on c.
func withH2(c *tls.Config) *tls.Config {
	c.GetConfigForClient = func(h *tls.ClientHelloInfo) (*tls.Config, error) {
		if !h2Only(h.SupportedProtos) {
			return nil, nil
		}
		cc := c.Clone()
		cc.GetConfigForClient = nil
		cc.NextProtos = []string{"h2"}
		return cc, nil
	}
	return c
}

// negotiated is the ALPN protocol agreed on c, if c is Go's TLS.
func negotiated(c net.Conn) string {
	if tc, ok := c.(*tls.Conn); ok {
		return tc.ConnectionState().NegotiatedProtocol
	}
	return ""
}

// serveH2 serves HTTP/2 on a client's tunnel to target ("host:443"),
// passing every request on to the server.
func (s *Server) serveH2(c net.Conn, target string) {
	srv := &http2.Server{IdleTimeout: 5 * time.Minute, PermitProhibitedCipherSuites: true}
	srv.ServeConn(c, &http2.ServeConnOpts{Handler: s.h2Handler(target)})
}

// h2Handler passes HTTP/2 requests to target over HTTP/2, as a reverse
// proxy that keeps trailers and flushes at once (what gRPC needs).
func (s *Server) h2Handler(target string) http.Handler {
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme = "https"
			pr.Out.URL.Host = target
			pr.Out.Host = pr.In.Host
		},
		Transport:     s.up.h2,
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			logx.Std.Printf(" HTTP/2 to %s failed: %v.", target, err)
			if isGRPC(r) {
				// gRPC clients read the status from headers, not the body
				w.Header().Set("Content-Type", "application/grpc")
				w.Header().Set("Grpc-Status", "14") // UNAVAILABLE
				w.Header().Set("Grpc-Message", "WebOne: "+err.Error())
				w.WriteHeader(http.StatusOK)
				return
			}
			http.Error(w, "WebOne: "+err.Error(), http.StatusBadGateway)
		},
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if host == "" {
			host = target
		}
		u := "https://" + host + r.URL.RequestURI()
		log := logx.New()
		kind := "HTTP/2"
		if isGRPC(r) {
			kind = "gRPC"
		}
		log.Printf(">[%s] %s %s (%s %s)", target, r.Method, u, kind, r.RemoteAddr)
		if containsAny(u, s.cfg.UrlBlackList) ||
			(len(s.cfg.UrlWhiteList) > 0 && !containsAny(u, s.cfg.UrlWhiteList)) {
			log.Printf("<Blocked by URL list.")
			http.Error(w, "WebOne: this URL is not allowed.", http.StatusForbidden)
			return
		}
		rp.ServeHTTP(w, r)
		log.Printf("<Done.")
	})
}

func isGRPC(r *http.Request) bool {
	return strings.HasPrefix(r.Header.Get("Content-Type"), "application/grpc")
}
