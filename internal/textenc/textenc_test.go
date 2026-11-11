package textenc

import (
	"bytes"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	all := make([]byte, 256)
	for i := range all {
		all[i] = byte(i)
	}
	for _, cp := range []int{1252, 1251, 866, 437, 28591} {
		e := ByCodePage(cp)
		if e == nil {
			t.Errorf("no code page %d", cp)
			continue
		}
		if got := e.Encode(e.Decode(all)); !bytes.Equal(got, all) {
			t.Errorf("%s: bytes change on decode and encode", e.Name)
		}
	}
}

func TestEncodeUnknown(t *testing.T) {
	e := Lookup("windows-1251")
	if e == nil {
		t.Fatal("no windows-1251")
	}
	if got := string(e.Encode("Ж€你")); got != "\xc6\x88?" {
		t.Errorf("got %q", got)
	}
	if Lookup("utf-8") != UTF8 || Lookup("koi8-r") == nil || Lookup("x-mac-cyrillic") == nil {
		t.Error("lookup by name")
	}
}
