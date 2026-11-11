package proxy

import (
	"bufio"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/D-Modem/webone-go/internal/certs"
	"github.com/D-Modem/webone-go/internal/config"
	"github.com/D-Modem/webone-go/internal/logx"
)

// startProxy runs a proxy with the given configuration text; it returns
// the proxy's address and the CA.
func startProxy(t *testing.T, conf string) (string, *certs.CA) {
	t.Helper()
	logx.SetConsole(io.Discard)
	dir := t.TempDir()
	cfg := config.New()
	cfg.Vars.Set("WOVer", Version)
	cfg.Vars.Set("WOSystem", "test")
	cfg.LoadContent([]byte("[Server]\nUpperProxy=no\nSearchInArchive=no\nContentDirectory="+dir+"\n"+conf), "test.conf")
	if err := cfg.Process(); err != nil {
		t.Fatal(err)
	}
	cfg.ContentDirectory = dir
	crt, key := filepath.Join(dir, "ca.crt"), filepath.Join(dir, "ca.key")
	if err := certs.CreateCA(crt, key, "", "SHA256", time.Now().Add(-time.Hour), time.Now().AddDate(1, 0, 0)); err != nil {
		t.Fatal(err)
	}
	ca, err := certs.LoadCA(crt, key)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Port = ln.Addr().(*net.TCPAddr).Port
	cfg.DefaultHostName = "127.0.0.1"
	cfg.SetProxyVars()
	cfg.PAC = config.DefaultPACHeader + config.DefaultPACHttp + config.DefaultPACFooter
	s, err := New(cfg, ca, ca)
	if err != nil {
		t.Fatal(err)
	}
	go s.Serve(ln)
	t.Cleanup(s.Stop)
	return ln.Addr().String(), ca
}

// rawGet sends a request by hand (as an old browser would) and returns the
// whole reply.
func rawGet(t *testing.T, addr, req string) string {
	t.Helper()
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(10 * time.Second))
	io.WriteString(c, req)
	b, _ := io.ReadAll(c)
	return string(b)
}

func TestHTTP10ProxyEditsAndCodePage(t *testing.T) {
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Strict-Transport-Security", "max-age=1")
		fmt.Fprint(w, "<html><body>Hello, modern web! Привет</body></html>")
	}))
	defer site.Close()
	addr, _ := startProxy(t, "[Server]\nOutputEncoding=windows-1251\n[Edit:127.0.0.1]\nAddFind=modern\nAddReplace=old\n")
	reply := rawGet(t, addr, "GET "+site.URL+"/ HTTP/1.0\r\nUser-Agent: Mozilla/2.0\r\n\r\n")
	head, body, _ := strings.Cut(reply, "\r\n\r\n")
	if !strings.HasPrefix(head, "HTTP/1.0 200") {
		t.Fatalf("status: %q", head)
	}
	if strings.Contains(head, "Strict-Transport-Security") {
		t.Error("HSTS header passed to the client")
	}
	if strings.Contains(head, "Transfer-Encoding") {
		t.Error("chunked reply to an HTTP/1.0 client")
	}
	if !strings.Contains(body, "Hello, old web!") {
		t.Errorf("find/replace not done: %q", body)
	}
	if !strings.Contains(body, "\xcf\xf0\xe8\xe2\xe5\xf2") { // "Привет" in windows-1251
		t.Errorf("not in windows-1251: %q", body)
	}
}

