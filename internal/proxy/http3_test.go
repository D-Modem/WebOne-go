package proxy

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/D-Modem/webone-go/internal/logx"
	"github.com/quic-go/quic-go/http3"
)

func TestHTTPVersions(t *testing.T) {
	for _, c := range []struct {
		rule       string
		h1, h2, h3 bool
	}{
		{"auto", true, true, true},
		{"=1.1", true, false, false},
		{"=1.0", true, false, false},
		{"=2.0", false, true, false},
		{"=3.0", false, false, true},
		{">1.1", false, true, true},
		{">2.0", false, false, true},
		{"<3.0", true, true, false},
		{"<2.0", true, false, false},
		{"<1.0", true, false, false}, // nothing: HTTP/1.1
	} {
		h1, h2, h3 := httpVersions(c.rule)
		if h1 != c.h1 || h2 != c.h2 || h3 != c.h3 {
			t.Errorf("%s: %v %v %v", c.rule, h1, h2, h3)
		}
	}
}

func TestParseAltSvc(t *testing.T) {
	now := time.Now()
	a, clear, ok := parseAltSvcH3([]string{`h3-29=":443"; ma=10, h3=":8443"; ma=60`}, now)
	if !ok || clear || a.addr != ":8443" || !a.until.Equal(now.Add(time.Minute)) {
		t.Errorf("got %+v %v %v", a, clear, ok)
	}
	if _, _, ok := parseAltSvcH3([]string{`h2=":443"`}, now); ok {
		t.Error("h2 taken as h3")
	}
	if _, clear, _ := parseAltSvcH3([]string{"clear"}, now); !clear {
		t.Error("clear not seen")
	}
}

// TestHTTP3Upstream: a server that says it has HTTP/3 in Alt-Svc is
// reached over HTTP/3 from then on, and "=3.0" uses it at once.
func TestHTTP3Upstream(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Alt-Svc", `h3=":`+r.URL.Query().Get("port")+`"; ma=60`)
		io.WriteString(w, r.Proto)
	})
	tcp := httptest.NewUnstartedServer(handler)
	tcp.EnableHTTP2 = true
	tcp.StartTLS()
	defer tcp.Close()
	port := tcp.Listener.Addr().(*net.TCPAddr).Port
	udp, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port})
	if err != nil {
		t.Skipf("UDP port %d: %v", port, err)
	}
	h3srv := &http3.Server{Handler: handler, TLSConfig: http3.ConfigureTLSConfig(tcp.TLS.Clone())}
	go h3srv.Serve(udp)
	defer h3srv.Close()

	s, _ := startServer(t, "ValidateCertificates=no\n")
	if s.up.h3 == nil {
		t.Fatal("no HTTP/3 client")
	}
	url := "https://127.0.0.1:" + strconv.Itoa(port) + "/?port=" + strconv.Itoa(port)
	get := func(rule string) string {
		t.Helper()
		req, _ := http.NewRequest("GET", url, nil)
		resp, err := s.up.do(req, rule, logx.Std)
		if err != nil {
			t.Fatalf("%s: %v", rule, err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return string(b)
	}
	if got := get("auto"); got != "HTTP/2.0" {
		t.Errorf("first request: %s", got)
	}
	if got := get("auto"); got != "HTTP/3.0" {
		t.Errorf("after Alt-Svc: %s", got)
	}
	if got := get("<3.0"); got != "HTTP/2.0" {
		t.Errorf("<3.0: %s", got)
	}
	s.up.h3.mu.Lock()
	clear(s.up.h3.alt)
	s.up.h3.mu.Unlock()
	if got := get("=3.0"); got != "HTTP/3.0" {
		t.Errorf("=3.0: %s", got)
	}
}
