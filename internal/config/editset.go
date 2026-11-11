package config

import (
	"fmt"
	"runtime"
	"strconv"
	"strings"

	"github.com/D-Modem/webone-go/internal/logx"
	"github.com/D-Modem/webone-go/internal/rx"
)

// Condition rules of edit sets
// (https://github.com/atauenis/webone/wiki/Sets-of-edits).
const (
	OnUrl         = "OnUrl"
	OnCode        = "OnCode"
	IgnoreUrl     = "IgnoreUrl"
	OnContentType = "OnContentType"
	OnHeader      = "OnHeader"
	OnHostOS      = "OnHostOS"
	OnHttpOnly    = "OnHttpOnly"
	OnHttpsOnly   = "OnHttpsOnly"
	OnUser        = "OnUser"
)

// Action rules of edit sets.
const (
	AddRedirect                  = "AddRedirect"
	AddInternalRedirect          = "AddInternalRedirect"
	AddFind                      = "AddFind"
	AddReplace                   = "AddReplace"
	AddFindReplace               = "AddFindReplace"
	AddConvert                   = "AddConvert"
	AddConvertDest               = "AddConvertDest"
	AddConvertArg1               = "AddConvertArg1"
	AddConvertArg2               = "AddConvertArg2"
	AddConverting                = "AddConverting"
	AddRequestHeaderFind         = "AddRequestHeaderFind"
	AddRequestHeaderReplace      = "AddRequestHeaderReplace"
	AddRequestHeaderFindReplace  = "AddRequestHeaderFindReplace"
	AddResponseHeaderFind        = "AddResponseHeaderFind"
	AddResponseHeaderReplace     = "AddResponseHeaderReplace"
	AddResponseHeaderFindReplace = "AddResponseHeaderFindReplace"
	AddHeaderDumping             = "AddHeaderDumping"
	AddRequestDumping            = "AddRequestDumping"
	AddDumping                   = "AddDumping"
	AddOutputEncoding            = "AddOutputEncoding"
	AddTranslit                  = "AddTranslit"
	AddDebugPrint                = "AddDebugPrint"
	AddHeader                    = "AddHeader"
	AddRequestHeader             = "AddRequestHeader"
	AddResponseHeader            = "AddResponseHeader"
	AddRequestHttpVersion        = "AddRequestHttpVersion"
	AddResponseHttpVersion       = "AddResponseHttpVersion"
	AddVariable                  = "AddVariable"
)

// EditRule is one action of an edit set.
type EditRule struct {
	Action string
	Value  string   // the value (parameters joined)
	Params []string // the quoted parameters, if any

	// AddFindReplace, AddRequestHeaderFindReplace, AddResponseHeaderFindReplace
	Find, Replace string
	// AddConverting
	Converter, ConvertDest, ConvertArg1, ConvertArg2 string
}

// EditSet is an [Edit] section: conditions, then the edits to make.
type EditSet struct {
	Location         string
	UrlMasks         []string
	UrlIgnoreMasks   []string
	ContentTypeMasks []string
	OnCode           *int
	HttpOnly         bool
	HttpsOnly        bool
	HeaderMasks      []string
	Users            []string
	CorrectHostOS    bool
	// IsForRequest: the set can be applied when the request is made, as
	// it needs nothing from the response
	IsForRequest bool
	Edits        []EditRule
}

