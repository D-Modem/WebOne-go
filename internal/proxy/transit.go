package proxy

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/D-Modem/webone-go/internal/config"
	"github.com/D-Modem/webone-go/internal/logx"
	"github.com/D-Modem/webone-go/internal/rx"
	"github.com/D-Modem/webone-go/internal/textenc"
)

const noContentType = "webone/unknown-content-type"

// transit is one request's trip through the proxy (HttpTransit.cs).
type transit struct {
	s    *Server
	cfg  *config.Config
	req  *Request
	resp *Response
	log  *logx.Writer

	vars     *config.Vars
	url      *url.URL // RequestURL
	editSets []*config.EditSet
	stop     bool // a converter has answered already
	op       *operation

	requestHTTPVersion string
	respMajor          int
	respMinor          int
	responseCode       int
	responseBody       string
	transitStream      io.ReadCloser
	transitLength      int64
	contentType        string
	sourceEncoding     *textenc.Encoding
	outputEncoding     *textenc.Encoding
	translit           bool
	dumpFile           string
	localIP            string

	// hijacked: the connection was taken over (a CONNECT tunnel) and is
	// not to be closed or reused by the caller
	hijacked bool
}

func newTransit(s *Server, req *Request, resp *Response, log *logx.Writer) *transit {
	return &transit{
		s: s, cfg: s.cfg, req: req, resp: resp, log: log,
		vars:               config.NewVars(),
		requestHTTPVersion: s.cfg.RemoteHttpVersion,
		respMajor:          1, respMinor: 1,
		responseCode:   502,
		responseBody:   ":(",
		contentType:    noContentType,
		sourceEncoding: textenc.UTF8,
		outputEncoding: s.cfg.OutputEncoding,
		transitLength:  -1,
	}
}

func (t *transit) urlString() string { return absURL(t.url) }

// clientID is "user, address" (or the address) for the log.
func (t *transit) clientID() string {
	id := t.req.Remote.IP.String()
	if pa := t.req.Header.Get("Proxy-Authorization"); strings.HasPrefix(pa, "Basic ") {
		if b, err := base64.StdEncoding.DecodeString(pa[6:]); err == nil {
			if user, _, ok := strings.Cut(string(b), ":"); ok {
				id = user + ", " + id
			}
		}
	}
	return id
}

func (t *transit) serverName() string { return t.s.serverName(t.localIP) }

func (t *transit) processURIMasks(masked string) string {
	return t.s.processURIMasks(masked, t.urlString(), false, t.vars)
}

