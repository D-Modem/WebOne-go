package proxy

import (
	"errors"
	"io"
	"math/rand/v2"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/D-Modem/webone-go/internal/certs"
	"github.com/D-Modem/webone-go/internal/config"
	"github.com/D-Modem/webone-go/internal/rx"
)

// runConverter runs a [Converters] program on in (nil when the program
// downloads srcURL itself) and gives its output, with the output's length
// when it is known (Converter.Run).
func (s *Server) runConverter(cv config.Converter, log logger, in io.ReadCloser, arg1, arg2, destType, srcURL string) (io.ReadCloser, int64, error) {
	if cv.SelfDownload && (in != nil || srcURL == "") {
		if in != nil {
			in.Close()
		}
		return nil, -1, errors.New("The converter " + cv.Executable + " can only download the content self")
	}
	if srcURL == "" {
		srcURL = "http://webone.github.io/index.htm"
	}
	if destType == "" {
		destType = "tmp"
	}
	rnd := strconv.FormatUint(uint64(rand.Uint32()), 10)
	srcTmp := filepath.Join(s.cfg.TemporaryDirectory, "convert-"+rnd+".orig.tmp")
	dstTmp := filepath.Join(s.cfg.TemporaryDirectory, "convert-"+rnd+".conv."+destType)
	cleanup := func() {
		os.Remove(srcTmp)
		os.Remove(dstTmp)
	}

	if !cv.UseStdin && !cv.SelfDownload {
		f, err := os.Create(srcTmp)
		if err != nil {
			in.Close()
			return nil, -1, err
		}
		_, err = io.Copy(f, in)
		f.Close()
		in.Close()
		in = nil
		if err != nil {
			cleanup()
			return nil, -1, err
		}
	}

	line := s.processURIMasks(cv.CommandLine, srcURL, false, nil)
	line = strings.NewReplacer(
		"%SRC%", srcTmp, "%ARG1%", arg1, "%DEST%", dstTmp, "%ARG2%", arg2,
		"%DESTEXT%", destType, "%SRCURL%", srcURL,
	).Replace(line)
	cmd := exec.Command(cv.Executable, certs.SplitArgs(line)...)
	cmd.Stderr = os.Stderr
	log.Printf(" Converting: %s %s...", cv.Executable, line)

	var stdin io.WriteCloser
	var err error
	if cv.UseStdin && in != nil {
		if stdin, err = cmd.StdinPipe(); err != nil {
			in.Close()
			return nil, -1, err
		}
	}
	feed := func() {
		if stdin != nil {
			go func() {
				io.Copy(stdin, in)
				stdin.Close()
			}()
		}
	}

	if cv.UseStdout {
		out, err := cmd.StdoutPipe()
		if err != nil {
			return nil, -1, err
		}
		if err := cmd.Start(); err != nil {
			if in != nil {
				in.Close()
			}
			cleanup()
			return nil, -1, err
		}
		feed()
		return &procReader{r: out, cmd: cmd, in: in, cleanup: cleanup}, -1, nil
	}

	if err := cmd.Start(); err != nil {
		if in != nil {
			in.Close()
		}
		cleanup()
		return nil, -1, err
	}
	feed()
	err = cmd.Wait()
	if in != nil {
		in.Close()
	}
	f, ferr := os.Open(dstTmp)
	if ferr != nil {
		cleanup()
		if err != nil {
			return nil, -1, err
		}
		return nil, -1, ferr
	}
	// WebOne leaves the files for a minute, then removes them
	time.AfterFunc(time.Minute, cleanup)
	st, _ := f.Stat()
	return f, st.Size(), nil
}

// procReader is a converter's standard output; closing it stops the
// converter and removes its temporary files.
type procReader struct {
	r       io.ReadCloser
	cmd     *exec.Cmd
	in      io.Closer
	cleanup func()
	once    sync.Once
}

func (p *procReader) Read(b []byte) (int, error) { return p.r.Read(b) }

func (p *procReader) Close() error {
	p.once.Do(func() {
		if p.in != nil {
			p.in.Close()
		}
		p.cmd.Process.Kill()
		p.cmd.Wait()
		p.cleanup()
	})
	return nil
}

func (s *Server) converter(name string) (config.Converter, bool) {
	for _, c := range s.cfg.Converters {
		if c.Executable == name {
			return c, true
		}
	}
	return config.Converter{}, false
}

// convertResponse converts the server's response on the fly with an
// AddConverting rule.
func (t *transit) convertResponse(name, dest, arg1, arg2, contentType, srcContentType string) {
	cv, ok := t.s.converter(name)
	if !ok {
		t.op.RespBody.Close()
		t.sendError(503, "<big>Converter &quot;<b>"+name+"</b>&quot; is unknown</big>.<br>"+
			"<p>This converter is not well listed in configuration file.</p>"+
			`<p>See <a href="http://github.com/atauenis/webone/wiki">WebOne wiki</a> for help on this.</p>`)
		return
	}
	var in io.ReadCloser = t.op.RespBody
	if cv.SelfDownload {
		in.Close()
		in = nil
	}
	out, n, err := t.s.runConverter(cv, t.log, in, arg1, arg2, dest, t.urlString())
	if err != nil {
		t.log.Printf(" On-fly converter error: %v", err)
		t.sendError(502, "<p><big><b>Converter error</b>: "+err.Error()+"</big></p>"+
			"Source URL: "+t.urlString()+"<br>Utility: "+name+"<br>"+
			"Mode: seamless from '"+srcContentType+"' to '"+contentType+"'")
		return
	}
	t.sendStream(out, contentType, n, 200)
}

