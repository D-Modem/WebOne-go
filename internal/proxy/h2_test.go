package proxy

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/http2"
)

// grpcLike is a server that answers like gRPC: it echoes each request
// message as it comes, then ends with trailers.
func grpcLike(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor != 2 {
			http.Error(w, "HTTP/2 only", http.StatusHTTPVersionNotSupported)
			return
		}
		w.Header().Set("Content-Type", "application/grpc")
		w.Header().Set("Trailer", "Grpc-Status")
		w.WriteHeader(http.StatusOK)
		br := bufio.NewReader(r.Body)
		for {
			line, err := br.ReadString('\n')
			if line != "" {
				fmt.Fprintf(w, "echo %s", line)
				w.(http.Flusher).Flush()
			}
			if err != nil {
				break
			}
		}
		w.Header().Set("Grpc-Status", "0")
	}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv
}

// streamH2 sends two messages over one HTTP/2 stream, reading each echo
// before sending the next, so the proxy must stream both ways.
func streamH2(t *testing.T, tr *http2.Transport, url string) {
	t.Helper()
	pr, pw := io.Pipe()
	req, _ := http.NewRequest("POST", url, pr)
	req.Header.Set("Content-Type", "application/grpc")
	req.Header.Set("TE", "trailers")
	go io.WriteString(pw, "one\n") // the first message goes with the request
	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d: %s", resp.StatusCode, b)
	}
	br := bufio.NewReader(resp.Body)
	if l, _ := br.ReadString('\n'); l != "echo one\n" {
		t.Fatalf("first reply %q", l)
	}
	io.WriteString(pw, "two\n")
	if l, _ := br.ReadString('\n'); l != "echo two\n" {
		t.Fatalf("second reply %q", l)
	}
	pw.Close()
	io.Copy(io.Discard, br)
	if got := resp.Trailer.Get("Grpc-Status"); got != "0" {
		t.Errorf("trailer Grpc-Status %q, want 0", got)
	}
}

// TestConnectGRPC: a client that offers only h2 gets HTTP/2 inside a
// CONNECT tunnel, and its stream reaches the server with trailers intact.
func TestConnectGRPC(t *testing.T) {
	up := grpcLike(t)
	s, addr := startServer(t, "ValidateCertificates=no\n[SecureProxy]\nSslHashAlgorithm=SHA256\n")
	upAddr := up.Listener.Addr().String()
	s.up.h2.DialContext = func(ctx context.Context, network, a string) (net.Conn, error) {
		if a != "site.invalid:443" {
			return nil, fmt.Errorf("dialed %s", a)
		}
		return (&net.Dialer{}).DialContext(ctx, network, upAddr)
	}
	pool := x509.NewCertPool()
	pool.AddCert(s.CA.Cert)
	tr := &http2.Transport{
		DialTLSContext: func(ctx context.Context, network, _ string, _ *tls.Config) (net.Conn, error) {
			c, br := connectTunnel(t, addr)
			c.SetDeadline(time.Time{})
			tc := tls.Client(&bufConn{Conn: c, r: br}, &tls.Config{ServerName: "site.invalid", RootCAs: pool, NextProtos: []string{"h2"}})
			if err := tc.Handshake(); err != nil {
				return nil, err
			}
			if p := tc.ConnectionState().NegotiatedProtocol; p != "h2" {
				return nil, fmt.Errorf("ALPN %q", p)
			}
			return tc, nil
		},
	}
	defer tr.CloseIdleConnections()
	streamH2(t, tr, "https://site.invalid/echo.Echo/Stream")

	// a browser (http/1.1 offered too) still gets HTTP/1.1
	c, br := connectTunnel(t, addr)
	tc := tls.Client(&bufConn{Conn: c, r: br}, &tls.Config{ServerName: "site.invalid", RootCAs: pool, NextProtos: []string{"h2", "http/1.1"}})
	if err := tc.Handshake(); err != nil {
		t.Fatal(err)
	}
	if p := tc.ConnectionState().NegotiatedProtocol; p != "" {
		t.Errorf("browser got ALPN %q", p)
	}
}

// TestNonHttpTunnelALPN: a decrypting non-HTTP tunnel agrees on the
// server's ALPN choice, so gRPC through it gets HTTP/2.
func TestNonHttpTunnelALPN(t *testing.T) {
	up := grpcLike(t)
	upAddr := up.Listener.Addr().String()
	addr, _ := startProxy(t, "ValidateCertificates=no\nAllowNonHttpsCONNECT=yes\n[SecureProxy]\nSslHashAlgorithm=SHA256\n[NonHttpSslServers]\n127.0.0.1\n")
	tr := &http2.Transport{
		DialTLSContext: func(ctx context.Context, network, _ string, _ *tls.Config) (net.Conn, error) {
			c, err := net.Dial("tcp", addr)
			if err != nil {
				return nil, err
			}
			fmt.Fprintf(c, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", upAddr, upAddr)
			br := bufio.NewReader(c)
			if line, _ := br.ReadString('\n'); !strings.HasPrefix(line, "HTTP/1.1 200") {
				return nil, fmt.Errorf("CONNECT reply: %q", line)
			}
			for {
				if l, err := br.ReadString('\n'); err != nil || l == "\r\n" {
					break
				}
			}
			tc := tls.Client(&bufConn{Conn: c, r: br}, &tls.Config{InsecureSkipVerify: true, NextProtos: []string{"h2"}})
			if err := tc.Handshake(); err != nil {
				return nil, err
			}
			if p := tc.ConnectionState().NegotiatedProtocol; p != "h2" {
				return nil, fmt.Errorf("ALPN %q", p)
			}
			return tc, nil
		},
	}
	defer tr.CloseIdleConnections()
	streamH2(t, tr, "https://"+upAddr+"/echo.Echo/Stream")
}