func TestHTTPSUpstreamAndAlternateMode(t *testing.T) {
	site := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<a href="https://%s/next">secure link</a>`, r.Host)
	}))
	defer site.Close()
	u, _ := url.Parse(site.URL)
	addr, _ := startProxy(t, "[Server]\nValidateCertificates=no\n[ForceHttps]\n127.0.0.1\n")

	// the old browser asks for http://; the proxy fetches https:// and
	// gives the links back as http://
	reply := rawGet(t, addr, "GET http://"+u.Host+"/ HTTP/1.0\r\n\r\n")
	if !strings.Contains(reply, "200 OK") || !strings.Contains(reply, `href="http://`+u.Host+`/next"`) {
		t.Fatalf("reply: %q", reply)
	}
	// alternate mode: http://proxy/https://site/
	reply = rawGet(t, addr, "GET /"+site.URL+"/ HTTP/1.0\r\nHost: "+addr+"\r\n\r\n")
	if !strings.Contains(reply, "200 OK") || !strings.Contains(reply, "secure link") {
		t.Fatalf("alternate reply: %q", reply)
	}
}

func TestKeepAliveAndChunked(t *testing.T) {
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.(http.Flusher).Flush() // no length: chunked to the proxy
		w.Write([]byte(strings.Repeat("x", 5000)))
	}))
	defer site.Close()
	addr, _ := startProxy(t, "")
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(10 * time.Second))
	br := bufio.NewReader(c)
	for i := 0; i < 2; i++ {
		fmt.Fprintf(c, "GET %s/%d HTTP/1.1\r\nHost: %s\r\nConnection: keep-alive\r\n\r\n", site.URL, i, strings.TrimPrefix(site.URL, "http://"))
		resp, err := http.ReadResponse(br, nil)
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		b, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil || len(b) != 5000 {
			t.Fatalf("request %d: %d bytes, %v", i, len(b), err)
		}
	}
}

func TestInternalPages(t *testing.T) {
	addr, ca := startProxy(t, "")
	for _, p := range []string{"/", "/!codepages", "/auto.pac", "/!ca"} {
		reply := rawGet(t, addr, "GET "+p+" HTTP/1.0\r\nHost: "+addr+"\r\n\r\n")
		if !strings.HasPrefix(reply, "HTTP/1.0 200") {
			t.Errorf("%s: %q", p, reply[:min(len(reply), 200)])
		}
	}
	reply := rawGet(t, addr, "GET /!ca HTTP/1.0\r\nHost: "+addr+"\r\n\r\n")
	_, body, _ := strings.Cut(reply, "\r\n\r\n")
	if body != string(ca.DER()) {
		t.Error("/!ca is not the CA certificate")
	}
	reply = rawGet(t, addr, "HEAD /auto.pac HTTP/1.1\r\nHost: "+addr+"\r\nConnection: close\r\n\r\n")
	if _, body, _ := strings.Cut(reply, "\r\n\r\n"); body != "" {
		t.Errorf("HEAD reply has a body: %q", body)
	}
}

func TestConnectMITM(t *testing.T) {
	addr, ca := startProxy(t, "[SecureProxy]\nSslHashAlgorithm=SHA256\n")
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(20 * time.Second))
	fmt.Fprint(c, "CONNECT site.invalid:443 HTTP/1.0\r\n\r\n")
	br := bufio.NewReader(c)
	line, _ := br.ReadString('\n')
	if !strings.HasPrefix(line, "HTTP/1.1 200") {
		t.Fatalf("CONNECT reply: %q", line)
	}
	for {
		l, err := br.ReadString('\n')
		if err != nil || l == "\r\n" {
			break
		}
	}
	pool := x509.NewCertPool()
	pool.AddCert(ca.Cert)
	tc := tls.Client(&bufConn{Conn: c, r: br}, &tls.Config{ServerName: "site.invalid", RootCAs: pool, MaxVersion: tls.VersionTLS12})
	if err := tc.Handshake(); err != nil {
		t.Fatalf("handshake: %v", err)
	}
	if got := tc.ConnectionState().PeerCertificates[0].DNSNames; len(got) != 1 || got[0] != "site.invalid" {
		t.Errorf("certificate names: %v", got)
	}
	// a request inside the tunnel is handled as any other: this site does
	// not exist, so the reply is the proxy's error page
	fmt.Fprint(tc, "GET / HTTP/1.0\r\nHost: site.invalid\r\n\r\n")
	resp, err := http.ReadResponse(bufio.NewReader(tc), nil)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(b), "site.invalid") {
		t.Errorf("error page: %d %q", resp.StatusCode, b)
	}
}

func TestPassThrough(t *testing.T) {
	echo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Close()
	go func() {
		c, err := echo.Accept()
		if err == nil {
			io.Copy(c, c)
			c.Close()
		}
	}()
	addr, _ := startProxy(t, "[ConnectPassThrough]\n127\\.0\\.0\\.1\n")
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(10 * time.Second))
	fmt.Fprintf(c, "CONNECT %s HTTP/1.1\r\n\r\n", echo.Addr())
	br := bufio.NewReader(c)
	for {
		l, err := br.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if l == "\r\n" {
			break
		}
	}
	io.WriteString(c, "ping")
	buf := make([]byte, 4)
	if _, err := io.ReadFull(br, buf); err != nil || string(buf) != "ping" {
		t.Fatalf("tunnel: %q %v", buf, err)
	}
}

func TestKindOf(t *testing.T) {
	cfg := config.New()
	cfg.Port = 8080
	cfg.DefaultHostName = "proxy.example"
	s := &Server{cfg: cfg}
	cases := []struct {
		raw, host string
		connect   bool
		want      Kind
	}{
		{"/123", "example.com", false, StandardRemote},
		{"/", "proxy.example:8080", false, StandardHttp},
		{"/http://example.com/", "proxy.example:8080", false, AlternateProxy},
		{"http://example.com/", "example.com", false, StandardProxy},
		{"ftp://example.com/pub", "", false, StandardProxy},
		{"example.com:443", "", true, StandardSslProxy},
	}
	for _, c := range cases {
		got, err := s.kindOf(c.raw, c.host, c.connect)
		if err != nil || got != c.want {
			t.Errorf("kindOf(%q, %q) = %v, %v; want %v", c.raw, c.host, got, err, c.want)
		}
	}
}

func TestFtpListParsing(t *testing.T) {
	e, ok := parseFtpLine("drwxr-xr-x    2 0        0            4096 Jan 01  2020 pub")
	if !ok || !e.Directory || e.Name != "pub" {
		t.Errorf("unix: %+v %v", e, ok)
	}
	e, ok = parseFtpLine("01-02-03  10:00AM       <DIR>          windows")
	if !ok || !e.Directory || e.Name != "windows" {
		t.Errorf("dos: %+v %v", e, ok)
	}
	e, ok = parseFtpLine("01-02-03  10:00AM              1234 readme.txt")
	if !ok || e.Directory || e.Size != 1234 {
		t.Errorf("dos file: %+v %v", e, ok)
	}
}

func TestActivexVersions(t *testing.T) {
	a, _ := parseDotVersion("6.4.7.1112")
	b, _ := parseDotVersion("6.4")
	if cmpVersion(a, b) <= 0 || cmpVersion(b, b) != 0 {
		t.Error("version compare")
	}
	if _, ok := parseDotVersion("6"); ok {
		t.Error("one-part version accepted")
	}
}
