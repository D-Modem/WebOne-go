package config

import (
	"io"
	"strings"
	"testing"

	"github.com/D-Modem/webone-go/internal/logx"
)

func TestParseOptionEscapes(t *testing.T) {
	o, err := ParseOption(`AddVariable="Name", "a \"quoted\"\tvalue"`, "test")
	if err != nil {
		t.Fatal(err)
	}
	if o.Key != "AddVariable" || len(o.Values) != 2 || o.Values[0] != "Name" || o.Values[1] != "a \"quoted\"\tvalue" {
		t.Errorf("got %q %q", o.Key, o.Values)
	}
	o, _ = ParseOption("Port=8080", "test")
	if o.Key != "Port" || o.Value != "8080" || o.Values != nil {
		t.Errorf("plain option: %+v", o)
	}
}

func load(t *testing.T, text string) *Config {
	t.Helper()
	logx.SetConsole(io.Discard)
	c := New()
	c.LoadContent([]byte(text), "test.conf")
	if err := c.Process(); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestLoadServerAndEdits(t *testing.T) {
	c := load(t, `
[Server]
Port=8088
DefaultHostName=bbs.local
OutputEncoding=Windows
UserAgent=%Original% WebOne/%WOVer%

[ForceHttps]
example.com

[Edit:^http://example\.com/]
OnContentType=text/html
AddFind=foo
AddReplace=bar
AddResponseHeader=X-Test: 1

[Edit:\.js$]
AddRedirect=http://cdn.example/x.js
`)
	if c.Port != 8088 || c.DefaultHostName != "bbs.local" {
		t.Errorf("server: %d %s", c.Port, c.DefaultHostName)
	}
	if c.OutputEncoding == nil || c.OutputEncoding.CodePage != c.CodepageCulture.ANSI {
		t.Errorf("OutputEncoding=Windows gave %v", c.OutputEncoding)
	}
	if len(c.ForceHttps) != 1 || c.ForceHttps[0] != "example.com" {
		t.Errorf("ForceHttps: %q", c.ForceHttps)
	}
	if v := c.Vars.Value("Proxy"); v != "bbs.local:8088" {
		t.Errorf("%%Proxy%% = %q", v)
	}
	if len(c.EditRules) != 2 {
		t.Fatalf("%d edit sets", len(c.EditRules))
	}
	set := c.EditRules[0]
	if len(set.ContentTypeMasks) != 1 || set.IsForRequest {
		t.Errorf("first set: %+v", set)
	}
	found := false
	for _, e := range set.Edits {
		if e.Action == AddFindReplace && e.Find == "foo" && e.Replace == "bar" {
			found = true
		}
	}
	if !found {
		t.Errorf("AddFind+AddReplace not joined: %+v", set.Edits)
	}
	if !c.EditRules[1].IsForRequest {
		t.Error("a redirect set is for requests")
	}
}

func TestExpand(t *testing.T) {
	g := NewVars()
	g.Set("Proxy", "bbs:80")
	g.Set("ProxyHost", "bbs")
	extra := NewVars()
	extra.Set("URL", "http://x/")
	got := Expand("%Proxy% %ProxyHost% %URL% $ProxyHost", g, extra)
	if !strings.HasPrefix(got, "bbs:80 bbs http://x/ bbs") {
		t.Errorf("Expand: %q", got)
	}
}

func TestParseDate(t *testing.T) {
	for _, s := range []string{"2001-02-03", "2001-02-03T04:05:06", "2001-02-03T04:05:06+0", "2001-02-03T04:05:06Z"} {
		d, err := ParseDate(s)
		if err != nil || d.Year() != 2001 || d.Month() != 2 || d.Day() != 3 {
			t.Errorf("ParseDate(%q) = %v, %v", s, d, err)
		}
	}
}