// process handles the request (HttpTransit.ProcessTransit).
func (t *transit) process() {
	defer func() {
		if r := recover(); r != nil {
			t.log.Printf(" A error has been catched: %v\n\t Please report to author.", r)
			t.sendError(500, fmt.Sprintf("An error occured: %v", r))
		}
	}()
	t.respMajor, t.respMinor = t.req.Major, t.req.Minor
	req := t.req
	remote := req.Remote.String()

	// IP black and white lists
	if containsAny(remote, t.cfg.IpBanList) {
		t.log.Printf(" Banned client.")
		t.resp.KeepAlive = false
		t.resp.Close(Version)
		return
	}
	if len(t.cfg.IpWhiteList) > 0 && !containsAny(remote, t.cfg.IpWhiteList) {
		if t.sendInternalContent("Err-IpNotWhitelisted.htm", "?IP="+req.Remote.IP.String(), 200) {
			return
		}
		t.sendError(403, "You are not in the list of allowed clients. Contact proxy server's administrator to add your IP address in it.")
		t.log.Printf(" Non-whitelisted client.")
		return
	}

	// proxy login
	if len(t.cfg.Authenticate) > 0 && !req.Secure {
		path := "/"
		if req.URL != nil {
			path = pathAndQuery(req.URL)
		}
		switch path {
		case "/!pac/", "/auto/", "/auto", "/auto.pac", "/wpad.dat", "/wpad.da":
			// the PAC script is always open
		default:
			if t.cfg.OpenForLocalIPs && isLanIP(req.Remote.IP) {
				t.log.Printf(" Bypassed authorization of local client.")
				break
			}
			pa := req.Header.Get("Proxy-Authorization")
			if pa == "" {
				t.log.Printf(" Unauthorized client.")
				t.resp.AddHeader("Proxy-Authenticate", `Basic realm="`+t.cfg.AuthenticateRealm+`"`)
				t.sendError(407, t.cfg.AuthenticateMessage)
				return
			}
			auth := ""
			if len(pa) > 6 {
				if b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(pa[6:])); err == nil {
					auth = string(b)
				}
			}
			ok := false
			for _, a := range t.cfg.Authenticate {
				if a == auth {
					ok = true
				}
			}
			if !ok {
				t.log.Printf(" Incorrect login: '%s'.", auth)
				t.resp.AddHeader("Proxy-Authenticate", `Basic realm="`+t.cfg.AuthenticateRealm+` (retry)"`)
				t.sendError(407, t.cfg.AuthenticateMessage+"<p>Your login or password is not correct. Please try again.</p>")
				return
			}
			user, _, _ := strings.Cut(auth, ":")
			t.vars.Set("ProxyUserName", user)
		}
	}

	// HTTPS proxy (CONNECT)
	if strings.EqualFold(req.Method, "CONNECT") {
		t.connect()
		return
	}

	t.url = req.URL
	referer := req.Header.Get("Referer")

	if containsAny(absURL(t.url), t.cfg.UrlBlackList) {
		t.log.Printf(" Blacklisted URL.")
		if t.sendInternalContent("Err-UrlBlacklisted.htm", "?URL="+qe(t.urlString()), 403) {
			return
		}
		t.sendError(403, "Access to this web page is disallowed by proxy settings.")
		return
	}

	if ua := req.Header.Get("User-Agent"); strings.TrimSpace(ua) != "" && rx.AnyMatches(ua, t.cfg.Http10Only) {
		t.resp.SimpleContentType = true
	}
	t.resp.SetVersion(t.respMajor, t.respMinor)

	// the proxy's own address, as the client reached it
	if ip4 := req.Local.IP.To4(); ip4 != nil {
		t.localIP = ip4.String()
	} else {
		t.localIP = "[" + req.Local.IP.String() + "]"
	}
	if t.localIP == "127.0.0.1" && req.URL != nil && req.URL.Hostname() == "10.0.2.2" {
		t.localIP = "10.0.2.2" // SLIRP
	}

	// the proxy's own pages
	if req.Kind == StandardHttp && !strings.Contains(t.url.Path, ":") {
		page := "/"
		segs := segments(t.url)
		for _, sg := range segs[1:] {
			page += sg
		}
		page = strings.ToLower(strings.TrimRight(page, "/"))
		t.sendInternalPage(page, t.url.RawQuery)
		return
	}
	if req.Kind == AlternateProxy {
		t.log.Printf(" Alternate: %s", t.urlString())
	}

	if strings.Contains(req.Header.Get("User-Agent"), "WebOne") {
		if t.sendInternalContent("Err-LoopRequest.htm", "", 403) {
			return
		}
		t.sendError(403, "Loop requests are probhited.")
		return
	}

	// FTP, Gopher, WAIS ... through HTTP (CERN proxy mode)
	if strings.Contains(t.url.String(), ":") {
		scheme := t.url.Scheme
		if scheme != "" && !strings.HasPrefix(scheme, "http") {
			t.log.Printf(" CERN Proxy request to %s detected.", strings.ToUpper(scheme))
		}
		if req.Kind != StandardProxy && strings.Contains(req.RawURL, "://") {
			scheme = req.RawURL[1:strings.Index(req.RawURL, "://")]
		}
		if !containsAny(scheme, []string{"http", "https", "ftp"}) {
			raw := strings.TrimLeft(req.RawURL, "/")
			if t.sendInternalContent("Err-UnknownProtocol.htm", "?Scheme="+strings.TrimRight(strings.ToUpper(scheme), ":")+"&URL="+qe(raw), 200) {
				return
			}
			msg := "<p>You're attempted to request content from <i>" + req.RawURL + "</i>. " +
				"The protocol specified in the URL is not supported by this proxy server.</p>" +
				"<p>Consider connect directly to the server, bypassing the proxy. This error message may also appear if your Web browser settings have enabled " +
				"<b>&quot;Use proxy for all protocols&quot;</b> option. Uncheck it and set only for protocols supported by WebOne. List of them can be found in project's Wiki.</p>"
			t.sendInfoPage(strings.ToUpper(t.url.Scheme)+" Is Not Supported", "Unsupported protocol", msg, 501)
			return
		}
	}
	lowerRaw := strings.ToLower(req.RawURL)
	ftpRedirect := func(title, target string) {
		p := newInfoPage(title, "", "", 302)
		p.Headers.Add("Location", "http://"+t.serverName()+"/!ftp/?client=-1&uri="+url.QueryEscape(target))
		t.sendInfo(p)
	}
	switch {
	case strings.HasPrefix(lowerRaw, "ftp://"):
		ftpRedirect("CERN Proxy Emulation Redirect", req.RawURL)
		return
	case strings.HasPrefix(lowerRaw, "http://ftp:"):
		ftpRedirect("CERN Proxy Emulation Redirect (NS)", req.RawURL[7:])
		return
	case strings.HasPrefix(lowerRaw, "http://ftp//"):
		ftpRedirect("CERN Proxy Emulation Redirect (IE)", "ftp://"+req.RawURL[12:])
		return
	case strings.HasPrefix(lowerRaw, "/ftp://"):
		ftpRedirect("CERN Proxy Emulation Redirect (Alt)", req.RawURL[1:])
		return
	}

	// HTTP<->HTTPS redirect loops ("carousels")
	cur := t.urlString()
	t.s.lastMu.Lock()
	lastURL, lastCode, lastCT := t.s.lastURL, t.s.lastCode, t.s.lastContentType
	t.s.lastMu.Unlock()
	host, _ := os.Hostname()
	if (cur == referer || cur == lastURL) && cur != "" &&
		(strings.HasPrefix(lastCT, "text/htm") || lastCT == "") &&
		((lastCode > 299 && lastCode < 400) || lastCode == 200) &&
		req.Method != "POST" && req.Method != "CONNECT" &&
		!t.s.isForceHttps(cur) &&
		!strings.EqualFold(t.url.Hostname(), host) &&
		!strings.EqualFold(t.url.Hostname(), t.localIP) &&
		!strings.EqualFold(t.url.Hostname(), t.cfg.DefaultHostName) {
		t.log.Printf(" Carousel detected.")
		if !strings.HasPrefix(lastURL, "https") && !strings.HasPrefix(cur, "https") {
			t.url, _ = parseURL("https" + cur[4:])
		}
		if strings.HasPrefix(lastURL, "https") {
			if strings.HasPrefix(cur, "https") {
				t.url, _ = parseURL("http" + cur[5:])
			} else {
				t.url, _ = parseURL("http" + cur[4:])
			}
		}
	}

	// a secure referer for a secure page of the same site
	if referer != "" {
		if ru, err := url.Parse(referer); err == nil && ru.Hostname() == t.url.Hostname() {
			if t.url.Scheme == "https" && !strings.HasPrefix(referer, "https://") && len(referer) > 4 {
				referer = "https" + referer[4:]
			}
		}
	}

	t.s.lastMu.Lock()
	t.s.lastURL = t.urlString()
	t.s.lastContentType = "unknown/unknown"
	t.s.lastCode = 200
	t.s.lastMu.Unlock()

	cur = t.urlString()
	if strings.Contains(cur, "??") {
		t.sendError(400, "Too many questions.")
		return
	}
	if cur == "" {
		t.sendError(400, "Empty URL.")
		return
	}

	// pick the edit sets for this request
	for _, set := range t.cfg.EditRules {
		if len(t.cfg.Authenticate) > 0 && len(set.Users) > 0 {
			valid := false
			for _, u := range set.Users {
				if t.vars.Value("ProxyUserName") == u {
					valid = true
				}
			}
			if !valid {
				continue
			}
		}
		if !set.CorrectHostOS {
			continue
		}
		if !rx.AnyMatches(cur, set.UrlMasks) || rx.AnyMatches(cur, set.UrlIgnoreMasks) {
			continue
		}
		if set.HttpOnly && req.Secure {
			continue
		}
		if set.HttpsOnly && !req.Secure {
			continue
		}
		if len(set.HeaderMasks) > 0 {
			good := false
			for _, m := range set.HeaderMasks {
				req.Header.Each(func(k, v string) {
					if !good && rx.Match(m, k+": "+v) {
						good = true
					}
				})
				if good {
					break
				}
			}
			if good {
				t.editSets = append(t.editSets, set)
			}
			continue
		}
		t.editSets = append(t.editSets, set)
	}

	if len(t.cfg.UrlWhiteList) > 0 && !containsAny(t.url.String(), t.cfg.UrlWhiteList) {
		if t.sendInternalContent("Err-UrlNotWhitelisted.htm", "?URL="+qe(cur), 403) {
			return
		}
		t.sendError(403, "Proxy server administrator has been limited this proxy server to work with several web sites only.")
		t.log.Printf(" URL out of white list.")
		return
	}

	if err := t.forward(referer); err != nil {
		if t.handleFetchError(err) {
			return
		}
	}
	if t.resp.HeadersSent {
		return
	}

	// look in the Web Archive on errors
	if t.responseCode >= 403 && t.cfg.SearchInArchive && req.Method != "POST" && req.Method != "PUT" && !t.stop {
		t.lookInWebArchive()
		if t.resp.HeadersSent {
			return
		}
	}

	// shorten the Wayback Machine's error pages
	if t.responseCode >= 403 && strings.HasPrefix(t.urlString(), "http://web.archive.org/web/") && t.cfg.ShortenArchiveErrors {
		t.log.Printf(" Wayback Machine error page shortened.")
		if t.sendInternalContent("Err-WA"+strconv.Itoa(t.responseCode)+".htm",
			"?ErrorMessage="+StatusName(t.responseCode)+"&URL="+qe(t.urlString()), t.responseCode) {
			return
		}
		switch t.responseCode {
		case 404:
			t.sendError(404, "<p><b>The Wayback Machine has not archived that URL.</b></p><p>Try to slightly change the URL.</p>"+
				"<small><i>You see this message because ShortenArchiveErrors option is enabled.</i></small>")
		case 403:
			t.sendError(404, "<p><b>This URL has been excluded from the Wayback Machine.</b></p><p>This page is not present in Web Archive.</p>"+
				"<small><i>You see this message because ShortenArchiveErrors option is enabled.</i></small>")
		default:
			t.sendError(404, "<p><b>The Wayback Machine cannot give this page.</b></p><p>This is reason: "+StatusName(t.responseCode)+".</p>"+
				"<small><i>You see this message because ShortenArchiveErrors option is enabled.</i></small>")
		}
		return
	}

	t.sendResult()
}

