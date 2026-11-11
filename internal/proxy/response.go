package proxy

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
)

// statusText is WebOne's list of reason phrases (HttpResponse.cs).
var statusText = map[int]string{
	100: "Continue", 101: "Switching Protocols",
	200: "OK", 201: "Created", 202: "Accepted", 203: "Non-Authoritative Information", 204: "No Content",
	205: "Reset Content", 206: "Partial Content",
	300: "Multiple Choices", 301: "Moved Permanently", 302: "Moved Temporarily", 303: "See Other",
	304: "Not Modified", 305: "Use Proxy", 307: "Temporary Redirect", 308: "Permanent Redirect",
	400: "Bad Request", 401: "Unauthorized", 402: "Payment Required", 403: "Forbidden", 404: "Not Found",
	405: "Method Not Allowed", 406: "Not Acceptable", 407: "Proxy Authentication Required",
	408: "Request Timeout", 409: "Conflict", 410: "Gone", 411: "Length Required", 412: "Precondition Failed",
	413: "Request Entity Too Large", 414: "Request-URI Too Long", 415: "Unsupported Media Type",
	418: "I'm a teapot", 429: "Too Many Requests",
	500: "Internal Server Error", 501: "Not Implemented", 502: "Bad Gateway", 503: "Service Unavailable",
	504: "Gateway Timeout", 505: "HTTP Version Not Supported",
}

// statusName is .NET's HttpStatusCode name of a code ("NotFound"), as
// WebOne shows in its error pages.
var statusName = map[int]string{
	200: "OK", 204: "NoContent", 206: "PartialContent", 301: "MovedPermanently", 302: "Redirect", 303: "RedirectMethod",
	304: "NotModified", 307: "TemporaryRedirect", 308: "PermanentRedirect",
	400: "BadRequest", 401: "Unauthorized", 403: "Forbidden", 404: "NotFound", 405: "MethodNotAllowed",
	406: "NotAcceptable", 407: "ProxyAuthenticationRequired", 408: "RequestTimeout", 409: "Conflict", 410: "Gone",
	429: "TooManyRequests", 451: "UnavailableForLegalReasons",
	500: "InternalServerError", 501: "NotImplemented", 502: "BadGateway", 503: "ServiceUnavailable",
	504: "GatewayTimeout",
}

// StatusName is the .NET enum name of a status code, or the number.
func StatusName(code int) string {
	if n, ok := statusName[code]; ok {
		return n
	}
	return strconv.Itoa(code)
}

// Response is the reply to the client, sent by hand on the connection as
// WebOne's HttpResponse does (so it can speak HTTP/1.0 to HTTP/1.0 clients).
type Response struct {
	Status        int
	StatusMessage string // "" for the usual one
	Major, Minor  int
	Header        *Header
	// SimpleContentType strips parameters from Content-Type (RFC 1945
	// clients: [Http10Only]).
	SimpleContentType bool
	// KeepAlive: the connection stays open after this response.
	KeepAlive   bool
	HeadersSent bool
	// NoBody: the response has no body (a reply to HEAD; 204, 304, 1xx):
	// what is written is dropped.
	NoBody bool

	contentLength int64 // -1 unknown, -2 not set
	chunked       bool
	bw            *bufio.Writer
	conn          net.Conn
	clientWantsKA bool
	closed        bool
	wroteBody     int64
}

func newResponse(conn net.Conn, keepAlive bool) *Response {
	return &Response{
		Status: 200, Major: 1, Minor: 1, Header: NewHeader(),
		contentLength: -2, bw: bufio.NewWriterSize(conn, 32*1024), conn: conn, clientWantsKA: keepAlive,
	}
}

// SetVersion sets the HTTP version of the reply ("1.0", "1.1").
func (w *Response) SetVersion(major, minor int) {
	if major < 1 {
		major, minor = 1, 0
	}
	if major > 1 {
		major, minor = 1, 1
	}
	w.Major, w.Minor = major, minor
}

// SetContentType sets Content-Type.
func (w *Response) SetContentType(ct string) {
	if w.SimpleContentType {
		if i := strings.IndexByte(ct, ';'); i >= 0 {
			ct = ct[:i]
		}
	}
	w.Header.Set("Content-Type", ct)
}

// AddHeader adds a header (Content-Type replaces).
func (w *Response) AddHeader(k, v string) {
	if strings.EqualFold(k, "Content-Type") {
		w.SetContentType(v)
		return
	}
	w.Header.Add(k, v)
}

