package proxy

import (
	"bufio"
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/D-Modem/webone-go/internal/rx"
)

// handleFetchError answers a request whose server could not be reached
// (the catch blocks around SendRequest in ProcessTransit). It returns true
// when the client has its answer.
func (t *transit) handleFetchError(err error) bool {
	u := t.urlString()
	archived := `<li>Try to use an <a href="http://web.archive.org/web/` + strconv.Itoa(time.Now().Year()) + "/" + u + `">archived copy</a> of the web site.</li>`
	check := "<li>If you are unable to load any pages, check your proxy server's network connection.</li>" +
		"<li>If your proxy server or network is protected by a firewall, make sure that WebOne is permitted to access the Web.</li>"

	var bad *badURLError
	if errors.As(err, &bad) {
		t.dump("!Invalid URL")
		t.sendError(400, "The URL <b>"+bad.u+"</b> is not valid.")
		return true
	}
	var dec *decodeError
	if errors.As(err, &dec) {
		t.dump("!Decompression failed")
		if t.sendInternalContent("Err-InvalidDataException.htm", "?ErrorMessage=Cannot+decode+HTTP+data.&URL="+qe(u), 200) {
			return true
		}
		msg := "<p><big>Cannot decode HTTP data.</big></p>" +
			"<ul>Remote server may use unsupported HTTP compression algorithm." +
			"<li>Try to set AllowHttpCompression=false option in configuration file to skip HTTP decompression.</li>" +
			"<li>If you are unable to load any pages, check your proxy server's network connection.</li></ul>"
		t.sendInfoPage("WebOne: Decompression failed", "Invalid data has been recieved", msg+"<br>URL: "+u, 500)
		return true
	}

	if isTimeout(err) {
		t.dump("!Connection timeout")
		const m = "The request was canceled due to Timeout of 100 seconds elapsing."
		if t.sendInternalContent("Err-TaskCanceledException.htm", "?ErrorMessage="+qe(m)+"&URL="+qe(u), 200) {
			return true
		}
		msg := "<p><big>" + m + "</big></p>" +
			"<ul><li>The site could be temporarily unavailable or too busy. Try again in a few moments.</li>" + archived +
			"<li>Internet Archive sometimes became busy. Be patient, wait a some time.</li>" + check + "</ul>"
		t.sendInfoPage("WebOne: Operation timeout", "The connection has timed out", msg+"<br>URL: "+u, 200)
		return true
	}

	t.log.Printf(" Cannot load this page: %v.", err)
	t.dump("!Network error: " + err.Error())
	if t.lookInWebArchive() {
		return true
	}

	title, header, message := "connection error", "Cannot load this page", ""
	if code, text := socketError(err); code != "" {
		if t.sendInternalContent("Err-"+code+".htm", "?ErrorMessage="+qe(text)+"&URL="+qe(u), 200) {
			return true
		}
		switch code {
		case "HostNotFound":
			header = "Cannot find the server"
			message = "<p><big>" + text + "</big></p>" +
				"<ul><li>Check the address for typing errors such as <strong>ww</strong>.example.com instead of <strong>www</strong>.example.com.</li>" +
				archived + check + "</ul>"
		case "ConnectionRefused", "ConnectionReset", "TimedOut":
			header = map[string]string{
				"ConnectionRefused": "The connection was refused",
				"ConnectionReset":   "The connection has been reset",
				"TimedOut":          "The connection has timed out",
			}[code]
			message = "<p><big>" + text + "</big></p>" +
				"<ul><li>The site could be temporarily unavailable or too busy. Try again in a few moments.</li>" + archived + check + "</ul>"
		default:
			header = "The connection can't be stablished"
			message = "<p><big>" + text + "</big></p>" +
				"<ul><li>The site could be temporarily unavailable or too busy. Try again in a few moments.</li>" + archived + check +
				"</ul><br>Error code: " + code
		}
	} else if polerr := tlsPolicyError(err); polerr != "" {
		if t.sendInternalContent("Err-TlsPolicyErrorException.htm", "?ErrorMessage="+qe(polerr)+"&URL="+qe(u), 200) {
			return true
		}
		header = "Secure connection could not be established"
		message = "<p><big>" + polerr + "</big></p>" +
			"<ul><li>The page you are trying to view cannot be shown because the authenticity of the received data could not be verified.</li>" +
			"<li>Make sure that the OS on the proxy server have all updates installed.</li>" +
			"<li>Check date and time on the proxy server.</li>" +
			"<li>Verify that the proxy server operating system have proper support for TLS/SSL version and chiphers used on the site.</li>" +
			archived +
			"<li>To disable this security check, set <q><b>ValidateCertificates=no</b></q> in proxy configuration file. But this will make the proxy less secure, do this at your own risk.</li></ul>"
	} else {
		title = fmt.Sprintf("%T", errors.Unwrap(err))
		if title == "<nil>" {
			title = "connection error"
		}
		message = "There are problems, that are preventing from displaying the page:<br>" + strings.ReplaceAll(err.Error(), "\n", "<br>")
	}
	t.sendInfoPage("WebOne: "+title, header, message+"<br>URL: "+u, 200)
	return true
}

func isTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var ne net.Error
	var oe *net.OpError
	// a dial timeout is a socket error (TimedOut); a whole-request
	// timeout is the 100 seconds one
	return errors.As(err, &ne) && ne.Timeout() && !(errors.As(err, &oe) && oe.Op == "dial")
}

// socketError is the .NET SocketError name of a network error, and its
// message.
func socketError(err error) (string, string) {
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		if dnsErr.IsNotFound {
			return "HostNotFound", "No such host is known. (" + dnsErr.Name + ")"
		}
		if dnsErr.IsTimeout {
			return "TryAgain", dnsErr.Error()
		}
		return "HostNotFound", dnsErr.Error()
	}
	var oe *net.OpError
	if !errors.As(err, &oe) {
		return "", ""
	}
	switch {
	case errors.Is(err, syscall.ECONNREFUSED):
		return "ConnectionRefused", "Connection refused (" + oe.Addr.String() + ")"
	case errors.Is(err, syscall.ECONNRESET):
		return "ConnectionReset", "Connection reset by peer"
	case errors.Is(err, syscall.EHOSTUNREACH):
		return "HostUnreachable", "No route to host"
	case errors.Is(err, syscall.ENETUNREACH):
		return "NetworkUnreachable", "Network is unreachable"
	case oe.Timeout():
		return "TimedOut", "Connection timed out"
	}
	return "SocketError", oe.Err.Error()
}

// tlsPolicyError describes a server certificate that is not trusted.
func tlsPolicyError(err error) string {
	var host x509.HostnameError
	var unk x509.UnknownAuthorityError
	var inv x509.CertificateInvalidError
	var verr *tlsVerifyError
	switch {
	case errors.As(err, &host):
		return "The certificate is issued for another site."
	case errors.As(err, &unk):
		return "Certificate chain is incorrect."
	case errors.As(err, &inv):
		return "Certificate is not valid: " + inv.Error()
	case errors.As(err, &verr):
		return verr.Error()
	case strings.Contains(err.Error(), "tls:"):
		return err.Error()
	}
	return ""
}

type tlsVerifyError struct{ msg string }

func (e *tlsVerifyError) Error() string { return e.msg }

