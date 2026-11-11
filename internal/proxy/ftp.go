package proxy

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/D-Modem/webone-go/internal/rx"
)

// ftpClient is one FTP session that the Web-FTP pages drive (FtpClient.cs).
type ftpClient struct {
	mu       sync.Mutex
	log      logger
	lastUsed time.Time
	ftpLog   string
	Server   string
	User     string
	Pass     string
	Port     int
	Workdir  string

	conn net.Conn
	br   *bufio.Reader
	data net.Conn
	ok   bool
}

type ftpResponse struct {
	Code   int
	Result string
}

func (r ftpResponse) String() string { return fmt.Sprintf("%03d%s", r.Code, r.Result) }

func clientErr(msg string) ftpResponse { return ftpResponse{0, " CLIENT ERROR: " + msg} }

const ftpIdleTimeout = 5 * time.Minute

// ftpBackends are the open sessions by client number (FtpTransitManager).
var ftpBackends = struct {
	sync.Mutex
	m       map[int]*ftpClient
	started bool
}{m: map[int]*ftpClient{}}

func addFtpBackend(id int, c *ftpClient) {
	ftpBackends.Lock()
	defer ftpBackends.Unlock()
	ftpBackends.m[id] = c
	if !ftpBackends.started {
		ftpBackends.started = true
		go func() {
			for {
				time.Sleep(ftpIdleTimeout + time.Second)
				ftpBackends.Lock()
				for id, c := range ftpBackends.m {
					c.mu.Lock()
					switch {
					case !c.ok:
						c.log.Printf(" Remove closed FTP connection to %s.", c.Server)
						delete(ftpBackends.m, id)
					case time.Since(c.lastUsed) > ftpIdleTimeout:
						c.log.Printf(" Close abandoned FTP connection to %s.", c.Server)
						c.close()
						delete(ftpBackends.m, id)
					}
					c.mu.Unlock()
				}
				ftpBackends.Unlock()
			}
		}()
	}
}

func getFtpBackend(id int) *ftpClient {
	ftpBackends.Lock()
	defer ftpBackends.Unlock()
	return ftpBackends.m[id]
}

// dialFtp connects and logs in. server may be "user:pass@host:port".
func dialFtp(d *net.Dialer, server, user, pass string, log logger) (*ftpClient, error) {
	c := &ftpClient{log: log, Server: server, User: user, Pass: pass, Port: 21, lastUsed: time.Now()}
	if strings.Contains(c.Server, "/") {
		return nil, errors.New(`Malformed server name. Use only "user@password:ftp.example.com:21" format or shorter.`)
	}
	if cred, host, ok := strings.Cut(c.Server, "@"); ok {
		if u, p, ok := strings.Cut(cred, ":"); ok {
			c.User, c.Pass = u, p
		} else {
			c.User = cred
		}
		c.Server = host
	}
	if h, p, ok := strings.Cut(c.Server, ":"); ok {
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil, errors.New("Malformed port number: " + p)
		}
		c.Server, c.Port = h, n
	}
	log.Printf(">FTP connect to: %s", c.Server)
	conn, err := d.Dial("tcp", net.JoinHostPort(c.Server, strconv.Itoa(c.Port)))
	if err != nil {
		log.Printf(" Connecting error: %v", err)
		code, text := socketError(err)
		switch code {
		case "HostNotFound":
			c.ftpLog += "\nUnknown host name."
		case "ConnectionRefused":
			c.ftpLog += "\nConnection refused."
		case "TimedOut":
			c.ftpLog += "\nConnection request was sent, but no reply has received in a reasonable time."
		case "":
			c.ftpLog += "\nERROR: " + err.Error()
		default:
			c.ftpLog += "\n" + text + " (" + code + ")."
		}
		log.Printf(" Connect failed.")
		return c, nil
	}
	c.conn, c.br, c.ok = conn, bufio.NewReader(conn), true
	c.ftpLog += "\nEstablished a TCP/IP connection."
	c.ftpLog += "\n" + c.reply().String()
	r := c.command("USER " + c.User)
	c.ftpLog += "\nUSER => " + r.String()
	switch r.Code {
	case 230:
		c.ftpLog += "\nSuccessfull."
	case 331:
		r = c.command("PASS " + c.Pass)
		c.ftpLog += "\nPASS => " + r.String()
		if r.Code == 230 || r.Code == 202 {
			c.ftpLog += "\nSuccessfull."
		} else {
			c.close()
			c.ftpLog += "\nDisconnected by client."
		}
	default:
		c.close()
		c.ftpLog += "\nUser name not accepted. Disconnecting."
	}
	if c.ok {
		log.Printf(" Success.")
	} else {
		log.Printf(" Connect failed.")
	}
	return c, nil
}

