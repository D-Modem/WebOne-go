// Package rx is WebOne's regular expressions: .NET's syntax and
// replacement rules ($1, ${name}, lookarounds, backreferences), which
// configuration files are written in, through regexp2, with compiled
// patterns cached.
package rx

import (
	"sync"
	"time"

	"github.com/dlclark/regexp2"
)

// Timeout bounds one match, as WebOne's RegexTimeout does.
const Timeout = 5 * time.Second

type key struct {
	pattern string
	opts    regexp2.RegexOptions
}

var cache sync.Map // key -> *regexp2.Regexp

// Options for Get.
const (
	None       = regexp2.None
	IgnoreCase = regexp2.IgnoreCase
	Singleline = regexp2.Singleline
)

// Get compiles a pattern (cached).
func Get(pattern string, opts regexp2.RegexOptions) (*regexp2.Regexp, error) {
	k := key{pattern, opts}
	if r, ok := cache.Load(k); ok {
		return r.(*regexp2.Regexp), nil
	}
	r, err := regexp2.Compile(pattern, opts)
	if err != nil {
		return nil, err
	}
	r.MatchTimeout = Timeout
	cache.Store(k, r)
	return r, nil
}

// Check reports whether a pattern compiles.
func Check(pattern string) error {
	_, err := Get(pattern, None)
	return err
}

// Match: pattern matches s. A bad pattern matches nothing.
func Match(pattern, s string) bool {
	return MatchOpt(pattern, s, None)
}

// MatchOpt is Match with options.
func MatchOpt(pattern, s string, opts regexp2.RegexOptions) bool {
	r, err := Get(pattern, opts)
	if err != nil {
		return false
	}
	ok, err := r.MatchString(s)
	return err == nil && ok
}

// AnyMatches: one of patterns matches s (Program.CheckStringRegExp(What, For[])).
func AnyMatches(s string, patterns []string) bool {
	for _, p := range patterns {
		if Match(p, s) {
			return true
		}
	}
	return false
}

// Replace replaces every match of pattern in s with repl (.NET substitution
// syntax). On a bad pattern or a timeout s comes back unchanged.
func Replace(pattern, s, repl string, opts regexp2.RegexOptions) string {
	r, err := Get(pattern, opts)
	if err != nil {
		return s
	}
	out, err := r.Replace(s, repl, -1, -1)
	if err != nil {
		return s
	}
	return out
}

// GroupCount is the number of groups in pattern, the whole match included.
func GroupCount(pattern string) int {
	r, err := Get(pattern, None)
	if err != nil {
		return 0
	}
	return len(r.GetGroupNumbers())
}

// FindGroups is the first match's groups (index 0 the whole match), or nil.
func FindGroups(pattern, s string) []string {
	r, err := Get(pattern, None)
	if err != nil {
		return nil
	}
	m, err := r.FindStringMatch(s)
	if err != nil || m == nil {
		return nil
	}
	var out []string
	for _, g := range m.Groups() {
		out = append(out, g.String())
	}
	return out
}

// FindGroupsOK is FindGroups with each group's success.
func FindGroupsOK(pattern, s string) (groups []string, ok []bool) {
	r, err := Get(pattern, None)
	if err != nil {
		return nil, nil
	}
	m, err := r.FindStringMatch(s)
	if err != nil || m == nil {
		return nil, nil
	}
	for _, g := range m.Groups() {
		groups = append(groups, g.String())
		ok = append(ok, len(g.Captures) > 0)
	}
	return groups, ok
}