// lookInWebArchive looks for the page in the Wayback Machine and answers
// with it (HttpTransit.LookInWebArchive). It returns true when the client
// has its answer, or the archived page is fetched and ready to send.
func (t *transit) lookInWebArchive() bool {
	if !t.cfg.SearchInArchive || t.url == nil {
		return false
	}
	if h := t.url.Hostname(); h == "web.archive.org" || h == "archive.org" {
		return false
	}
	t.log.Printf(" Look in Archive.org...")
	t.dump("=Look in Web Archive...")
	archived, err := t.s.webArchive(t.urlString())
	if err != nil {
		if t.resp.HeadersSent {
			return true
		}
		m := strings.ReplaceAll(err.Error(), "\n", "<br>")
		if t.sendInternalContent("Err-WArchiveException.htm", "?ErrorMessage="+qe(m)+"&URL="+qe(t.urlString()), 200) {
			return true
		}
		t.sendInfoPage("WebOne: Web Archive error.", "Cannot load this page",
			"<b>The requested server or page is not found and a Web Archive error occured.</b><br>"+m, 200)
		return true
	}
	if archived == "" {
		t.log.Printf(" No snapshots.")
		t.dump("=Not in Web Archive")
		return false
	}
	t.log.Printf(" Available.")
	g := rx.FindGroups(`^http://web.archive.org/web/([0-9]*)/(.*)`, archived)
	if t.cfg.HideArchiveRedirect && g != nil {
		target := "http://web.archive.org/web/" + g[1] + "id_/" + g[2]
		nu, perr := parseURL(target)
		if perr == nil {
			t.dump("=Go to Web Archive, internal: " + target)
			if t.transitStream != nil {
				t.transitStream.Close()
				t.transitStream = nil
			}
			t.url = nu
			if t.op == nil {
				t.op = &operation{Method: "GET", Header: NewHeader(), HTTPVersion: t.requestHTTPVersion, log: t.log}
			}
			t.op.Body = nil
			t.op.Secure = false
			perr = t.sendRequest()
		}
		if perr == nil {
			return true
		}
		m := strings.ReplaceAll(perr.Error(), "\n", "<br>")
		if t.sendInternalContent("Err-WArchiveRetrieveException.htm", "?ErrorMessage="+qe(m)+"&URL="+qe(t.urlString()), 200) {
			return true
		}
		t.sendInfoPage("WebOne: Web Archive retrieve error.", "Cannot load this page from Web Archive",
			"<b>The requested page is found only at Web Archive, but cannot be delivered from it.</b><br>"+m, 200)
		return true
	}
	if t.cfg.ArchiveUrlSuffix != "" && g != nil {
		scheme := "http"
		if t.req.Secure {
			scheme = "https"
		}
		archived = scheme + "://web.archive.org/web/" + g[1] + t.cfg.ArchiveUrlSuffix + "/" + g[2]
	}
	t.sendRedirect(archived, "<b>Good news:</b> an archived copy of now-removed content is available.")
	return true
}

// webArchive asks the Wayback Machine's CDX API for the latest good
// snapshot of u (WebArchiveRequest.cs). "" means none.
func (s *Server) webArchive(u string) (string, error) {
	cdx := "https://web.archive.org/cdx/search/cdx?fl=timestamp,original,statuscode&url=" + url.QueryEscape(u)
	resp, err := s.fetch(cdx)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", errors.New("Unsuccessful Web Archive request: " + resp.Status)
	}
	var lines []string
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64*1024), 4<<20)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) == 0 || lines[0] == "" {
		return "", nil
	}
	last := ""
	for _, l := range lines {
		f := strings.Split(l, " ")
		if len(f) != 3 {
			continue
		}
		if s.cfg.ArchiveDateLimit > 0 {
			ts, _ := strconv.ParseInt(f[0], 10, 64)
			if ts > int64(s.cfg.ArchiveDateLimit)*1000000 {
				continue
			}
		}
		if f[2] == "200" {
			last = l
		}
	}
	if last == "" {
		last = lines[len(lines)-1]
	}
	f := strings.Split(last, " ")
	if len(f) != 3 {
		return "", errors.New("Incorrect Web Archive request: " + last)
	}
	return "http://web.archive.org/web/" + f[0] + "/" + f[1], nil
}

// isLanIP: ip is on a local network (loopback, private, link-local).
// WebOne pings the address with TTL 1 instead; this is the same answer
// without sending anything.
func isLanIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast()
}