// NewEditSet builds an edit set from an [Edit] or [Edit:mask] section.
// authenticate and converters are what is configured so far, for warnings.
func NewEditSet(s *Section, c *Config) (*EditSet, error) {
	e := &EditSet{Location: s.Location, CorrectHostOS: true}
	mayBeForResponse := false
	if s.HasMask {
		if err := checkRegExp(s.Mask, s.Location); err != nil {
			return nil, err
		}
		e.UrlMasks = append(e.UrlMasks, s.Mask)
	}
	for _, line := range s.Options {
		if !line.HasKeyValue() {
			continue
		}
		switch line.Key {
		case OnUrl:
			if err := checkRegExp(line.Value, line.Location); err != nil {
				return nil, err
			}
			e.UrlMasks = append(e.UrlMasks, line.Value)
			continue
		case OnCode:
			n, err := strconv.Atoi(strings.TrimSpace(line.Value))
			if err != nil {
				return nil, fmt.Errorf("bad OnCode at %s", line.Location)
			}
			e.OnCode = &n
			continue
		case IgnoreUrl:
			if err := checkRegExp(line.Value, line.Location); err != nil {
				return nil, err
			}
			e.UrlIgnoreMasks = append(e.UrlIgnoreMasks, line.Value)
			continue
		case OnContentType:
			if err := checkRegExp(line.Value, line.Location); err != nil {
				return nil, err
			}
			e.ContentTypeMasks = append(e.ContentTypeMasks, line.Value)
			continue
		case OnHeader:
			if err := checkRegExp(line.Value, line.Location); err != nil {
				return nil, err
			}
			e.HeaderMasks = append(e.HeaderMasks, line.Value)
			continue
		case OnHostOS:
			switch strings.ToLower(line.Value) {
			case "windows":
				e.CorrectHostOS = runtime.GOOS == "windows"
			case "linux":
				e.CorrectHostOS = runtime.GOOS == "linux"
			case "macos":
				e.CorrectHostOS = runtime.GOOS == "darwin"
			default:
				logx.Server(true, "Warning: unknown host OS \"%s\", edit set disabled.", line.Value)
				e.CorrectHostOS = false
			}
			continue
		case OnHttpOnly:
			e.HttpOnly = ToBool(line.Value)
			continue
		case OnHttpsOnly:
			e.HttpsOnly = ToBool(line.Value)
			continue
		case OnUser:
			for _, user := range line.ValuesOrValue() {
				e.Users = append(e.Users, user)
				found := false
				for _, reg := range c.Authenticate {
					if strings.HasPrefix(reg, user+":") {
						found = true
					}
				}
				if !found {
					logx.Server(true, "Warning: unknown user \"%s\" at %s.", user, line.Location)
				}
			}
			continue

		case AddRedirect, AddInternalRedirect, AddFind, AddReplace:
			if err := checkRegExp(line.Value, line.Location); err != nil {
				return nil, err
			}
			e.Edits = append(e.Edits, EditRule{Action: line.Key, Value: line.Value})
		case AddConvert, AddConvertDest, AddConvertArg1, AddConvertArg2,
			AddRequestHeaderFind, AddRequestHeaderReplace, AddResponseHeaderFind, AddResponseHeaderReplace,
			AddHeaderDumping, AddRequestDumping, AddDumping, AddOutputEncoding, AddTranslit, AddDebugPrint:
			e.Edits = append(e.Edits, EditRule{Action: line.Key, Value: line.Value, Params: line.ValuesOrValue()})
		case AddHeader, AddRequestHeader, AddResponseHeader:
			if strings.Contains(line.Value, ": ") {
				e.Edits = append(e.Edits, EditRule{Action: line.Key, Value: line.Value, Params: line.ValuesOrValue()})
			} else {
				logx.Server(true, "Warning: Incorrect HTTP header at %s. Line ignored.", line.Location)
			}
		case AddRequestHttpVersion:
			if v, ok := ParseHTTPVersionRule(line.Value); ok {
				e.Edits = append(e.Edits, EditRule{Action: AddRequestHttpVersion, Value: v})
			} else {
				logx.Server(true, "Warning: Incorrect HTTP version at %s. Line ignored.", line.Location)
			}
		case AddResponseHttpVersion:
			if _, _, ok := ParseVersion(line.Value); ok {
				e.Edits = append(e.Edits, EditRule{Action: line.Key, Value: line.Value})
			} else {
				logx.Server(true, "Warning: Incorrect HTTP version at %s. Line ignored.", line.Location)
			}
		case AddVariable:
			p := line.ValuesOrValue()
			switch len(p) {
			case 2, 3:
				e.Edits = append(e.Edits, EditRule{Action: line.Key, Value: line.Value, Params: p})
			case 4:
				if n, err := strconv.Atoi(p[3]); err == nil && n >= 0 {
					e.Edits = append(e.Edits, EditRule{Action: line.Key, Value: line.Value, Params: p})
				} else {
					logx.Server(true, "Warning: invalid group number [4th argument] at %s. Line ignored.", line.Location)
				}
			default:
				logx.Server(true, "Warning: bad count of arguments of AddVariable at %s. Line ignored.", line.Location)
			}
		default:
			if strings.HasPrefix(line.Key, "Add") {
				logx.Server(true, "Warning: unknown edit action \"%s\" at %s. Line ignored.", line.Key, line.Location)
			} else {
				logx.Server(true, "Warning: unknown edit set condition \"%s\" at %s. Line ignored.", line.Key, line.Location)
			}
		}
		if strings.HasPrefix(line.Key, AddConvert) || line.Key == AddFind || line.Key == AddReplace {
			mayBeForResponse = true
		}
		if line.Key == AddInternalRedirect {
			mayBeForResponse = false
		}
	}
	e.processComplexRules(c)
	if len(e.ContentTypeMasks) == 0 && !mayBeForResponse {
		e.IsForRequest = true
	}
	if len(e.UrlMasks) == 0 {
		e.UrlMasks = append(e.UrlMasks, ".*")
	}
	return e, nil
}

