package proxy

import (
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/D-Modem/webone-go/internal/config"
	"github.com/D-Modem/webone-go/internal/textenc"
)

// infoPage is one of the proxy's own HTML pages (InfoPage.cs).
type infoPage struct {
	Title, Header, Content string
	Status                 int
	Headers                *Header
	HTMLHeaders            string
	ShowFooter             bool
	Attachment             io.ReadCloser
	AttachmentType         string
}

func newInfoPage(title, header, content string, status int) *infoPage {
	if content == "" {
		content = "No description is available."
	}
	return &infoPage{Title: title, Header: header, Content: content, Status: status, Headers: NewHeader(), ShowFooter: true}
}

// outEnc is the code page the proxy's own pages are sent in.
func (t *transit) outEnc() *textenc.Encoding {
	if t.outputEncoding != nil {
		return t.outputEncoding
	}
	return textenc.UTF8
}

// sendBytes sends a complete response.
func (t *transit) sendBytes(status int, contentType string, body []byte) {
	w := t.resp
	w.Status = status
	if contentType != "" {
		w.SetContentType(contentType)
	}
	w.SetContentLength(int64(len(body)))
	if err := w.SendHeaders(Version); err != nil {
		t.clientError(err)
		return
	}
	if t.req.Method != "HEAD" {
		if _, err := w.Write(body); err != nil {
			t.clientError(err)
		}
	}
}

// sendError sends a short error page (HttpTransit.SendError).
func (t *transit) sendError(code int, text string) {
	if t.resp.HeadersSent {
		t.log.Printf("<!Not returned code %d. Response is in process of sending.", code)
		t.resp.KeepAlive = false
		return
	}
	t.log.Printf("<Return code %d.", code)
	text += t.s.infoString()
	head := ""
	if r := t.resp.Header.Get("Refresh"); r != "" {
		head = `<META HTTP-EQUIV="REFRESH" CONTENT="` + r + `">`
	}
	enc := t.outEnc()
	head += `<META CHARSET="` + enc.Name + `">`
	html := "<HTML>" + head + "<BODY><H1>" + strconv.Itoa(code) + " " + StatusName(code) + "</H1>" + text + "</BODY></HTML>"
	t.sendBytes(code, "text/html", enc.Encode(html))
	t.dump("End is internal page: code " + strconv.Itoa(code) + ", " + text)
}

// sendRedirect sends a 302 to url (HttpTransit.SendRedirect).
func (t *transit) sendRedirect(u, message string) {
	target := u
	if t.req.Secure && strings.HasPrefix(target, "http://") {
		target = "https://" + target[len("http://"):]
	}
	if strings.HasPrefix(target, "//") {
		if t.req.Secure {
			target = "https:" + target
		} else {
			target = "http:" + target
		}
	}
	t.log.Printf("<Return redirect.")
	html := `<HTML><HEAD><META HTTP-EQUIV="REFRESH" CONTENT="` + target + `"><TITLE>WebOne: redirect by proxy</TITLE></HEAD><BODY>`
	if message != "" {
		html += "<P>" + message + "</P>"
	}
	html += `<P>Please navigate your browser to <A HREF="` + target + `">` + target + "</A>.</P>" + t.s.infoString() + "</BODY></HTML>"
	t.resp.AddHeader("Location", target)
	t.sendBytes(302, "text/html", t.outEnc().Encode(html))
	t.dump("End is 302 to " + target)
}

// sendInfoPage sends an information page.
func (t *transit) sendInfoPage(title, header, content string, status int) {
	t.sendInfo(newInfoPage(title, header, content, status))
}