// reply reads one (possibly multi-line) reply; its first line is the
// result.
func (c *ftpClient) reply() ftpResponse {
	c.lastUsed = time.Now()
	if !c.ok {
		return clientErr("not connected")
	}
	c.conn.SetReadDeadline(time.Now().Add(2 * time.Minute))
	defer c.conn.SetReadDeadline(time.Time{})
	line, err := c.br.ReadString('\n')
	if err != nil {
		c.log.Printf("!Errror on NetworkStream: %v", err)
		c.close()
		return clientErr("unexpected close of NetworkStream")
	}
	line = strings.TrimRight(line, "\r\n\x00")
	if len(line) < 3 {
		return clientErr("incorrect FTP server response")
	}
	code, err := strconv.Atoi(line[:3])
	if err != nil {
		return clientErr("incorrect FTP server response")
	}
	r := ftpResponse{code, line[3:]}
	if len(line) > 3 && line[3] == '-' {
		end := line[:3] + " "
		for {
			l, err := c.br.ReadString('\n')
			if err != nil {
				c.close()
				break
			}
			if strings.HasPrefix(l, end) {
				break
			}
		}
	}
	return r
}

func (c *ftpClient) command(cmd string) ftpResponse {
	c.lastUsed = time.Now()
	if !c.ok {
		return clientErr("cannot use NetworkStream")
	}
	if _, err := io.WriteString(c.conn, cmd+"\r\n"); err != nil {
		c.close()
		return clientErr("cannot use NetworkStream")
	}
	return c.reply()
}

// pasvData opens the data connection a 227 reply describes.
func (c *ftpClient) pasvData(d *net.Dialer, info string) (net.Conn, error) {
	m := rx.FindGroups(`\(([0-9,]*)\)`, info)
	if m == nil {
		return nil, errors.New("PASV 227 reply is not correct")
	}
	p := strings.Split(m[1], ",")
	if len(p) < 6 {
		return nil, errors.New("PASV 227 reply contains not full IP")
	}
	p1, _ := strconv.Atoi(p[4])
	p2, _ := strconv.Atoi(p[5])
	port := strconv.Itoa(p1*256 + p2)
	ip := strings.Join(p[:4], ".")
	conn, err := d.Dial("tcp", net.JoinHostPort(ip, port))
	if err != nil {
		// servers behind NAT often give their private address
		conn, err = d.Dial("tcp", net.JoinHostPort(c.Server, port))
	}
	if err != nil {
		return nil, err
	}
	c.data = conn
	return conn, nil
}

func (c *ftpClient) closeData() {
	c.lastUsed = time.Now()
	if c.data != nil {
		c.data.Close()
		c.data = nil
	}
}

func (c *ftpClient) close() {
	if c.conn != nil {
		c.conn.Close()
	}
	c.closeData()
	c.ok = false
}

// ftpEntry is a line of a LIST reply (FtpDirectoryListEntry.cs).
type ftpEntry struct {
	Name, Date      string
	Size            int64
	Directory, Link bool
}

const (
	ftpUnixPattern = `^([\w-]+)\s+(\d+)\s+(\w+)\s+(\w+)\s+(\d+)\s+(\w+\s+\d+\s+\d+|\w+\s+\d+\s+\d+:\d+)\s+(.+)$`
	ftpDosPattern  = `^(\d+-\d+-\d+\s+\d+:\d+(?:AM|PM))\s+(<DIR>|\d+)\s+(.+)$`
)

func parseFtpLine(line string) (ftpEntry, bool) {
	if g := rx.FindGroups(ftpDosPattern, line); g != nil {
		e := ftpEntry{Date: g[1], Directory: g[2] == "<DIR>", Name: g[3], Size: -1}
		if !e.Directory {
			e.Size, _ = strconv.ParseInt(g[2], 10, 64)
		}
		return e, true
	}
	if g := rx.FindGroups(ftpUnixPattern, line); g != nil {
		e := ftpEntry{Date: g[6], Name: g[7], Directory: strings.HasPrefix(g[1], "d"), Link: strings.HasPrefix(g[1], "l")}
		e.Size, _ = strconv.ParseInt(g[5], 10, 64)
		return e, true
	}
	return ftpEntry{}, false
}