// processComplexRules joins AddFind+AddReplace into AddFindReplace (and the
// header variants), and AddConvert* into AddConverting.
func (e *EditSet) processComplexRules(c *Config) {
	var finds, repls, rqFinds, rqRepls, rsFinds, rsRepls []string
	var converter, dest, arg1, arg2 string
	for _, r := range e.Edits {
		switch r.Action {
		case AddFind:
			finds = append(finds, r.Value)
		case AddReplace:
			repls = append(repls, r.Value)
		case AddRequestHeaderFind:
			rqFinds = append(rqFinds, r.Value)
		case AddRequestHeaderReplace:
			rqRepls = append(rqRepls, r.Value)
		case AddResponseHeaderFind:
			rsFinds = append(rsFinds, r.Value)
		case AddResponseHeaderReplace:
			rsRepls = append(rsRepls, r.Value)
		case AddConvert:
			converter = r.Value
		case AddConvertDest:
			dest = r.Value
		case AddConvertArg1:
			arg1 = r.Value
		case AddConvertArg2:
			arg2 = r.Value
		}
	}
	pair := func(f, r []string, action, what string) {
		if len(f) != len(r) {
			logx.Server(true, "Warning: Invalid amount of %sfinds/replaces in %s.", what, e.Location)
			return
		}
		for i := range f {
			e.Edits = append(e.Edits, EditRule{Action: action, Find: f[i], Replace: r[i]})
		}
	}
	pair(finds, repls, AddFindReplace, "")
	pair(rqFinds, rqRepls, AddRequestHeaderFindReplace, "request header ")
	pair(rsFinds, rsRepls, AddResponseHeaderFindReplace, "response header ")

	if converter != "" {
		ok := false
		for _, cv := range c.Converters {
			if cv.Executable == converter {
				e.Edits = append(e.Edits, EditRule{Action: AddConverting, Converter: converter, ConvertDest: dest, ConvertArg1: arg1, ConvertArg2: arg2})
				ok = true
				break
			}
		}
		if !ok {
			logx.Server(true, "Warning: Converter \"%s\" in Edit Set at %s is not present in lists of converters.", converter, e.Location)
		}
	} else if dest != "" || arg1 != "" || arg2 != "" {
		logx.Server(true, "Warning: Please add AddConvert rule to Edit Set starting at %s to use converting.", e.Location)
	}
}

// CheckCode: an OnCode condition (nil: any) admits an HTTP status code.
// 0 is any below 300; 2, 3 and 4 are 2xx, 3xx and 4xx-or-above.
func CheckCode(expected *int, code int) bool {
	if expected == nil {
		return true
	}
	switch {
	case *expected == code:
		return true
	case *expected == 0 && code < 300:
		return true
	case *expected == 2 && code >= 200 && code < 300:
		return true
	case *expected == 3 && code >= 300 && code < 400:
		return true
	case *expected == 4 && code > 399:
		return true
	}
	return false
}

func checkRegExp(pattern, location string) error {
	if pattern == "" {
		return nil
	}
	if err := rx.Check(pattern); err != nil {
		return fmt.Errorf("problem with regular expression \"%v\" at %s", err, location)
	}
	return nil
}