// sendResult sends the fetched (and edited) response to the client.
func (t *transit) sendResult() {
	defer func() {
		if t.transitStream != nil {
			t.transitStream.Close()
		}
	}()
	w := t.resp
	w.SetVersion(t.respMajor, t.respMinor)
	w.Status = t.responseCode
	w.AddHeader("Via", "HTTP/1.0 WebOne/"+t.cfg.Vars.Value("WOVer"))
	if !w.Header.Has("Content-Type") && t.contentType != "" {
		w.AddHeader("Content-Type", t.contentType)
	}
	if t.req.KeepAlive {
		w.AddHeader("Proxy-Connection", "keep-alive")
	}
	if t.transitStream == nil {
		enc := t.outputEncoding
		if enc == nil {
			enc = t.sourceEncoding
		}
		buf := enc.Encode(t.responseBody)
		w.SetContentLength(int64(len(buf)))
		if len(buf) > 300*1024 {
			t.log.Printf(" Sending binary.")
		}
		if err := w.SendHeaders(Version); err != nil {
			t.clientError(err)
			return
		}
		if t.req.Method != "HEAD" {
			w.Write(buf)
		}
		if t.dumpFile != "" {
			w.Header.Each(func(k, v string) { t.dump(k + ": " + v) })
			t.dump("\n")
			if len(buf) < 1024 {
				t.dump(t.responseBody)
			} else {
				t.dump("Over 1 KB response body")
			}
		}
	} else {
		if w.ContentLength() == -2 {
			w.SetContentLength(t.transitLength)
		}
		if err := w.SendHeaders(Version); err != nil {
			t.clientError(err)
			return
		}
		if _, err := io.Copy(w, t.transitStream); err != nil {
			t.clientError(err)
			w.KeepAlive = false
			return
		}
		if t.dumpFile != "" {
			w.Header.Each(func(k, v string) { t.dump(k + ": " + v) })
			t.dump("\nBody is binary stream")
		}
	}
}

