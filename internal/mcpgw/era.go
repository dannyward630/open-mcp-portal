package mcpgw

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Protocol eras (docs/mcp-protocol.md, decision 51). The era is chosen per
// request, never remembered:
//
//   - legacy (2024-11-05 to 2025-11-25): initialize handshake, no required
//     headers, results as they always were;
//   - modern (2026-07-28 and later): stateless, every request carries
//     _meta io.modelcontextprotocol/protocolVersion and clientCapabilities
//     and the MCP-Protocol-Version, Mcp-Method and (calls and reads)
//     Mcp-Name headers, which must match the body.
//
// A request is modern when its params._meta carries a protocolVersion of
// 2026-07-28 or later (or one no version we know, which is then refused as
// unsupported) or, without one, its MCP-Protocol-Version header names
// 2026-07-28 or later. initialize and everything without an id
// (notifications) are legacy.

// Era-defining _meta keys and error codes of 2026-07-28.
const (
	metaProtocolVersion    = "io.modelcontextprotocol/protocolVersion"
	metaClientCapabilities = "io.modelcontextprotocol/clientCapabilities"
	metaServerInfo         = "io.modelcontextprotocol/serverInfo"

	// codeHeaderMismatch is HeaderMismatch: a required header is missing or
	// doesn't match the body (HTTP 400).
	codeHeaderMismatch = -32020
	// codeUnsupportedVersion is UnsupportedProtocolVersion (HTTP 400, with
	// data.supported and data.requested).
	codeUnsupportedVersion = -32022

	headerProtocolVersion = "MCP-Protocol-Version"
	headerMethod          = "Mcp-Method"
	headerName            = "Mcp-Name"
	headerParamPrefix     = "Mcp-Param-"
)

// modernVersions are the versions of the modern era this endpoint serves.
var modernVersions = []string{statelessVersion}

// allVersions is what server/discover lists: newest first.
func allVersions() []string {
	return append(slices.Clone(modernVersions), protocolVersions...)
}

var dateRE = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// modernDate reports whether v is a date-shaped version of 2026-07-28 or
// later.
func modernDate(v string) bool { return dateRE.MatchString(v) && v >= statelessVersion }

// eraState is what a request's era detection found.
type eraState struct {
	modern bool
	// version is the version the modern request asked for.
	version string
	// caps are the client's declared capabilities (modern requests).
	caps map[string]json.RawMessage
}

// eraError is a refusal of a modern request before it is processed.
type eraError struct {
	status  int
	code    int64
	message string
	data    any
}

func (e *eraError) Error() string { return e.message }

func badHeader(format string, a ...any) *eraError {
	return &eraError{status: http.StatusBadRequest, code: codeHeaderMismatch, message: "Header mismatch: " + fmt.Sprintf(format, a...)}
}

func badMeta(format string, a ...any) *eraError {
	return &eraError{status: http.StatusBadRequest, code: codeInvalidParams, message: "Invalid params: " + fmt.Sprintf(format, a...)}
}

func unsupportedVersion(requested string) *eraError {
	return &eraError{status: http.StatusBadRequest, code: codeUnsupportedVersion, message: "Unsupported protocol version",
		data: map[string]any{"supported": allVersions(), "requested": requested}}
}

// paramsOf reads a request's params as an object (nil when it isn't one)
// and its _meta.
func paramsOf(raw json.RawMessage) (params, meta map[string]json.RawMessage) {
	if len(raw) == 0 || json.Unmarshal(raw, &params) != nil {
		return nil, nil
	}
	if m, ok := params["_meta"]; ok {
		_ = json.Unmarshal(m, &meta)
	}
	return params, meta
}

