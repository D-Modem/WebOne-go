// Package config reads WebOne configuration files: an INI-like format whose
// sections may also hold raw lists (ForceHttps, TextTypes ...), may repeat
// (Edit), and may pull in other files ([Include:path]).
//
// Ported from WebOne's ConfigFileEntry.cs, ConfigFileLoader.cs and
// ConfigFile.cs.
package config

import (
	"fmt"
	"strings"
)

// Option is one line inside a section: "Key=Value" or a raw string.
type Option struct {
	Key      string   // empty for a raw line
	Value    string   // the value, unescaped; the whole line for a raw line
	Values   []string // the quoted parts of an escaped value ("a", "b"); nil if not escaped
	Raw      string   // the line as written
	Location string   // "file, line N"
}

// HasKeyValue: the line is "Key=Value", not a raw list item.
func (o Option) HasKeyValue() bool { return o.Key != "" }

// ValuesOrValue is Values, or Value alone when the value was not quoted.
func (o Option) ValuesOrValue() []string {
	if o.Values != nil {
		return o.Values
	}
	return []string{o.Value}
}

func (o Option) String() string { return o.Raw }

// ParseOption parses a section line. Values that start with a double quote
// (or a backslash) use C-like escapes, and may hold several quoted parts
// separated by spaces, tabs or commas, ending at ';'.
func ParseOption(raw, location string) (Option, error) {
	o := Option{Raw: raw, Location: location}
	i := strings.IndexByte(raw, '=')
	if i < 0 {
		o.Value = raw
		return o, nil
	}
	o.Key = raw[:i]
	o.Value = raw[i+1:]
	if o.Key == "" {
		// "=x" has no key: WebOne treats an empty key as a raw line
		o.Value = raw
		return o, nil
	}
	v := o.Value
	if !(strings.HasPrefix(v, `"`) || strings.HasPrefix(v, `\`)) {
		return o, nil
	}

	var values []string
	var sb strings.Builder
	escape := false
	inString := v[0] == '"'
	gap := false
	rs := []rune(v)
loop:
	for i := 1; i < len(rs); i++ {
		c := rs[i]
		if !gap {
			if !escape && c == '\\' {
				escape = true
				continue
			}
			if escape {
				escape = false
				switch c {
				case '\'':
					sb.WriteRune('\'')
				case '"':
					sb.WriteRune('"')
				case '\\':
					sb.WriteRune('\\')
				case '0':
					sb.WriteRune(0)
				case 'a':
					sb.WriteRune('\a')
				case 'b':
					sb.WriteRune('\b')
				case 'f':
					sb.WriteRune('\f')
				case 'n':
					sb.WriteRune('\n')
				case 'r':
					sb.WriteRune('\r')
				case 't':
					sb.WriteRune('\t')
				case 'v':
					sb.WriteRune('\v')
				default:
					return o, fmt.Errorf("unknown escape sequence: %c", c)
				}
				continue
			}
			if c == '"' && inString {
				values = append(values, sb.String())
				sb.Reset()
				gap = true
				inString = false
				continue
			}
			sb.WriteRune(c)
			continue
		}
		// the gap between quoted values
		switch c {
		case ' ', '\t', ',':
			continue
		case '"':
			gap = false
			inString = true
		case ';':
			break loop
		default:
			return o, fmt.Errorf("garbage in the middle gap: %c", c)
		}
	}
	if len(values) == 0 {
		values = append(values, sb.String())
	}
	o.Values = values
	o.Value = strings.Join(values, "")
	return o, nil
}

// Section is a "[Kind]" or "[Kind:Mask]" section with its lines.
type Section struct {
	Title    string
	Kind     string
	Mask     string
	HasMask  bool
	Location string
	Options  []Option
}

// ParseSection parses a "[Title]" line.
func ParseSection(raw, location string) (*Section, error) {
	if !(strings.HasPrefix(raw, "[") && strings.HasSuffix(raw, "]")) || len(raw) < 2 {
		return nil, fmt.Errorf("invalid section title")
	}
	s := &Section{Title: raw[1 : len(raw)-1], Location: location}
	if i := strings.IndexByte(s.Title, ':'); i >= 0 {
		s.Kind = s.Title[:i]
		s.Mask = s.Title[i+1:]
		s.HasMask = true
	} else {
		s.Kind = s.Title
	}
	return s, nil
}

func (s *Section) String() string { return "[" + s.Title + "]" }