func (t *transit) clientError(err error) {
	if !t.cfg.HideClientErrors {
		t.log.Printf("<Can't return reply. %v", err)
	}
}

// forward prepares the request for the server and sends it (the part of
// ProcessTransit in its inner try block).
func (t *transit) forward(referer string) error {
	req := t.req
	h := NewHeader()
	toHTTPS := t.url.Scheme == "https" || containsAny(t.url.Hostname(), t.s.forceHttpsList())
	req.Header.Each(func(k, v string) {
		if toHTTPS {
			v = strings.ReplaceAll(v, "http://", "https://")
		}
		h.Add(k, v)
	})
	if !h.Has("Origin") && h.Has("Referer") {
		if ru, err := url.Parse(h.Get("Referer")); err == nil && ru.Scheme != "" {
			h.Add("Origin", ru.Scheme+"://"+ru.Host)
		}
	}
	if h.Has("User-Agent") {
		h.Set("User-Agent", t.s.userAgent(h.Get("User-Agent")))
	}

	// request-time edits
	for _, set := range t.editSets {
		if !set.IsForRequest {
			continue
		}
		// a single URL mask may give its groups to the redirect target
		useRegEx := len(set.UrlMasks) == 1 && rx.GroupCount(set.UrlMasks[0]) > 1
		for _, e := range set.Edits {
			switch e.Action {
			case config.AddVariable:
				t.addVariable(e.Params)
			case config.AddHeaderDumping, config.AddRequestDumping, config.AddDumping:
				f := strings.NewReplacer(":", "-", "<", "(", ">", ")", "?", "-", "|", "!").Replace(t.processURIMasks(e.Value))
				if len(f) > 128 {
					f = f[:128] + "-CUT.log"
				}
				t.dumpFile = f
				t.dump(req.Method + " " + req.RawURL + " HTTP/" + req.ProtoVersion())
			case config.AddInternalRedirect:
				var target string
				if useRegEx {
					target = t.processURIMasks(rx.Replace(set.UrlMasks[0], t.urlString(), e.Value, rx.None))
				} else {
					target = t.processURIMasks(e.Value)
				}
				t.log.Printf(" Fix to %s internally", target)
				t.dump("~Internal redirect to: " + target)
				nu, err := parseURL(target)
				if err != nil {
					return &badURLError{target}
				}
				t.url = nu
			case config.AddRedirect:
				var target string
				if useRegEx {
					if g := rx.FindGroups(set.UrlMasks[0], t.urlString()); g != nil {
						target = t.processURIMasks(rx.Replace(set.UrlMasks[0], g[0], e.Value, rx.None))
					} else {
						target = t.processURIMasks(e.Value)
					}
				} else {
					target = t.processURIMasks(e.Value)
				}
				t.log.Printf(" Fix to %s", target)
				t.dump("~Redirect using 302 to: " + target)
				t.sendRedirect(target, "Брось каку!")
				t.stop = true
				return nil
			case config.AddRequestHeader, config.AddHeader:
				hdr := t.processURIMasks(e.Value)
				t.dump("~Add request header: " + hdr)
				name := e.Value[:strings.Index(e.Value, ": ")]
				h.Del(name)
				h.AddLine(hdr)
			case config.AddRequestHeaderFindReplace:
				h.Map(func(_, v string) string { return strings.ReplaceAll(v, e.Find, e.Replace) })
				t.dump("~Request header find&replace: '" + e.Find + "' / '" + e.Replace + "'")
			case config.AddRequestHttpVersion:
				t.dump("~Using HTTP(S) " + e.Value + " for remote connection")
				t.requestHTTPVersion = e.Value
			case config.AddResponseHttpVersion:
				t.dump("~Reply as HTTP/" + e.Value)
				t.respMajor, t.respMinor, _ = config.ParseVersion(e.Value)
			case config.AddOutputEncoding:
				t.outputEncoding = t.cfg.CodePage(e.Value)
				name := "AsIs"
				if t.outputEncoding != nil {
					name = t.outputEncoding.Name
				}
				t.dump("~Output encoding set to: " + name)
			case config.AddTranslit:
				t.translit = config.ToBool(e.Value)
				t.dump("~Enable transliteration")
			case config.AddDebugPrint:
				t.dump("~" + e.Value + " (at request processing)")
				t.log.Printf(" [Request processing] %s", t.processURIMasks(e.Value))
			}
		}
	}
	h.Each(func(k, v string) {
		t.vars.TryAdd("Request."+k, v)
		t.dump(k + ": " + v)
	})
	t.dump("")

	// Microsoft Internet Component Download emulation
	if t.cfg.ActivexGalleryEmul && containsAny(t.url.String(), t.cfg.ActivexGalleryUrls) {
		if r := t.activexCabURL(); strings.TrimSpace(r) != "" {
			if r == "404" {
				t.sendError(404, "The specified Internet Component is known and is up to date.")
			} else {
				t.sendRedirect(r, "Nate entu vashu codebase, zhrite.")
			}
			t.stop = true
			return nil
		}
	}

	t.op = &operation{
		Method:      req.Method,
		Header:      h,
		URL:         t.url,
		HTTPVersion: t.requestHTTPVersion,
		log:         t.log,
	}
	if !t.cfg.DontPreferHTTPS {
		t.op.Secure = req.Secure
	}
	return t.sendRequest()
}