// detectEra decides a request's era and, for a modern one, validates it
// (streamable-http "Server Validation"): required _meta, the version, and
// the Mcp-Method and Mcp-Name headers against the body. Tool parameter
// headers need the tool's definition and are checked with the call
// (validateParamHeaders). Only requests (a method and an id) are judged.
func detectEra(msg message, h http.Header) (eraState, *eraError) {
	if msg.Method == "" || len(msg.ID) == 0 || msg.Method == methodInitialize {
		return eraState{}, nil
	}
	params, meta := paramsOf(msg.Params)
	headerVersion := h.Get(headerProtocolVersion)
	rawVersion, metaPresent := meta[metaProtocolVersion]
	var metaVersion string
	metaIsString := metaPresent && json.Unmarshal(rawVersion, &metaVersion) == nil
	modern := false
	switch {
	case metaPresent:
		// A protocolVersion that isn't one of the legacy versions (including
		// one that isn't a string) can only be a modern request asking for
		// something we don't have.
		modern = !metaIsString || !slices.Contains(protocolVersions, metaVersion)
	default:
		modern = modernDate(headerVersion)
	}
	if !modern {
		return eraState{}, nil
	}
	st := eraState{modern: true, version: metaVersion}
	if !metaIsString || metaVersion == "" {
		return st, badMeta("_meta." + metaProtocolVersion + " is required")
	}
	rawCaps, ok := meta[metaClientCapabilities]
	if !ok {
		return st, badMeta("_meta." + metaClientCapabilities + " is required")
	}
	if err := json.Unmarshal(rawCaps, &st.caps); err != nil || st.caps == nil {
		return st, badMeta("_meta." + metaClientCapabilities + " must be an object")
	}
	switch {
	case headerVersion == "":
		return st, badHeader("the %s header is required", headerProtocolVersion)
	case headerVersion != metaVersion:
		return st, badHeader("%s header value '%s' does not match body value '%s'", headerProtocolVersion, clip(headerVersion, 50), clip(metaVersion, 50))
	case !slices.Contains(modernVersions, metaVersion):
		return st, unsupportedVersion(clip(metaVersion, 50))
	}
	method := h.Get(headerMethod)
	switch {
	case method == "":
		return st, badHeader("the %s header is required", headerMethod)
	case method != msg.Method:
		return st, badHeader("%s header value '%s' does not match body value '%s'", headerMethod, clip(method, 100), clip(msg.Method, 100))
	}
	var field string
	switch msg.Method {
	case methodToolsCall, methodPromptsGet:
		field = "name"
	case methodRead:
		field = "uri"
	}
	if field != "" {
		raw := h.Get(headerName)
		if raw == "" {
			return st, badHeader("the %s header is required for %s", headerName, msg.Method)
		}
		got, ok := decodeHeaderValue(raw)
		if !ok {
			return st, badHeader("the %s header is not a valid header value", headerName)
		}
		var want string
		if json.Unmarshal(params[field], &want) == nil && got != want {
			return st, badHeader("%s header value '%s' does not match body value '%s'", headerName, clip(got, 200), clip(want, 200))
		}
	}
	return st, nil
}

// decodeHeaderValue reads a header value that may use the Base64 sentinel
// =?base64?...?= (Mcp-Name, Mcp-Param-*). ok is false for characters a
// header may not carry unencoded (anything but printable ASCII, space and
// tab), invalid base64 or an encoded value that isn't UTF-8.
func decodeHeaderValue(v string) (string, bool) {
	for i := 0; i < len(v); i++ {
		if c := v[i]; c != '\t' && (c < 0x20 || c > 0x7e) {
			return "", false
		}
	}
	const prefix, suffix = "=?base64?", "?="
	if len(v) >= len(prefix)+len(suffix) && strings.HasPrefix(v, prefix) && strings.HasSuffix(v, suffix) {
		b, err := base64.StdEncoding.DecodeString(v[len(prefix) : len(v)-len(suffix)])
		if err != nil || !utf8.Valid(b) {
			return "", false
		}
		return string(b), true
	}
	return v, true
}

// ParamBinding is an argument a tool mirrors into a header: its x-mcp-header
// name and the path of properties leading to it (SEP-2243). Only properties
// reachable through "properties" alone count, as the spec requires.
type paramBinding struct {
	header string // Mcp-Param-<name>
	path   []string
}

