package upstream

import (
	"encoding/base64"
	"encoding/json"
	"math"
	"strconv"
	"strings"
)

// Parameter headers (SEP-2243, MCP 2026-07-28): a tool's input schema may
// mark an argument with "x-mcp-header": "<Name>", and a client then also
// sends that argument's value in an Mcp-Param-<Name> HTTP header on
// tools/call, so servers and proxies can route on it. GitHub's remote MCP
// server requires them (for owner and repo). The Go SDK only adds them for
// tools it listed in the same session; the gateway opens one session per
// call, so it builds them itself from the approved definition. The rules
// mirror the SDK's: only string, boolean and safe-integer values; values
// with characters outside printable ASCII, or with leading or trailing
// whitespace, are sent base64-encoded as =?base64?...?=.

const (
	paramHeaderPrefix = "Mcp-Param-"
	maxParamHeaders   = 32
	maxParamValue     = 1024
)

type headerSchema struct {
	XMCPHeader json.RawMessage         `json:"x-mcp-header,omitempty"`
	Properties map[string]headerSchema `json:"properties,omitempty"`
}

// ParamHeaders returns the Mcp-Param-* headers for a tools/call with args,
// given the tool's definition as listed (its inputSchema is read). Nil when
// the tool declares none or the arguments carry none of them.
func ParamHeaders(definition, args json.RawMessage) map[string]string {
	var def struct {
		InputSchema headerSchema `json:"inputSchema"`
	}
	if json.Unmarshal(definition, &def) != nil || len(def.InputSchema.Properties) == 0 {
		return nil
	}
	var arguments map[string]json.RawMessage
	if len(args) == 0 || json.Unmarshal(args, &arguments) != nil || arguments == nil {
		return nil
	}
	out := map[string]string{}
	var walk func(props map[string]headerSchema, values map[string]json.RawMessage, depth int)
	walk = func(props map[string]headerSchema, values map[string]json.RawMessage, depth int) {
		if depth > 8 {
			return
		}
		for name, prop := range props {
			raw, ok := values[name]
			if !ok || string(raw) == "null" {
				continue
			}
			var header string
			if json.Unmarshal(prop.XMCPHeader, &header) == nil && validHeaderToken(header) && len(out) < maxParamHeaders {
				if v, ok := headerValue(raw); ok {
					out[paramHeaderPrefix+header] = v
				}
			}
			if len(prop.Properties) > 0 {
				var nested map[string]json.RawMessage
				if json.Unmarshal(raw, &nested) == nil {
					walk(prop.Properties, nested, depth+1)
				}
			}
		}
	}
	walk(def.InputSchema.Properties, arguments, 0)
	if len(out) == 0 {
		return nil
	}
	return out
}

// validHeaderToken reports whether s is a usable header-name suffix (an
// HTTP token of at most 64 characters).
func validHeaderToken(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, c := range s {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case strings.ContainsRune("!#$%&'*+-.^_`|~", c):
		default:
			return false
		}
	}
	return true
}

// headerValue encodes an argument value per SEP-2243.
func headerValue(raw json.RawMessage) (string, bool) {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return "", false
	}
	var s string
	switch x := v.(type) {
	case string:
		s = x
	case bool:
		s = strconv.FormatBool(x)
	case float64:
		const safe = 1<<53 - 1
		if math.IsNaN(x) || math.IsInf(x, 0) || x != math.Trunc(x) || x > safe || x < -safe {
			return "", false
		}
		s = strconv.FormatInt(int64(x), 10)
	default:
		return "", false
	}
	if len(s) > maxParamValue {
		return "", false
	}
	if needsBase64(s) {
		return "=?base64?" + base64.StdEncoding.EncodeToString([]byte(s)) + "?=", true
	}
	return s, true
}

func needsBase64(s string) bool {
	if s == "" {
		return false
	}
	if s[0] == ' ' || s[0] == '\t' || s[len(s)-1] == ' ' || s[len(s)-1] == '\t' {
		return true
	}
	for _, c := range s {
		if c < 0x20 || c > 0x7E {
			return true
		}
	}
	return strings.HasPrefix(s, "=?base64?") && strings.HasSuffix(s, "?=")
}