func (s *Server) forceHttpsList() []string {
	s.forceMu.RLock()
	defer s.forceMu.RUnlock()
	return append([]string(nil), s.forceHttps...)
}

type badURLError struct{ u string }

func (e *badURLError) Error() string { return "invalid URL " + e.u }

// sendRequest sends t.op to the server and makes the output
// (HttpTransit.SendRequest).
func (t *transit) sendRequest() error {
	op := t.op
	cl := t.req.Header.Get("Content-Length")
	op.URL = t.url
	if cl == "" || cl == "0" {
		t.log.Printf(">Downloading content...")
	} else {
		n, _ := strconv.Atoi(cl)
		t.log.Printf(">Uploading %dK of %s...", n/1024, op.Header.Get("Content-Type"))
		if t.dumpFile == "" {
			op.Body = t.req.Body
		} else {
			data, _ := io.ReadAll(t.req.Body)
			op.Body = bytes.NewReader(data)
			if len(data) > 0 {
				if len(data) < 1024 {
					t.dump("\nCAUTION: Request body may contain private data!\n" + string(data))
				} else {
					t.dump("\nRequest body is longer than 1 KB")
				}
			} else {
				t.dump("\n")
			}
		}
	}
	if err := t.s.send(op); err != nil {
		return err
	}
	if op.URL.Scheme != t.url.Scheme {
		t.url = op.URL
	}
	t.makeOutput()
	if t.stop {
		return nil
	}
	t.responseCode = op.Resp.StatusCode

	// an HTTP site that moved to HTTPS
	if t.responseCode >= 301 && t.responseCode <= 399 {
		if loc := op.RespHeader.Get("Location"); loc != "" {
			if t.urlString() == strings.Replace(loc, "https://", "http://", 1) {
				t.log.Printf(">Reload secure...")
				t.dump("Upgrade HTTP to HTTPS")
				nu, err := parseURL(strings.Replace(t.urlString(), "http://", "https://", 1))
				if err == nil {
					if t.transitStream != nil {
						t.transitStream.Close()
						t.transitStream = nil
					}
					h := t.req.Header.Clone()
					t.url = nu
					t.op = &operation{Method: op.Method, Header: h, URL: nu, HTTPVersion: t.requestHTTPVersion, log: t.log}
					err := t.sendRequest()
					t.s.addForceHttps(nu.Hostname())
					return err
				}
			}
			if strings.HasPrefix(loc, "https://") {
				t.s.addForceHttps(t.url.Hostname())
			}
		}
	}

	// the server's response headers, made fit for the client
	op.RespHeader.Each(func(k, v string) {
		cv := v
		if !t.req.Secure {
			cv = strings.ReplaceAll(cv, "https://", "http://")
			cv = rx.Replace("; secure", cv, ";", rx.IgnoreCase)
		}
		if t.req.Kind == AlternateProxy || t.req.Kind == DirtyAlternateProxy {
			sn := t.serverName()
			if strings.HasPrefix(cv, "http://") && !strings.HasPrefix(cv, "http://"+sn) {
				cv = strings.Replace(cv, "http://", "http://"+sn+"/http://", 1)
			}
			cv = rx.Replace("domain=[^;]*; ", cv, " ", rx.IgnoreCase)
			cv = rx.Replace("path=[^;]*; ", cv, " ", rx.IgnoreCase)
		}
		if !strings.HasPrefix(k, "Content-") &&
			!strings.HasPrefix(k, "Connection") &&
			!strings.HasPrefix(k, "Transfer-Encoding") &&
			!strings.HasPrefix(k, "Keep-Alive") &&
			!strings.HasPrefix(k, "Access-Control-Allow-Methods") &&
			!strings.HasPrefix(k, "Strict-Transport-Security") &&
			!strings.HasPrefix(k, "Content-Security-Policy") &&
			!strings.HasPrefix(k, "Upgrade-Insecure-Requests") &&
			!strings.HasPrefix(k, "Alt-Svc") &&
			!(strings.HasPrefix(k, "Vary") && strings.Contains(cv, "Upgrade-Insecure-Requests")) {
			t.resp.Header.Add(k, cv)
		}
		if k == "Content-Length" && t.transitStream != nil {
			if n, err := strconv.ParseInt(cv, 10, 64); err == nil {
				t.resp.SetContentLength(n)
			}
		}
	})
	return nil
}