// ftpPage is the Web-FTP client, /!ftp/ (FtpClientGUI.cs).
func (t *transit) ftpPage() *infoPage {
	return t.ftpPageFor("")
}

func newFtpPage() *infoPage {
	p := newInfoPage("WebOne: FTP client", "File Transfer Protocol client", "This is a FTP Web-GUI page.", 200)
	p.ShowFooter = false
	p.HTMLHeaders = "<link href=\"/webone.css\" rel=\"stylesheet\"/>\n<link rel=\"shortcut icon\" href=\"/ftpfav.ico\" type=\"image/x-icon\">"
	return p
}

func (t *transit) ftpPageFor(dest string) (page *infoPage) {
	args := url.Values{}
	if t.url != nil {
		args, _ = url.ParseQuery(t.url.RawQuery)
	}
	clientID, _ := strconv.Atoi(args.Get("client"))
	defer func() {
		if r := recover(); r != nil {
			t.log.Printf(" FTP Client Error: %v", r)
			page = newFtpPage()
			page.Header = "File Transfer Error"
			page.Content = "<big>An error occured in FTP Client.</big><p>" + strings.ReplaceAll(fmt.Sprint(r), "\n", "<br>") + "</p>" +
				"<p>Navigation: <a href=\"javascript:history.back()\"><b>Go back</b></a>. " +
				"<a href=\"/!ftp/\"><b>Reconnect</b></a>. Directory listing: " +
				"<a href=\"/!ftp/?client=" + strconv.Itoa(clientID) + "&task=listdir&cwd=/\"><b>root</b></a>, " +
				"<a href=\"/!ftp/?client=" + strconv.Itoa(clientID) + "&task=listdir\"><b>current</b>.</a> </p>"
		}
	}()
	if !t.cfg.EnableWebFtp {
		t.log.Printf("<Web-FTP: disabled.")
		p := newFtpPage()
		p.Header = "Sorry"
		p.Content = "The server administrator has disabled FTP browsing via this proxy."
		return p
	}
	switch clientID {
	case 0:
		return t.ftpWelcome()
	case -1:
		return t.ftpConnect(args, dest)
	}
	return t.ftpResult(args, clientID)
}

func (t *transit) ftpWelcome() *infoPage {
	t.log.Printf("<Web-FTP: welcome page.")
	p := newFtpPage()
	p.Header = "File Transfer Protocol client"
	p.Content = "<p>Welcome to space of computer files, directories and servers. Here you'll be able to download something to your PC from FTP servers without quitting a web browser.</p>"
	p.HTMLHeaders += "\n<style>.formlabel { width: 80px; display: inline-block; display:-moz-inline-block; display:-moz-inline-stack; text-align: right; }</style>"
	p.Content += "<form action=\"/!ftp/\" method=\"GET\" name=\"Connect\">\n" +
		"<center><input type=\"hidden\" name=\"client\" value=\"-1\">\n" +
		"<p><label class=\"formlabel\" for=\"server\">Server:</label> <input type=\"text\" size=\"23\" name=\"server\" id=\"server\" value=\"\"><br>\n" +
		"<label class=\"formlabel\" for=\"uri\">or URI:</label> <input type=\"text\" size=\"23\" name=\"uri\" id=\"uri\" value=\"\"></p>\n" +
		"<p><label class=\"formlabel\" for=\"user\">Username:</label> <input type=\"text\" size=\"20\" name=\"user\" id=\"user\" value=\"anonymous\"><br>\n" +
		"<label class=\"formlabel\" for=\"pass\">Password:</label> <input type=\"password\" size=\"20\" name=\"pass\" id=\"pass\" value=\"user@domain.su\"></p>\n" +
		"<p><input type=\"submit\" value=\"Let's go!\"></p>\n" +
		"</center></form>"
	return p
}

func argOr(v url.Values, k, def string) string {
	if _, ok := v[k]; ok {
		return v.Get(k)
	}
	return def
}

