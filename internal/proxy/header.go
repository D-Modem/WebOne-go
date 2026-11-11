package proxy

import (
	"net/http"
	"sort"
	"strings"
)

// Header is an ordered list of HTTP headers, looked up ignoring case, as
// WebOne's WebHeaderCollection is used.
type Header struct {
	kv []hkv
}

type hkv struct{ k, v string }

// NewHeader is an empty header list.
func NewHeader() *Header { return &Header{} }

// Get is a header's value; several of one name are joined with ", ".
// "" when absent.
func (h *Header) Get(k string) string {
	var vals []string
	for _, e := range h.kv {
		if strings.EqualFold(e.k, k) {
			vals = append(vals, e.v)
		}
	}
	return strings.Join(vals, ", ")
}

// Values is every value of a header.
func (h *Header) Values(k string) []string {
	var vals []string
	for _, e := range h.kv {
		if strings.EqualFold(e.k, k) {
			vals = append(vals, e.v)
		}
	}
	return vals
}

// Has: the header is present.
func (h *Header) Has(k string) bool {
	for _, e := range h.kv {
		if strings.EqualFold(e.k, k) {
			return true
		}
	}
	return false
}

// Add appends a header line.
func (h *Header) Add(k, v string) { h.kv = append(h.kv, hkv{k, v}) }

// AddLine appends a "Name: value" line.
func (h *Header) AddLine(line string) {
	k, v, ok := strings.Cut(line, ":")
	if !ok {
		return
	}
	h.Add(strings.TrimSpace(k), strings.TrimLeft(v, " "))
}

// Set replaces a header's values with one (keeping its place).
func (h *Header) Set(k, v string) {
	for i, e := range h.kv {
		if strings.EqualFold(e.k, k) {
			h.kv[i].v = v
			h.delFrom(k, i+1)
			return
		}
	}
	h.Add(k, v)
}

func (h *Header) delFrom(k string, from int) {
	out := h.kv[:from]
	for _, e := range h.kv[from:] {
		if !strings.EqualFold(e.k, k) {
			out = append(out, e)
		}
	}
	h.kv = out
}

// Del removes a header.
func (h *Header) Del(k string) { h.delFrom(k, 0) }

// Keys is each header name once, in order.
func (h *Header) Keys() []string {
	var keys []string
	seen := map[string]bool{}
	for _, e := range h.kv {
		lk := strings.ToLower(e.k)
		if !seen[lk] {
			seen[lk] = true
			keys = append(keys, e.k)
		}
	}
	return keys
}

// Each calls fn for each header line.
func (h *Header) Each(fn func(k, v string)) {
	for _, e := range h.kv {
		fn(e.k, e.v)
	}
}

// Map replaces each value with fn's.
func (h *Header) Map(fn func(k, v string) string) {
	for i, e := range h.kv {
		h.kv[i].v = fn(e.k, e.v)
	}
}

// Clone copies the list.
func (h *Header) Clone() *Header { return &Header{kv: append([]hkv(nil), h.kv...)} }

// Len is the number of lines.
func (h *Header) Len() int { return len(h.kv) }

// String renders the lines as on the wire, each ending in CRLF.
func (h *Header) String() string {
	var b strings.Builder
	for _, e := range h.kv {
		b.WriteString(e.k)
		b.WriteString(": ")
		b.WriteString(e.v)
		b.WriteString("\r\n")
	}
	return b.String()
}

// FromHTTP is a header list of an http.Header (names sorted).
func FromHTTP(hh http.Header) *Header {
	h := NewHeader()
	keys := make([]string, 0, len(hh))
	for k := range hh {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		for _, v := range hh[k] {
			h.Add(k, v)
		}
	}
	return h
}