func (t *transit) sendInfo(p *infoPage) {
	p.Headers.Each(func(k, v string) {
		if !strings.EqualFold(k, "Content-Type") {
			t.resp.AddHeader(k, v)
		}
	})
	if p.Attachment != nil {
		ct := p.AttachmentType
		if ct == "" {
			ct = p.Headers.Get("Content-Type")
		}
		t.sendStream(p.Attachment, ct, -1, p.Status)
		return
	}
	t.log.Printf("<Return information page: %s.", p.Title)
	if p.ShowFooter && fileExists(filepath.Join(t.cfg.ContentDirectory, "InfoPage.htm")) {
		args := "?Title=" + url.QueryEscape(p.Title) + "&Header=" + url.QueryEscape(p.Header) + "&Content=" + url.QueryEscape(p.Content)
		if ct := p.Headers.Get("Content-Type"); ct != "" {
			t.resp.SetContentType(ct)
		}
		if t.sendInternalContent("InfoPage.htm", args, p.Status) {
			return
		}
	}
	enc := t.outEnc()
	title := "<title>WebOne: untitled</title>"
	if p.Title != "" {
		title = "<title>" + p.Title + "</title>\n"
	}
	h1 := ""
	if p.Header != "" {
		h1 = "<h1>" + p.Header + "</h1>\n"
	}
	footer := ""
	if p.ShowFooter {
		footer = t.s.infoString() + "\n"
	}
	html := "<html>\n" + title + `<meta charset="` + enc.Name + `"/>` + "\n" + p.HTMLHeaders + "<body>\n" + h1 + "\n" + p.Content + "\n" + footer + "</body>\n</html>"
	ct := p.Headers.Get("Content-Type")
	if ct == "" {
		ct = "text/html"
	}
	t.sendBytes(p.Status, ct, enc.Encode(html))
	t.dump("End is information page: " + p.Header)
}

// sendStream sends a stream (length -1 if unknown).
func (t *transit) sendStream(r io.ReadCloser, contentType string, length int64, status int) {
	defer r.Close()
	if length >= 0 {
		t.log.Printf("<Send stream with %dK of %s.", length/1024, contentType)
	} else {
		t.log.Printf("<Send %s stream.", contentType)
	}
	w := t.resp
	if status == 0 {
		status = 200
	}
	w.Status = status
	w.SetContentType(contentType)
	w.SetContentLength(length)
	if err := w.SendHeaders(Version); err != nil {
		t.clientError(err)
		return
	}
	if _, err := io.Copy(w, r); err != nil {
		t.log.Printf("<Stream closed: %v", err)
		w.KeepAlive = false
	}
	t.dump("End is stream of " + contentType)
}

