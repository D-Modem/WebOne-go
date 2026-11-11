package config

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// Vars is an ordered set of named strings, used as %Name% or $Name in
// configuration values, edit sets and internal pages.
type Vars struct {
	keys []string
	m    map[string]string
}

// NewVars is an empty set.
func NewVars() *Vars { return &Vars{m: map[string]string{}} }

// Set sets (or replaces) a variable.
func (v *Vars) Set(k, val string) {
	if v.m == nil {
		v.m = map[string]string{}
	}
	if _, ok := v.m[k]; !ok {
		v.keys = append(v.keys, k)
	}
	v.m[k] = val
}

// TryAdd sets a variable that is not set yet.
func (v *Vars) TryAdd(k, val string) {
	if _, ok := v.m[k]; ok {
		return
	}
	v.Set(k, val)
}

// Get is a variable's value.
func (v *Vars) Get(k string) (string, bool) {
	if v == nil {
		return "", false
	}
	s, ok := v.m[k]
	return s, ok
}

// Value is a variable's value, or "".
func (v *Vars) Value(k string) string { s, _ := v.Get(k); return s }

// Keys in order of first setting.
func (v *Vars) Keys() []string {
	if v == nil {
		return nil
	}
	return v.keys
}

// Clone copies the set.
func (v *Vars) Clone() *Vars {
	c := NewVars()
	if v != nil {
		for _, k := range v.keys {
			c.Set(k, v.m[k])
		}
	}
	return c
}

// FromMap is a set of the map's entries (sorted by name).
func FromMap(m map[string]string) *Vars {
	v := NewVars()
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v.Set(k, m[k])
	}
	return v
}

// DefaultLogDir is where a log file goes by default (%SYSLOGDIR%).
func DefaultLogDir() string {
	switch runtime.GOOS {
	case "windows":
		if d, err := os.UserConfigDir(); err == nil {
			return d
		}
	case "darwin":
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, "Library", "Logs")
		}
	default:
		return "/var/log"
	}
	d, _ := os.Getwd()
	return d
}

// tempDir is %TEMP% / $TMPDIR, with a trailing separator as .NET's
// Path.GetTempPath() has.
func tempDir() string {
	d := os.TempDir()
	if !strings.HasSuffix(d, string(os.PathSeparator)) {
		d += string(os.PathSeparator)
	}
	return d
}

// replaceFold replaces old with repl, ignoring case.
func replaceFold(s, old, repl string) string {
	if old == "" {
		return s
	}
	lo, lold := strings.ToLower(s), strings.ToLower(old)
	if !strings.Contains(lo, lold) || len(lo) != len(s) {
		if len(lo) != len(s) {
			// case folding changed byte lengths; fall back to exact matching
			return strings.ReplaceAll(s, old, repl)
		}
		return s
	}
	var b strings.Builder
	i := 0
	for {
		j := strings.Index(lo[i:], lold)
		if j < 0 {
			b.WriteString(s[i:])
			break
		}
		b.WriteString(s[i : i+j])
		b.WriteString(repl)
		i += j + len(old)
	}
	return b.String()
}

// Expand replaces %Name% and $Name with the values of global variables,
// then of extra ones (globals win), then of environment variables. %TEMP%,
// $TMPDIR, %SYSLOGDIR% and $SYSLOGDIR are always known, and "~/" is the
// home directory.
//
// Ported from Program.ExpandMaskedVariables. Unlike the original, $Name is
// replaced longest name first, so $UrlDomain is not taken for $Url.
func Expand(s string, global, extra *Vars) string {
	if !strings.ContainsAny(s, "%$~") {
		return s
	}
	tmp, logdir := tempDir(), DefaultLogDir()
	s = strings.ReplaceAll(s, "$TMPDIR", tmp)
	s = replaceFold(s, "%TEMP%", tmp)
	s = strings.ReplaceAll(s, "$SYSLOGDIR", logdir)
	s = replaceFold(s, "%SYSLOGDIR%", logdir)

	all := global.Clone()
	if extra != nil {
		for _, k := range extra.keys {
			all.TryAdd(k, extra.m[k])
		}
	}
	s = replaceVars(s, all, false)

	env := NewVars()
	for _, kv := range os.Environ() {
		if i := strings.IndexByte(kv, '='); i > 0 {
			env.Set(kv[:i], kv[i+1:])
		}
	}
	s = replaceVars(s, env, true)
	if strings.Contains(s, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			s = strings.ReplaceAll(s, "~/", h+"/")
		}
	}
	return s
}

func replaceVars(s string, v *Vars, foldPercent bool) string {
	if !strings.ContainsAny(s, "%$") {
		return s
	}
	for _, k := range v.keys {
		if foldPercent {
			s = replaceFold(s, "%"+k+"%", v.m[k])
		} else {
			s = strings.ReplaceAll(s, "%"+k+"%", v.m[k])
		}
	}
	if !strings.Contains(s, "$") {
		return s
	}
	keys := append([]string(nil), v.keys...)
	sort.SliceStable(keys, func(i, j int) bool { return len(keys[i]) > len(keys[j]) })
	for _, k := range keys {
		if k == "" {
			continue
		}
		s = strings.ReplaceAll(s, "$"+k, v.m[k])
	}
	return s
}
