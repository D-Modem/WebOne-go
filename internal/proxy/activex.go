package proxy

import (
	"bytes"
	"io"
	"strconv"
	"strings"

	"github.com/D-Modem/webone-go/internal/rx"
)

// activexCabURL emulates Microsoft's ActiveX codebase search server
// (activex.microsoft.com/objects/ocget.dll) from [ActiveXGallery]: the URL
// of a component by CLSID or MIME type; "" to ask the real service; "404"
// when the installed component is up to date (HttpTransit.GetActiveXCabUrl).
func (t *transit) activexCabURL() string {
	var clsid, version, mime, lang string
	var hasCLSID, hasVer, hasMime bool
	if cl := t.req.Header.Get("Content-Length"); cl != "" && cl != "0" {
		data, _ := io.ReadAll(t.req.Body)
		t.req.Body = bytes.NewReader(data)
		body := strings.ToUpper(string(data))
		if g := rx.FindGroups(`CLSID=\{(.*)\}`, body); g != nil {
			clsid, hasCLSID = g[1], true
		}
		if g := rx.FindGroups(`VERSION=(.*)`, body); g != nil {
			version, hasVer = g[1], true
		}
		if g := rx.FindGroups(`MIMETYPE=(.*)`, body); g != nil {
			mime, hasMime = g[1], true
		}
	} else {
		q := strings.ToUpper("?" + t.url.RawQuery)
		if g := rx.FindGroups(`CLSID=%7(.*)%7`, q); g != nil {
			clsid, hasCLSID = g[1], true
		}
		if g := rx.FindGroups(`VERSION=(.*)`, q); g != nil {
			version, hasVer = g[1], true
		}
		if g := rx.FindGroups(`MIMETYPE=(.*)`, q); g != nil {
			mime, hasMime = g[1], true
		}
	}
	if al := t.req.Header.Get("Accept-Language"); al != "" {
		if g := rx.FindGroups(`[a-zA-Z]*`, al); g != nil {
			lang = strings.ToUpper(g[0])
		}
	}
	if !hasCLSID && !hasMime {
		t.dump("--OCGET Emulation: Request not understood, unknown syntax.--")
		t.log.Printf("OCGET Emulation: missing search terms.")
		return ""
	}
	if hasVer {
		version = strings.ReplaceAll(version, ",", ".")
	}
	db := t.cfg.ActivexGalleryCLSIDs
	codebase, found := "", false
	lookup := func(key string) bool {
		if _, ok := db[key]; !ok {
			return false
		}
		if hasVer {
			if reported, ok := parseDotVersion(version); ok {
				if latest, ok := db[key+",LATEST"]; ok {
					if lv, ok := parseDotVersion(latest); ok && cmpVersion(reported, lv) >= 0 {
						return true // up to date
					}
				}
			}
		}
		if _, ok := db[key+","+lang]; ok {
			key += "," + lang
		}
		codebase, found = db[key], true
		return false
	}
	if hasCLSID && lookup(clsid) {
		return "404"
	}
	if hasMime && lookup(mime) {
		return "404"
	}

	report := "something"
	if hasCLSID {
		report = clsid
	}
	if hasMime {
		report = mime
	}
	if hasVer {
		report += ";version=" + version
	}
	if lang != "" {
		report += ";language=" + lang
	}
	if found {
		t.dump("--OCGET Emulation: " + report + " is " + codebase + ".--")
		t.log.Printf("OCGET Emulation: found codebase for %s, return 302.", report)
		return codebase
	}
	t.dump("--OCGET Emulation: using real Microsoft service for " + report + ".--")
	t.log.Printf("OCGET Emulation: no codebase for %s, use real service.", report)
	return ""
}

// parseDotVersion parses a .NET Version ("1.2", "1.2.3.4").
func parseDotVersion(s string) ([]int, bool) {
	parts := strings.Split(strings.TrimSpace(s), ".")
	if len(parts) < 2 || len(parts) > 4 {
		return nil, false
	}
	out := make([]int, len(parts))
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return nil, false
		}
		out[i] = n
	}
	return out, true
}

// cmpVersion compares versions as .NET does: a missing part is less than
// any number.
func cmpVersion(a, b []int) int {
	for i := 0; i < 4; i++ {
		x, y := -1, -1
		if i < len(a) {
			x = a[i]
		}
		if i < len(b) {
			y = b[i]
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}