// sendFile sends a file.
func (t *transit) sendFile(name, contentType, downloadName string) {
	t.log.Printf("<Send file %s.", name)
	f, err := os.Open(name)
	if err != nil {
		code := 500
		if os.IsNotExist(err) {
			code = 404
		}
		t.sendError(code, "Cannot open the file <i>"+name+"</i>.<br>"+err.Error())
		return
	}
	st, _ := f.Stat()
	if downloadName != "" {
		t.resp.AddHeader("Content-Disposition", `attachment; filename="`+downloadName+`"`)
	}
	t.sendStream(f, contentType, st.Size(), 200)
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// contentPath is a file in the content directory, or "" when the id
// tries to climb out of it.
func (t *transit) contentPath(id string) string {
	if strings.Contains(id, "..") {
		return ""
	}
	id = strings.TrimLeft(strings.ReplaceAll(id, `\`, "/"), "/")
	return filepath.Join(t.cfg.ContentDirectory, filepath.FromSlash(id))
}

// parseQueryString reads "?a=b&c=d" as WebOne does: names trimmed of "?"
// and spaces, values URL-decoded.
// qe escapes a value for an internal page's query string.
func qe(s string) string { return url.QueryEscape(s) }

func parseQueryString(q string) *config.Vars {
	v := config.NewVars()
	if strings.TrimSpace(q) == "" {
		return v
	}
	for _, seg := range strings.Split(q, "&") {
		k, val, ok := strings.Cut(seg, "=")
		if !ok {
			continue
		}
		k = strings.Trim(k, "? ")
		if d, err := url.QueryUnescape(val); err == nil {
			val = d
		}
		v.TryAdd(k, strings.TrimSpace(val))
	}
	return v
}

// sendInternalContent sends a file of the content directory (html/),
// expanding %variables% in text files with args and the server's figures.
// false: there is no such file.
func (t *transit) sendInternalContent(id, args string, status int) bool {
	if strings.Contains(id, "../") {
		t.sendError(403, "Please specify an absoulte path.")
		return true
	}
	path := t.contentPath(id)
	if path == "" || !fileExists(path) {
		t.log.Printf("Cannot find the file: %s.", filepath.Join(t.cfg.ContentDirectory, id))
		return false
	}
	ext := strings.ToLower(filepath.Ext(path))
	ext = strings.TrimPrefix(ext, ".")
	mime := "application/octet-stream"
	if m, ok := t.cfg.MimeTypes[ext]; ok {
		mime = m
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	t.resp.SetVersion(1, 1)
	if strings.TrimSpace(args) == "" {
		t.resp.AddHeader("Expires", time.Now().AddDate(0, 0, 30).UTC().Format(http.TimeFormat))
	}
	if containsAny(mime, t.cfg.TextTypes) {
		sub := parseQueryString(args)
		sub.Set("ServerName", t.serverName())
		sub.Set("OpenedConnections", strconv.FormatInt(t.s.opened.Load(), 10))
		sub.Set("BusyConnections", strconv.FormatInt(t.s.busy.Load(), 10))
		sub.Set("UsedMemory", strconv.Itoa(usedMemoryMB()))
		sub.Set("ClientIP", t.req.Remote.String())
		text := string(data)
		text = strings.TrimPrefix(text, "\uFEFF")
		text = t.cfg.Expand(text, sub)
		t.sendBytes(status, mime, t.outEnc().Encode(text))
		return true
	}
	t.sendBytes(status, mime, data)
	return true
}

// notModified: the client's copy of a content file is current.
func (t *transit) notModified(id, ims string) bool {
	if strings.TrimSpace(ims) == "" {
		return false
	}
	path := t.contentPath(id)
	st, err := os.Stat(path)
	if path == "" || err != nil {
		return false
	}
	when, err := http.ParseTime(ims)
	if err != nil {
		if w2, err2 := config.ParseDate(ims); err2 == nil {
			when = w2
		} else {
			return false
		}
	}
	return !when.Before(st.ModTime().Truncate(time.Second))
}

// sendInternalPage serves the proxy's own URLs (HttpTransit.SendInternalPage).
func (t *transit) sendInternalPage(page, query string) {
	t.log.Printf(" Internal page: %s ", page)
	q := ""
	if query != "" {
		q = "?" + query
	}
	switch page {
	case "", "/", "/!", "/!/":
		t.sendStatusPage()
	case "/!codepages":
		t.sendCodepages()
	case "/!img-test":
		if t.cfg.EnableManualConverting {
			t.sendError(200, `ImageMagick test.<br><img src="/!convert/?src=logo.webp&dest=gif&type=image/gif" alt="ImageMagick logo" width=640 height=480><br>A wizard should appear nearby.`)
		} else {
			t.sendError(200, `ImageMagick test.<br><img src="/!imagemagicktest.gif" alt="ImageMagick logo" width=640 height=480><br>A wizard should appear nearby.`)
		}
	case "/!imagemagicktest.gif":
		t.imageMagickTest()
	case "/!convert":
		t.manualConvert(query)
	case "/!webvideo":
		t.webVideo(query)
	case "/!player":
		t.sendInfo(t.videoPlayerPage(query))
	case "/!clear":
		n := 0
		files, _ := filepath.Glob(filepath.Join(t.cfg.TemporaryDirectory, "convert-*.*"))
		for _, f := range files {
			if os.Remove(f) == nil {
				n++
			}
		}
		t.sendError(200, "<b>"+strconv.Itoa(n)+"</b> temporary files have been deleted in <i>"+t.cfg.TemporaryDirectory+"</i>.")
	case "/!ftp":
		t.sendInfo(t.ftpPage())
	case "/!redirect":
		t.sendInfo(t.redirectorPage(query))
	case "/!ca", "/weboneca.crt", "/weboneca.cer", "/weboneca.der":
		t.log.Printf("<Return WebOne CA (root) certificate.")
		if !t.cfg.SslEnable || t.s.RootCA == nil {
			t.sendError(404, "SSL and TLS are disabled. Use <i>http://</i> URL protocol to access any <i>https://</i> websites.")
			return
		}
		t.resp.AddHeader("Content-Disposition", `attachment; filename="WebOneCA.crt"`)
		t.sendBytes(200, "application/x-x509-ca-cert", t.s.RootCA.DER())
	case "/weboneca.pem", "/weboneca.txt":
		t.log.Printf("<Return WebOne CA (root) certificate in PEM format.")
		if !t.cfg.SslEnable || t.s.RootCA == nil {
			t.sendError(404, "SSL and TLS are disabled. Use <i>http://</i> URL protocol to access any <i>https://</i> websites.")
			return
		}
		p := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: t.s.RootCA.DER()})
		t.resp.AddHeader("Content-Disposition", `attachment; filename="WebOneCA.crt"`)
		t.sendBytes(200, "application/x-x509-ca-cert", p)
	case "/!pac", "/auto", "/auto.pac", "/wpad.dat", "/wpad.da":
		t.log.Printf("<Return PAC/WPAD script.")
		host := t.serverName()
		if strings.HasPrefix(host, "[") {
			host = t.cfg.DefaultHostName + ":" + strconv.Itoa(t.cfg.Port)
		}
		t.sendBytes(200, "application/x-ns-proxy-autoconfig", []byte(t.s.pac(host)))
	case "/robots.txt":
		t.log.Printf("<Return robot kicker.")
		if t.sendInternalContent("robots.txt", "", 200) {
			return
		}
		t.sendBytes(200, "text/plain", []byte("User-agent: *\nDisallow: / "))
	default:
		if page == "/rovp.htm" && !t.videoEnabled() {
			t.sendRedirect("/norovp.htm", "ROVP is disabled on this server.")
			return
		}
		if t.notModified(page, t.req.Header.Get("If-Modified-Since")) {
			t.resp.Status = 304
			t.resp.SetContentLength(0)
			t.resp.SendHeaders(Version)
			return
		}
		if t.sendInternalContent(page, q, 200) {
			return
		}
		msg := "<p>The page you are viewing does not exist.</p>" +
			"<p>If you think we brought you here on purpose by posting a wrong link, send us that link via GitHub.</p>" +
			"<p>And if you really want to find something on the Internet, specify IP of the WebOne server in your browser's proxy server settings.</p>" +
			"<pre>" + page + "</pre>"
		t.sendInfoPage("WebOne: 404", "404 - there's no page.", msg, 404)
	}
}

func (t *transit) sendCodepages() {
	var b strings.Builder
	b.WriteString("<p>The following code pages are available: <br>\n<table><tr><td><b>Name</b></td><td><b>#</b></td><td><b>Description</b></td></tr>\n")
	b.WriteString("<tr><td><b>AsIs</b></td><td>0</td><td>Keep original encoding (code page)</td></tr>\n")
	listed := false
	for _, e := range textenc.All() {
		fmt.Fprintf(&b, "<tr><td><b>%s</b></td><td>%d</td><td>%s", e.Name, e.CodePage, e.Display)
		if t.cfg.OutputEncoding != nil && e.CodePage == t.cfg.OutputEncoding.CodePage {
			b.WriteString(" <b>(Current)</b>")
			listed = true
		}
		b.WriteString("</td></tr>\n")
	}
	b.WriteString("</table><br>Use any of these names or numbers.</p>\n")
	name := func(cp string) string {
		if e := t.cfg.CodePage(cp); e != nil {
			return e.Name
		}
		return "?"
	}
	b.WriteString("<p>Code pages for current server's locale:\n<table>" +
		"<tr><td>Windows &quot;ANSI&quot;</td><td>" + name("Win") + "</td></tr>\n" +
		"<tr><td>DOS &quot;OEM&quot;</td><td>" + name("DOS") + "</td></tr>\n" +
		"<tr><td>MacOS classic</td><td>" + name("Mac") + "</td></tr>\n" +
		"<tr><td>ISO</td><td>" + name("ISO") + "</td></tr>\n" +
		"<tr><td>EBCDIC</td><td>" + name("EBCDIC") + "</td></tr>\n" +
		"</table>Clients without UTF-8 support will got content in these code pages.</p>\n")
	if t.cfg.OutputEncoding == nil {
		b.WriteString("<br>Current output encoding: <b>same as source</b>.\n")
	} else if !listed {
		fmt.Fprintf(&b, "<br>Current output encoding: <b>%s</b> &quot;%s&quot; (# %d).\n", t.cfg.OutputEncoding.Name, t.cfg.OutputEncoding.Display, t.cfg.OutputEncoding.CodePage)
	}
	t.sendInfoPage("WebOne: List of supported code pages", "Content encodings", b.String(), 200)
}

// sendStatusPage is the proxy's home page (HttpTransit.SendInternalStatusPage).
func (t *transit) sendStatusPage() {
	var h strings.Builder
	opened, busy := t.s.opened.Load(), t.s.busy.Load()
	switch t.cfg.DisplayStatusPage {
	case "no":
		t.sendInfoPage("WebOne status page", "Sorry", "<p>The status page is disabled by server administrator.</p>", 200)
		return
	case "short":
		fmt.Fprintf(&h, "<p>This is <b>%s</b>.<br>", t.serverName())
		fmt.Fprintf(&h, "Connections open: <b>%d</b> (<b>%d</b> active).<br>", opened, busy)
		fmt.Fprintf(&h, "Used memory: <b>%d</b> MB.<br>", usedMemoryMB())
		h.WriteString(`About: <a href="https://github.com/atauenis/webone/">https://github.com/atauenis/webone/</a> (Go port: <a href="https://github.com/D-Modem/webone-go">https://github.com/D-Modem/webone-go</a>)</p>`)
		fmt.Fprintf(&h, "<p>Client IP: <b>%s</b>.</p>", t.req.Remote)
		h.WriteString("<h2>May be useful:</h2><ul>")
		h.WriteString(`<li><a href="/auto.pac">Proxy auto-configuration file</a>: /!pac/, /auto/, /auto, /auto.pac, /wpad.dat.</li>`)
		h.WriteString(`<li><a href="/!ca">WebOne CA root certificate</a>.</li>`)
		h.WriteString(`<li><a href="/!ftp/">Web-based FTP client</a>.</li>`)
		h.WriteString(`<li><a href="/!player/">Online video player</a>.</li>`)
		h.WriteString("</ul>")
	case "full":
		host, _ := os.Hostname()
		fmt.Fprintf(&h, "This is <b>%s:%d</b>.<br>", host, t.cfg.Port)
		fmt.Fprintf(&h, "Used memory: <b>%d</b> MB.<br>", usedMemoryMB())
		fmt.Fprintf(&h, "Connections open: <b>%d</b> (<b>%d</b> active).<br>", opened, busy)
		h.WriteString("<h2>Aliases:</h2><ul>")
		evident := false
		for _, ip := range config.LocalIPs() {
			a := ip.String()
			if a == t.cfg.DefaultHostName {
				evident = true
				a = "<b>" + a + "</b>"
			}
			fmt.Fprintf(&h, "<li>%s:%d</li>", a, t.cfg.Port)
		}
		if !evident {
			fmt.Fprintf(&h, "<li><b>%s</b>:%d</li>", t.cfg.DefaultHostName, t.cfg.Port)
		}
		h.WriteString("</ul>")
		fmt.Fprintf(&h, "<p>Client IP: <b>%s</b>.</p>", t.req.Remote)
		h.WriteString("<h2>Internal URLs:</h2><ul>" +
			`<li><a href="/!codepages/">/!codepages/</a> - list of available encodings for OutputEncoding setting</li>` +
			`<li><a href="/!img-test/">/!img-test/</a> - test if ImageMagick is working</li>` +
			`<li><a href="/!convert/">/!convert/</a> - run a file format converter (<a href="/!convert/?src=logo.webp&dest=gif&type=image/gif">demo</a>)</li>` +
			`<li><a href="/!clear/">/!clear/</a> - remove temporary files in WebOne working directory</li>` +
			`<li><a href="/auto.pac">Proxy auto-configuration file</a>: /!pac/, /auto/, /auto, /auto.pac, /wpad.dat.</li>` +
			`<li><a href="/!ca">/!ca/</a> - WebOne CA root certificate.</li>` +
			`<li><a href="/!ftp/">/!ftp/</a> - Web-based FTP client.</li>` +
			`<li><a href="/!player/">/!player/</a> - online video player.</li>` +
			`<li><a href="/!webvideo/">/!webvideo/</a> - online video downloader.</li>` +
			"</ul>")
	default:
		if t.sendInternalContent(t.cfg.DisplayStatusPage, "", 200) {
			return
		}
		h.WriteString("<h2>It works!</h2>")
	}
	h.WriteString("<h2>Headers sent by browser</h2><ul>")
	fmt.Fprintf(&h, "<li><b>%s %s HTTP/%s</b></li>", t.req.Method, t.req.RawURL, t.req.ProtoVersion())
	t.req.Header.Each(func(k, v string) { fmt.Fprintf(&h, "<li>%s: %s</li>", k, v) })
	h.WriteString("</ul>")
	t.sendInfoPage("WebOne status page", "", h.String(), 200)
}

// osDescription is %WOSystem%.
func osDescription() string {
	return runtime.GOOS + " " + runtime.GOARCH + " (" + runtime.Version() + ")"
}