// makeOutput looks at the server's response: edits for response headers,
// redirects, converters; text is read and edited, anything else streamed
// (HttpTransit.MakeOutput).
func (t *transit) makeOutput() {
	op := t.op
	code := op.Resp.StatusCode
	ct := op.RespHeader.Get("Content-Type")
	if ct == "" {
		ct = noContentType
	}
	lengthKB := "?"
	if op.Resp.ContentLength >= 0 {
		lengthKB = strconv.FormatInt(op.Resp.ContentLength/1024, 10)
	}
	t.contentType = ct
	srcCT := ct
	t.dump(fmt.Sprintf("\n\n%d HTTP/1.0", code))
	t.vars.TryAdd("Response.HttpStatusCode", strconv.Itoa(code))
	t.vars.TryAdd("Response.HttpVersion", fmt.Sprintf("%d.%d", op.Resp.ProtoMajor, op.Resp.ProtoMinor))
	op.RespHeader.Each(func(k, v string) { t.vars.TryAdd("Response."+k, v) })

	var converter, dest, arg1, arg2, redirect string
	for _, set := range t.editSets {
		if len(set.ContentTypeMasks) > 0 && !rx.AnyMatches(ct, set.ContentTypeMasks) {
			continue
		}
		if !config.CheckCode(set.OnCode, code) {
			continue
		}
		for _, e := range set.Edits {
			switch e.Action {
			case config.AddVariable:
				t.addVariable(e.Params)
			case config.AddConverting:
				converter, dest, arg1, arg2 = e.Converter, e.ConvertDest, e.ConvertArg1, e.ConvertArg2
				t.stop = true
				t.dump("~~Convert using: " + converter)
			case config.AddResponseHeader:
				hdr := t.processURIMasks(e.Value)
				t.log.Printf(" Add response header: %s", hdr)
				op.RespHeader.AddLine(hdr)
				if strings.HasPrefix(e.Value, "Content-Type: ") {
					ct = e.Value[len("Content-Type: "):]
				}
				t.dump("~~Add response header: " + hdr)
			case config.AddResponseHeaderFindReplace:
				op.RespHeader.Map(func(_, v string) string { return strings.ReplaceAll(v, e.Find, e.Replace) })
				t.dump("~~Response header find&replace: " + e.Find + " -> " + e.Replace)
			case config.AddRedirect:
				redirect = t.processURIMasks(e.Value)
				t.log.Printf(" Add redirect: %s", redirect)
				t.dump("~~Redirect to: " + redirect)
			case config.AddResponseHttpVersion:
				t.dump("~Reply as HTTP/" + e.Value)
				t.respMajor, t.respMinor, _ = config.ParseVersion(e.Value)
			case config.AddDebugPrint:
				t.dump("~" + e.Value + " (at response headers processing)")
				t.log.Printf("[Response headers processing] %s", t.processURIMasks(e.Value))
			}
		}
	}

	if redirect != "" {
		t.log.Printf(" %d %s. Body %sK of %s [Need to redirect].", code, StatusName(code), lengthKB, srcCT)
		op.RespBody.Close()
		t.sendRedirect(redirect, "Traffic has been edited.")
		t.stop = true
		return
	}
	if converter != "" {
		t.log.Printf(" %d %s. Body %sK of %s [Wants %s].", code, StatusName(code), lengthKB, srcCT, converter)
		t.convertResponse(converter, dest, arg1, arg2, ct, srcCT)
		return
	}

	t.s.lastMu.Lock()
	t.s.lastCode = code
	t.s.lastContentType = ct
	t.s.lastMu.Unlock()

	shown := ct
	if ct == noContentType {
		shown = "something"
	}
	if containsAny(ct, t.cfg.TextTypes) {
		t.log.Printf(" %d %s. Body %sK of %s [Text].", code, StatusName(code), lengthKB, shown)
		raw, err := io.ReadAll(op.RespBody)
		op.RespBody.Close()
		if err != nil && len(raw) == 0 {
			t.log.Printf(" Body read error: %v", err)
		}
		t.sourceEncoding = t.findContentCharset(raw)
		if t.outputEncoding == nil {
			t.responseBody = t.processBody(t.sourceEncoding.Decode(raw))
			t.contentType = ct
			return
		}
		forceUTF8 := strings.Contains(strings.ToLower(ct), "utf-8")
		if !forceUTF8 && len(raw) > 0 {
			forceUTF8 = t.sourceEncoding.IsUTF8()
		}
		for _, f := range t.cfg.ForceUtf8 {
			if rx.Match(f, t.urlString()) {
				forceUTF8 = true
			}
		}
		if forceUTF8 {
			t.responseBody = textenc.UTF8.Decode(raw)
		} else {
			t.responseBody = t.sourceEncoding.Decode(raw)
		}
		if rx.MatchOpt(`<meta.*UTF-8.*>`, t.responseBody, rx.IgnoreCase) {
			t.responseBody = textenc.UTF8.Decode(raw)
		}
		if strings.Contains(strings.ToLower(ct), "utf-8") {
			if i := strings.IndexByte(ct, ';'); i >= 0 {
				ct = ct[:i]
			}
		}
		t.contentType = ct
		t.responseBody = t.processBody(t.responseBody)
		return
	}
	t.log.Printf(" %d %s. Body %sK of %s [Binary].", code, StatusName(code), lengthKB, shown)
	t.transitStream = op.RespBody
	t.transitLength = op.Resp.ContentLength
	t.contentType = ct
}