type bindingSchema struct {
	XMCPHeader json.RawMessage          `json:"x-mcp-header,omitempty"`
	Properties map[string]bindingSchema `json:"properties,omitempty"`
}

const maxBindingDepth = 8

// paramBindings lists the headers a tool's definition (as approved) asks
// for, in a stable order.
func paramBindings(definition json.RawMessage) []paramBinding {
	var def struct {
		InputSchema bindingSchema `json:"inputSchema"`
	}
	if json.Unmarshal(definition, &def) != nil {
		return nil
	}
	var out []paramBinding
	var walk func(props map[string]bindingSchema, path []string)
	walk = func(props map[string]bindingSchema, path []string) {
		if len(path) >= maxBindingDepth {
			return
		}
		names := make([]string, 0, len(props))
		for n := range props {
			names = append(names, n)
		}
		slices.Sort(names)
		for _, n := range names {
			p := props[n]
			here := append(slices.Clone(path), n)
			var name string
			if json.Unmarshal(p.XMCPHeader, &name) == nil && validHeaderToken(name) {
				out = append(out, paramBinding{header: headerParamPrefix + name, path: here})
			}
			walk(p.Properties, here)
		}
	}
	walk(def.InputSchema.Properties, nil)
	return out
}

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

// validateParamHeaders checks the Mcp-Param-* headers of a modern tools/call
// against its arguments, for a tool whose definition has x-mcp-header: each
// argument that is present (and not null) must have its header, equal after
// decoding; an absent or null one must have none. Integers compare as
// numbers.
func validateParamHeaders(h http.Header, definition, args json.RawMessage) *eraError {
	bindings := paramBindings(definition)
	if len(bindings) == 0 {
		return nil
	}
	var arguments map[string]json.RawMessage
	if t := bytes.TrimSpace(args); len(t) > 0 {
		_ = json.Unmarshal(t, &arguments)
	}
	for _, b := range bindings {
		values := h.Values(b.header)
		value, present := lookupPath(arguments, b.path)
		if !present || string(bytes.TrimSpace(value)) == "null" {
			if len(values) > 0 {
				return badHeader("unexpected %s header for the absent or null argument %q", b.header, strings.Join(b.path, "."))
			}
			continue
		}
		if len(values) != 1 {
			return badHeader("the %s header is required once for the argument %q", b.header, strings.Join(b.path, "."))
		}
		decoded, ok := decodeHeaderValue(values[0])
		if !ok {
			return badHeader("the %s header is not a valid header value", b.header)
		}
		if !paramEqual(decoded, value) {
			return badHeader("%s header value does not match the argument %q", b.header, strings.Join(b.path, "."))
		}
	}
	return nil
}

func lookupPath(obj map[string]json.RawMessage, path []string) (json.RawMessage, bool) {
	for i, p := range path {
		raw, ok := obj[p]
		if !ok {
			return nil, false
		}
		if i == len(path)-1 {
			return raw, true
		}
		var next map[string]json.RawMessage
		if json.Unmarshal(raw, &next) != nil {
			return nil, false
		}
		obj = next
	}
	return nil, false
}

// paramEqual compares a decoded header value with the JSON argument it
// mirrors: strings and booleans as text, integers as numbers (42.0 equals
// 42, as the spec asks); anything else never matches.
func paramEqual(header string, raw json.RawMessage) bool {
	var v any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if dec.Decode(&v) != nil {
		return false
	}
	switch x := v.(type) {
	case string:
		return header == x
	case bool:
		return header == strconv.FormatBool(x)
	case json.Number:
		const safe = 1<<53 - 1
		want, err := x.Float64()
		got, err2 := strconv.ParseFloat(header, 64)
		if err != nil || err2 != nil || math.IsNaN(got) || math.IsInf(got, 0) || got != math.Trunc(got) || want != math.Trunc(want) ||
			math.Abs(want) > safe || math.Abs(got) > safe {
			return false
		}
		return got == want
	}
	return false
}