func (t *transit) ftpConnect(args url.Values, dest string) *infoPage {
	p := newFtpPage()
	server := args.Get("server")
	user := argOr(args, "user", "anonymous")
	pass := argOr(args, "pass", "email@example.com")
	uri := args.Get("uri")
	if dest != "" {
		uri = dest
	}
	if server == "" && uri == "" {
		p.Content = "<h2>Empty connection data</h2>\n<p>You need to specify the remote server first.</p><p><a href=\"/!ftp/\"><b>Go back.</b></a></p>"
		return p
	}
	id := int(rand.Int32N(1<<31-2)) + 1
	web := "http://" + t.serverName() + "/!ftp/?client=" + strconv.Itoa(id)
	if uri == "" {
		web += "&task=listdir"
	} else {
		if !strings.HasPrefix(uri, "ftp://") {
			p.Content = "<h2>Malformed Universal Resource Identificator</h2>\n" +
				"<p>URIs (locations) are accepted only in the following format:\n" +
				"<pre>ftp://ftp.microsoft.com/MISC1/DESKAPPS/DOSWORD/KB/Q81/4/46.TXT</pre>\n" +
				"Also the Identificator can contain FTP user name, password and port.</p><p><a href=\"/!ftp/\"><b>Go back.</b></a></p>"
			return p
		}
		path := rx.Replace(`(;type=.*)`, uri, "", rx.None)
		path = rx.Replace(`ftp://`, path, "", rx.IgnoreCase)
		path = rx.Replace(`http://`, path, "", rx.IgnoreCase)
		if !strings.Contains(path, "/") {
			path += "/"
		}
		i := strings.Index(path, "/")
		server, path = path[:i], path[i:]
		if strings.HasSuffix(path, "/") {
			web += "&cwd=" + path + "&task=listdir"
		} else {
			web += "&name=" + path + "&task=retr"
		}
	}
	c, err := dialFtp(t.s.up.dialer, server, user, pass, t.log)
	if err != nil {
		panic(err.Error())
	}
	if c.ok {
		addFtpBackend(id, c)
		p.Header = "File Transfer: " + c.Server
		p.Content = "<h2>Connecting to the server</h2>\n<pre>" + c.ftpLog + "</pre>\n<p>Okay, <a href=\"" + web + "\"><b>click here</b></a>.</p>"
		p.Status = 302
		p.Headers.Add("Location", web)
		return p
	}
	p.Title = "WebOne cannot open FTP connection"
	p.Header = "File Transfer: " + c.Server
	p.Content = "<h2>The client could not connect to the server.</h2>\n<pre>" + c.ftpLog + "</pre>\n" +
		"<p>Return to <a href=\"/!ftp/\"><b>connection page</b></a> and try again.</p>"
	return p
}

func (t *transit) ftpIconsOn() bool {
	if !t.cfg.ShowFileIcons {
		return false
	}
	for _, f := range []string{"_dir.gif", "_dir-up.gif", "_file.gif", "_link.gif"} {
		if !fileExists(filepath.Join(t.cfg.ContentDirectory, "filetypes", f)) {
			return false
		}
	}
	return true
}