// SetContentLength sets Content-Length; below 0 means unknown: chunked
// transfer on HTTP/1.1, and the end of the connection on HTTP/1.0.
func (w *Response) SetContentLength(n int64) {
	w.contentLength = n
	if n < 0 {
		w.Header.Del("Content-Length")
		if w.Major == 1 && w.Minor >= 1 {
			w.Header.Set("Transfer-Encoding", "chunked")
			w.chunked = true
			return
		}
		w.KeepAlive = false
		w.Header.Set("Connection", "Close")
		return
	}
	w.Header.Del("Transfer-Encoding")
	w.chunked = false
	w.Header.Set("Content-Length", strconv.FormatInt(n, 10))
}

// ContentLength is what was set (-1 unknown, -2 not set).
func (w *Response) ContentLength() int64 { return w.contentLength }

// SendHeaders writes the status line and headers.
func (w *Response) SendHeaders(version string) error {
	if w.HeadersSent {
		return errors.New("HTTP response headers are already sent")
	}
	if !w.Header.Has("Server") && !w.Header.Has("Via") {
		w.Header.Add("Server", "WebOne/"+version)
	}
	if w.Status == 204 || w.Status == 304 || w.Status/100 == 1 {
		w.NoBody = true
		w.Header.Del("Transfer-Encoding")
	}
	if w.NoBody {
		w.chunked = false
	}
	if w.Header.Get("Content-Type") == noContentType {
		w.Header.Del("Content-Type")
	}
	if !w.Header.Has("Connection") {
		// WebOne always answers Keep-Alive; this port only does when the
		// client asked for it (an HTTP/1.0 client that did not would wait
		// for the end of the connection)
		if w.clientWantsKA || (w.Major == 1 && w.Minor >= 1 && w.contentLength >= 0) {
			w.Header.Add("Connection", "Keep-Alive")
			w.KeepAlive = true
		} else {
			w.Header.Add("Connection", "Close")
			w.KeepAlive = false
		}
	} else if strings.EqualFold(w.Header.Get("Connection"), "close") {
		w.KeepAlive = false
	} else if w.contentLength == -2 {
		w.KeepAlive = false
	}
	if w.contentLength == -2 && w.Status != 304 && w.Status != 204 && w.Status/100 != 1 {
		// no length and no chunks: the end of the body is the end of the connection
		w.KeepAlive = false
	}
	msg := w.StatusMessage
	if msg == "" {
		msg = statusText[w.Status]
	}
	line := fmt.Sprintf("HTTP/%d.%d %d", w.Major, w.Minor, w.Status)
	if msg != "" {
		line += " " + msg
	}
	if _, err := io.WriteString(w.bw, line+"\r\n"+w.Header.String()+"\r\n"); err != nil {
		return err
	}
	w.HeadersSent = true
	return w.bw.Flush()
}

// Write writes body bytes (as a chunk when chunked).
func (w *Response) Write(p []byte) (int, error) {
	if !w.HeadersSent {
		return 0, errors.New("call SendHeaders() first before sending body")
	}
	if len(p) == 0 {
		return 0, nil
	}
	if w.NoBody {
		return len(p), nil
	}
	if w.chunked {
		if _, err := fmt.Fprintf(w.bw, "%X\r\n", len(p)); err != nil {
			return 0, err
		}
		if _, err := w.bw.Write(p); err != nil {
			return 0, err
		}
		if _, err := w.bw.WriteString("\r\n"); err != nil {
			return 0, err
		}
	} else if _, err := w.bw.Write(p); err != nil {
		return 0, err
	}
	w.wroteBody += int64(len(p))
	return len(p), nil
}

// Flush sends what is buffered.
func (w *Response) Flush() error { return w.bw.Flush() }

// Close ends the response (and the connection, unless kept alive).
func (w *Response) Close(version string) error {
	if w.closed {
		return nil
	}
	if !w.HeadersSent {
		if err := w.SendHeaders(version); err != nil {
			return err
		}
	}
	w.closed = true
	if w.chunked {
		w.bw.WriteString("0\r\n\r\n")
	}
	err := w.bw.Flush()
	if !w.KeepAlive {
		w.conn.Close()
	}
	return err
}

// Connected: the connection is still usable.
func (w *Response) Connected() bool { return !w.closed || w.KeepAlive }
