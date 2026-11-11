package proxy

import (
	"errors"
	"os"
	"strconv"
	"strings"

	"github.com/D-Modem/webone-go/internal/config"
)

// Kind is how a request names its target (HttpUtil.RequestKind).
//
//	GET /123, Host: example.com          -> http://example.com/123   StandardRemote
//	GET /123, Host: (the proxy)          -> the proxy's own page     StandardHttp
//	GET /http://example.com/123          -> http://example.com/123   AlternateProxy
//	GET http://example.com/123           -> http://example.com/123   StandardProxy
//	GET ftp://example.com/pub            -> ftp://example.com/pub    StandardProxy
//	CONNECT example.com:443              -> (a tunnel)               StandardSslProxy
type Kind int

const (
	StandardRemote Kind = iota
	StandardHttp
	AlternateProxy
	DirtyAlternateProxy
	StandardProxy
	StandardSslProxy
)

func (k Kind) String() string {
	return [...]string{"StandardRemote", "StandardHttp", "AlternateProxy", "DirtyAlternateProxy", "StandardProxy", "StandardSslProxy"}[k]
}

// kindOf works out a request's Kind (HttpUtil.GetKindOfRequest).
func (s *Server) kindOf(rawURL, hostHeader string, isConnect bool) (Kind, error) {
	host := hostHeader
	if host == "" {
		host = "127.0.0.1"
	}
	port := 80
	if !strings.HasPrefix(host, "/") && strings.Contains(host, ":") && !strings.HasSuffix(host, "]") {
		i := strings.LastIndexByte(host, ':')
		if p, err := strconv.Atoi(host[i+1:]); err == nil {
			port = p
		}
		host = host[:i]
	}
	lower := strings.ToLower(rawURL)
	if strings.HasPrefix(rawURL, "/") {
		if s.isLocalhost(host, port) {
			if strings.HasPrefix(lower, "/http:") || strings.HasPrefix(lower, "/https:") || strings.HasPrefix(lower, "/ftp:") {
				return AlternateProxy, nil
			}
			// (WebOne's DirtyAlternateProxy test never succeeds; it is not ported.)
			return StandardHttp, nil
		}
		return StandardRemote, nil
	}
	self := "http://" + host + ":" + strconv.Itoa(port) + "/"
	if strings.HasPrefix(rawURL, self) && strings.Contains(rawURL[len(self):], ":") {
		return AlternateProxy, nil
	}
	if s.isLocalhost(host, port) {
		return StandardHttp, nil
	}
	if strings.Contains(rawURL, "://") {
		return StandardProxy, nil
	}
	if isConnect {
		return StandardSslProxy, nil
	}
	return 0, errors.New("cannot guess kind of requested target")
}

// isLocalhost: host:port is this proxy.
func (s *Server) isLocalhost(host string, port int) bool {
	if port != s.cfg.Port {
		return false
	}
	h := strings.ToLower(host)
	for _, n := range s.localHostNames() {
		if strings.Contains(h, strings.ToLower(n)) {
			return true
		}
	}
	return false
}

// localHostNames is the names and addresses the proxy answers to.
func (s *Server) localHostNames() []string {
	var names []string
	for _, ip := range config.LocalIPs() {
		if ip.To4() == nil {
			names = append(names, "["+ip.String()+"]")
		} else {
			names = append(names, ip.String())
		}
	}
	host, _ := os.Hostname()
	names = append(names, "wpad", "localhost", host, s.cfg.DefaultHostName)
	names = append(names, s.cfg.HostNames...)
	var out []string
	for _, n := range names {
		if n != "" {
			out = append(out, n)
		}
	}
	return out
}