func (t *transit) ftpIcon(name string) string {
	i := strings.LastIndexByte(name, '.')
	if i < 0 || strings.TrimSpace(name[i+1:]) == "" {
		return "/filetypes/_file.gif"
	}
	ext := name[i+1:]
	if !strings.ContainsAny(ext, `/\`) && fileExists(filepath.Join(t.cfg.ContentDirectory, "filetypes", ext+".gif")) {
		return "/filetypes/" + ext + ".gif"
	}
	return "/filetypes/_file.gif"
}

func (t *transit) ftpResult(args url.Values, id int) *infoPage {
	p := newFtpPage()
	c := getFtpBackend(id)
	if c == nil {
		t.log.Printf(" Web-FTP: unknown client ID.")
		return t.ftpWelcome()
	}
	c.mu.Lock()
	unlock := true
	defer func() {
		if unlock {
			c.mu.Unlock()
		}
	}()
	sid := strconv.Itoa(id)
	again := "<p>Return to <a href=\"/!ftp/\"><b>start page</b></a> and try to connect again.</p>"
	p.Title = c.Server + " - FTP"
	p.Header = "File Transfer: " + c.Server
	p.Content = ""
	if !c.ok {
		p.Content = "<h2>Connection lost</h2><p>The FTP connection to the server is closed.</p><p><a href=\"/!ftp/\"><b>Go to start</b></a> and try again.</p>"
		return p
	}
	icons := t.ftpIconsOn()
	img := func(src string) string {
		return "<img border=\"0\" src=\"" + src + "\" width=\"16px\" height=\"16px\" class=\"fileicon\"> "
	}
	d := t.s.up.dialer
	switch args.Get("task") {
	case "listdir":
		if cwd := args.Get("cwd"); cwd != "" {
			if r := c.command("CWD " + cwd); r.Code != 250 {
				p.Content += "<p><b>Cannot change working directory:</b> " + r.String() + "</p>"
			}
		}
		r := c.command("PWD")
		if r.Code != 257 {
			p.Content += "<p><b>&quot;Print Working Directory&quot; command has returned an unexpected result:</b> " + r.String() + "</p>"
			p.Content += "<p>Reload this page or return to <a href=\"/!ftp/\"><b>start page</b></a> and try to connect again.</p>"
			return p
		}
		if g := rx.FindGroups(`"(.*)"`, r.Result); g != nil {
			p.Content += "<h2><b><a href=\"/!ftp/?client=" + sid + "&task=listdir&cwd=" + url.QueryEscape("/") + "\">Server root</a></b>"
			c.Workdir = ""
			for _, dir := range strings.Split(g[1], "/") {
				c.Workdir += dir + "/"
				if dir != "" {
					p.Content += " &raquo; <b><a href=\"/!ftp/?client=" + sid + "&task=listdir&cwd=" + url.QueryEscape(c.Workdir) + "\">" + dir + "</a></b>"
				}
			}
			p.Content += "</h2>\n"
		} else {
			p.Content += "<h2>" + r.Result + "</h2>\n"
			c.Workdir = "./"
		}
		c.command("OPTS UTF8 ON")
		if r = c.command("TYPE A"); r.Code != 200 {
			p.Content += "<p><b>Cannot set ASCII mode:</b> " + r.String() + "</p>" + again
			return p
		}
		if r = c.command("PASV"); r.Code != 227 {
			p.Content += "<p><b>Cannot prepare data connection:</b> " + r.String() + "</p>" + again
			return p
		}
		data, err := c.pasvData(d, r.Result)
		if err != nil {
			p.Content += "<p><b>Cannot establish data connection:</b> " + r.String() + "</p>" + again
			return p
		}
		if r = c.command("LIST"); r.Code != 150 && r.Code != 125 {
			c.closeData()
			p.Content += "<p><b>Directory listing is inaccessible:</b> " + r.String() + "</p>" + again
			return p
		}
		data.SetReadDeadline(time.Now().Add(2 * time.Minute))
		list, _ := io.ReadAll(data)
		c.closeData()
		c.reply()
		var entries []ftpEntry
		for _, l := range strings.Split(string(list), "\n") {
			l = strings.Trim(l, "\r\n")
			if strings.TrimSpace(l) == "" {
				continue
			}
			e, ok := parseFtpLine(l)
			if !ok || e.Name == "." || e.Name == ".." {
				continue
			}
			entries = append(entries, e)
		}
		var b strings.Builder
		b.WriteString("<table>\n")
		if c.Workdir != "//" {
			b.WriteString("<tr><td>")
			if icons {
				b.WriteString(img("/filetypes/_dir-up.gif"))
			}
			b.WriteString("[<a href=\"/!ftp/?client=" + sid + "&task=listdir&cwd=" + url.QueryEscape(c.Workdir+"..") + "\">..</a>]")
			b.WriteString("</td><td></td><td></td></tr>\n")
		}
		for _, e := range entries {
			icon := ""
			if icons {
				if e.Directory {
					icon = img("/filetypes/_dir.gif")
				} else {
					icon = img(t.ftpIcon(e.Name))
				}
			}
			b.WriteString("<tr><td>")
			switch {
			case e.Directory && !e.Link:
				b.WriteString(icon + "[<a href=\"/!ftp/?client=" + sid + "&task=listdir&cwd=" + url.QueryEscape(c.Workdir+e.Name) + "\">" + e.Name + "</a>]")
			case e.Link:
				if icons {
					b.WriteString(img("/filetypes/_link.gif"))
				}
				if e.Directory {
					b.WriteString("<i>[" + e.Name + "]</i>")
				} else {
					b.WriteString("<i>" + e.Name + "</i>")
				}
			default:
				b.WriteString(icon + "<a href=\"/!ftp/?client=" + sid + "&task=retr&name=" + url.QueryEscape(c.Workdir+e.Name) + "\" target='_blank'>" + e.Name + "</a>")
			}
			b.WriteString("</td><td>" + strconv.FormatInt(e.Size, 10) + "</td><td>" + e.Date + "</td></tr>\n")
		}
		b.WriteString("\n</table><br>\n<table class=\"nostyle\" border=\"0\"><tr class=\"nostyle\"><td class=\"nostyle\" width=\"100%\">")
		uri := "ftp://"
		if c.User != "anonymous" {
			uri += c.User + ":" + c.Pass + "@"
		}
		uri += c.Server
		if c.Port != 21 {
			uri += ":" + strconv.Itoa(c.Port)
		}
		uri += strings.ReplaceAll(c.Workdir, "//", "/")
		b.WriteString("<a href=\"/!ftp/?client=-1&uri=" + url.QueryEscape(uri) + "\" title=\"Files from " + c.Server + "\">Permanent link</a> ")
		b.WriteString("(<a href=\"" + uri + "\" title=\"" + uri + "\">direct</a>).</td>\n")
		b.WriteString("<td class=\"nostyle\"><a href=\"/!ftp/?client=" + sid + "&task=close\" title=\"End this FTP session\">Disconnect</a>.</td>\n</tr></table>")
		p.Content += b.String()
		return p

	case "retr":
		name := args.Get("name")
		if name == "" {
			p.Content += "<h2>Nothing to download</h2><p><a href=\"/!ftp/?client=" + sid + "&task=listdir\">Click here</a> to see directory listing.</p>"
			return p
		}
		r := c.command("TYPE I")
		if r.Code != 200 {
			p.Content += "<p><b>Cannot set BINARY mode:</b> " + r.String() + "</p>" + again
			return p
		}
		if r = c.command("PASV"); r.Code != 227 {
			p.Content += "<p><b>Cannot prepare data connection:</b> " + r.String() + "</p>" + again
			return p
		}
		data, err := c.pasvData(d, r.Result)
		if err != nil {
			p.Content += "<p><b>Cannot establish data connection:</b> " + r.String() + "</p>" + again
			return p
		}
		if r = c.command("RETR " + name); r.Code != 150 && r.Code != 125 {
			c.closeData()
			p.Content += "<p><b>Cannot download the file:</b> " + r.String() + "</p>"
			return p
		}
		ct := "application/octet-stream"
		lower := strings.ToLower(name)
		for ext, mt := range t.cfg.MimeTypes {
			if strings.HasSuffix(lower, "."+ext) {
				ct = mt
				break
			}
		}
		base := name[strings.LastIndexByte(name, '/')+1:]
		if ct == "application/octet-stream" {
			p.Headers.Add("Content-Disposition", `attachment; filename="`+base+`"`)
		} else {
			p.Headers.Add("Content-Disposition", `inline; filename="`+base+`"`)
		}
		p.AttachmentType = ct
		// the session stays locked until the file is sent
		unlock = false
		p.Attachment = &ftpDownload{Conn: data, c: c, log: t.log}
		return p

	case "close":
		c.close()
		p.Content = "<h2>Disconnected from the server</h2>\n<p>The file transfer session has been ended.</p>\n<p>Return to <a href=\"/!ftp/\"><b>connection page</b></a>.</p>"
		return p
	}
	p.Content = "<h2>No or unknown task</h2><p>The specified <i>task</i> argument is not recognized by WebOne.</p>" +
		"<p><a href=\"/!ftp/?client=" + sid + "&task=listdir\">Click here</a> to see directory listing.</p>"
	return p
}

// ftpDownload is a file being downloaded; closing it reads the server's
// "transfer complete" and frees the session.
type ftpDownload struct {
	net.Conn
	c    *ftpClient
	log  logger
	once sync.Once
}

func (f *ftpDownload) Close() error {
	f.once.Do(func() {
		f.c.closeData()
		f.c.reply()
		f.log.Printf(" Close data stream.")
		f.c.mu.Unlock()
	})
	return nil
}