// findContentCharset works out the code page of a text body: byte order
// mark, then the Content-Type charset, then a <meta http-equiv> charset,
// else the culture's Windows code page.
func (t *transit) findContentCharset(raw []byte) *textenc.Encoding {
	if len(raw) < 1 {
		return textenc.UTF8
	}
	switch raw[0] {
	case 0xEF:
		return textenc.UTF8
	case 0xFF:
		return textenc.UTF16LE
	case 0xFE:
		return textenc.UTF16BE
	}
	if ct := t.op.RespHeader.Get("Content-Type"); ct != "" {
		if g := rx.FindGroups(`; ?charset=(.*)`, ct); g != nil {
			name := g[1]
			if strings.EqualFold(name, "utf8") {
				name = "utf-8"
			}
			name = strings.NewReplacer(`"`, "", "'", "").Replace(name)
			if i := strings.IndexByte(name, ';'); i >= 0 {
				name = name[:i]
			}
			if e := textenc.Lookup(name); e != nil {
				return e
			}
		}
	}
	windows := textenc.FromCodePageOr(t.cfg.CodepageCulture.ANSI, textenc.ByCodePage(1252))
	content := textenc.UTF8.Decode(raw)
	meta := rx.FindGroups(`<meta http-equiv .*charset=.*>`, strings.ToLower(content))
	if meta == nil {
		return windows
	}
	cs := rx.FindGroups(`charset=.*['"]`, meta[0])
	if cs == nil {
		return windows
	}
	name := strings.ReplaceAll(cs[0][len("charset="):], `"`, "")
	if e := textenc.Lookup(name); e != nil {
		return e
	}
	return textenc.UTF8
}