// localFile finds a file of the proxy (logo.webp ...): as given, or next
// to the program.
func localFile(name string) (string, error) {
	if _, err := os.Stat(name); err == nil {
		return name, nil
	}
	if exe, err := os.Executable(); err == nil {
		p := filepath.Join(filepath.Dir(exe), name)
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", errors.New("No such file: " + name)
}

// imageMagickTest converts WebOne's logo to GIF (/!imagemagicktest.gif).
func (t *transit) imageMagickTest() {
	for _, cv := range t.cfg.Converters {
		if cv.Executable == "convert" && !cv.SelfDownload {
			p, err := localFile("logo.webp")
			if err == nil {
				var f *os.File
				if f, err = os.Open(p); err == nil {
					var out io.ReadCloser
					var n int64
					out, n, err = t.s.runConverter(cv, t.log, f, "", "", "gif", "https://github.com/atauenis/webone/")
					if err == nil {
						t.sendStream(out, "image/gif", n, 200)
						return
					}
				}
			}
			t.sendError(502, "<p><big><b>Converter error</b>: "+err.Error()+"</big></p>")
			return
		}
	}
	t.sendInfoPage("WebOne: ImageMagick error", "Error",
		"<p>ImageMagick's <b>convert</b> utility is not properly registered in <b>[Converters]</b> section of proxy configuration.</p>"+
			"<p>Make sure that the line is present in <code>[Converters]</code> section of configuration: <code>convert %SRC% %ARG1% %DESTEXT%:- %ARG2%</code></p>", 500)
}

// manualConvert is /!convert/?url=...&dest=gif&type=image/gif (or src= a
// local file).
func (t *transit) manualConvert(query string) {
	if !t.cfg.EnableManualConverting {
		t.sendInfoPage("WebOne: Feature disabled", "Feature disabled",
			"Manual file converting is disabled for security purposes.<br>Proxy administrator can enable it via <code>[Server]</code> section, <code>EnableManualConverting</code> option.", 500)
		return
	}
	q := "?" + query
	get := func(name, def string) (string, bool) {
		g := rx.FindGroups(`(`+name+`)=([^&]+)`, q)
		if g == nil {
			return def, false
		}
		v, err := url.PathUnescape(g[2])
		if err != nil {
			v = g[2]
		}
		return v, true
	}
	src, hasSrc := get("src", "")
	srcURL, hasURL := get("url", "")
	dest, _ := get("dest", "xbm")
	destMime, _ := get("type", "image/x-xbitmap")
	name, _ := get("util", "convert")
	arg1, _ := get("arg", "")
	arg2, _ := get("arg2", "")

	if !hasURL && !hasSrc {
		t.sendError(200, "<big>Here you can summon ImageMagick to convert a picture file</big>.<br>"+
			"<p>Usage: /!convert/?url=https://example.com/filename.ext&dest=gif&type=image/gif<br>"+
			"or: /!convert/?src=filename.ext&dest=gif&type=image/gif</p>"+
			`<p>See <a href="http://github.com/atauenis/webone/wiki">WebOne wiki</a> for help on this.</p>`)
		return
	}
	cv, ok := t.s.converter(name)
	if !ok {
		t.sendError(503, "<big>Converter &quot;<b>"+name+"</b>&quot; is unknown</big>.<br>"+
			"<p>This converter is not listed in configuration file.</p>"+
			`<p>See <a href="http://github.com/atauenis/webone/wiki">WebOne wiki</a> for help on this.</p>`)
		return
	}
	var in io.ReadCloser
	if hasURL {
		if !cv.SelfDownload {
			t.log.Printf(">Downloading source stream...")
			resp, err := t.s.fetch(srcURL)
			if err != nil {
				t.log.Printf(" Converter cannot download source: %v", err)
				t.sendError(503, "<p><big><b>Converter cannot download the source</b>: "+err.Error()+"</big></p>Source URL: "+srcURL)
				return
			}
			in = resp.Body
		}
	} else {
		srcURL = "http://0.0.0.0/localfile"
		if !cv.SelfDownload {
			// only files of the proxy's own folder: no paths
			p, err := localFile(src)
			if err == nil && (src == "CON:" || strings.ContainsAny(src, `/\`) || strings.Contains(src, "..")) {
				err = errors.New("Bad source file name")
			}
			var f *os.File
			if err == nil {
				f, err = os.Open(p)
			}
			if err != nil {
				t.log.Printf(" Converter cannot open source: %v", err)
				t.sendError(503, "<p><big><b>Converter cannot open the source</b>: "+err.Error()+"</big></p>Source URL: "+srcURL)
				return
			}
			in = f
		}
	}
	out, n, err := t.s.runConverter(cv, t.log, in, arg1, arg2, dest, srcURL)
	if err != nil {
		t.log.Printf(" Converter error: %v", err)
		t.sendError(502, "<p><big><b>Converter error</b>: "+err.Error()+"</big></p>Source URL: "+srcURL+"<br>Utility: "+cv.Executable)
		return
	}
	t.sendStream(out, destMime, n, 200)
}