// processBody edits a text body (HttpTransit.ProcessBody).
func (t *transit) processBody(body string) string {
	if !t.req.Secure {
		body = strings.ReplaceAll(body, "https", "http")
	}
	if out := t.outputEncoding; out != nil {
		n := out.Name
		body = strings.NewReplacer(
			`harset="utf-8"`, `harset="`+n+`"`,
			`harset="UTF-8"`, `harset="`+n+`"`,
			"harset=utf-8", "harset="+n,
			"harset=UTF-8", "harset="+n,
			"CHARSET=UTF-8", "CHARSET="+n,
			`ncoding="utf-8"`, `ncoding="`+n+`"`,
			`ncoding="UTF-8"`, `ncoding="`+n+`"`,
			"ncoding=utf-8", "ncoding="+n,
			"ncoding=UTF-8", "ncoding="+n,
			"ENCODING=UTF-8", "ENCODING="+n,
		).Replace(body)
		body = strings.ReplaceAll(body, "\uFEFF", "")
		body = strings.ReplaceAll(body, out.Decode([]byte{0xEF, 0xBB, 0xBF}), "")
		// what the code page has not becomes '?'
		body = out.Decode(out.Encode(body))
	}
	for _, set := range t.editSets {
		// (WebOne tests the masks against the content type the other way
		// round here, which misses "text/html; charset=..." for a mask of
		// "html"; this port tests as MakeOutput does)
		if len(set.ContentTypeMasks) > 0 && !rx.AnyMatches(t.contentType, set.ContentTypeMasks) {
			continue
		}
		if !config.CheckCode(set.OnCode, t.op.Resp.StatusCode) {
			continue
		}
		for _, e := range set.Edits {
			switch e.Action {
			case config.AddVariable:
				t.addVariable(e.Params)
			case config.AddFindReplace:
				body = rx.Replace(e.Find, body, e.Replace, rx.Singleline)
				t.dump("~~Find & replace (RegEx): " + e.Find + " -> " + e.Replace)
			case config.AddResponseHttpVersion:
				t.dump("~Reply as HTTP/" + e.Value)
				t.respMajor, t.respMinor, _ = config.ParseVersion(e.Value)
			case config.AddDebugPrint:
				t.dump("~" + e.Value + " (at response body processing)")
				t.log.Printf("[Response body processing] %s", t.processURIMasks(e.Value))
			}
		}
	}
	if t.translit {
		for _, l := range t.cfg.TranslitTable {
			body = strings.ReplaceAll(body, l[0], l[1])
		}
	}
	if t.req.Kind == AlternateProxy || t.req.Kind == DirtyAlternateProxy {
		body = t.alternateBody(body)
	}
	return body
}

// alternateBody points a page's links back through the proxy, for clients
// that use it as a web server (/http://site/page).
func (t *transit) alternateBody(body string) string {
	segs := segments(t.url)
	dir := "/"
	for i := 0; i < len(segs)-1; i++ {
		dir += segs[i]
	}
	host := t.url.Host
	steps := [][2]string{
		{`.replace(/"/g`, `.replace(/"WEBONEGOOGLEFIX1/g`},
		{`.replace(/'/g`, `.replace(/'WEBONEGOOGLEFIX2/g`},
		{`"http://`, `"@@@/http://`},
		{`"https://`, `"@@@/http://`},
		{`"ftp://`, `"@@@/ftp://`},
		{`"//`, `"@@@/http://`},
		{`"/`, `"/http://` + host + `/`},
		{`"./`, `"/http://` + host + dir},
		{`"@@@/http://`, `"/http://`},
		{`"@@@/ftp://`, `"/ftp://`},
		{`'http://`, `'@@@/http://`},
		{`'https://`, `'@@@/http://`},
		{`'ftp://`, `'@@@/ftp://`},
		{`'//`, `'@@@/http://`},
		{`'/`, `'/http://` + host + `/`},
		{`'./`, `'/http://` + host + dir},
		{`'@@@/http://`, `'/http://`},
		{`'@@@/ftp://`, `'/ftp://`},
		{`:url(http://`, `:url(/http://` + host + `/`},
		{`:url(https://`, `:url(/http://` + host + `/`},
		{`:url(//`, `:url(@@@/http://`},
		{`:url(/`, `:url(/http://` + host + `/`},
		{`:url(./`, `:url(/http://` + host + dir},
		{`:url(@@@/http://`, `:url(/http://`},
		{`.replace(/"WEBONEGOOGLEFIX1/g`, `.replace(/"/g`},
		{`.replace(/'WEBONEGOOGLEFIX2/g`, `.replace(/'/g`},
	}
	for _, st := range steps {
		body = strings.ReplaceAll(body, st[0], st[1])
	}
	return body
}

// addVariable runs AddVariable: "Name","Value"; "Name","Source","Mask";
// or "Name","Source","Mask","Group".
func (t *transit) addVariable(p []string) {
	switch len(p) {
	case 2:
		t.vars.Set(p[0], p[1])
	case 3, 4:
		src := t.processURIMasks(p[1])
		groups, ok := rx.FindGroupsOK(p[2], src)
		if groups == nil {
			t.log.Printf(" No match for %s in %s found.", p[2], p[1])
			return
		}
		if len(p) == 3 {
			t.vars.Set(p[0], groups[0])
			return
		}
		n, _ := strconv.Atoi(p[3])
		if n < len(groups) && ok[n] {
			t.vars.Set(p[0], groups[n])
		} else {
			t.log.Printf(" Group number %s in %s is not success.", p[3], p[1])
		}
	}
}

// dump appends a line to the traffic dump file, if dumping.
func (t *transit) dump(s string) {
	if t.dumpFile == "" {
		return
	}
	f, err := os.OpenFile(t.dumpFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	fmt.Fprintln(f, s)
	f.Close()
}

// containsAny: s contains one of list (Program.CheckString).
func containsAny(s string, list []string) bool {
	for _, l := range list {
		if strings.Contains(s, l) {
			return true
		}
	}
	return false
}
