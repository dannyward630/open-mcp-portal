package upstream

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Capability kinds.
const (
	KindTool             = "tool"
	KindResource         = "resource"
	KindResourceTemplate = "resource_template"
	KindPrompt           = "prompt"
)

// Kinds lists the capability kinds in display order.
var Kinds = []string{KindTool, KindResource, KindResourceTemplate, KindPrompt}

// ValidKind reports whether k is a capability kind.
func ValidKind(k string) bool {
	for _, x := range Kinds {
		if x == k {
			return true
		}
	}
	return false
}

// Bounds of what is stored per server and capability (docs/registry.md,
// "Limits").
const (
	// MaxPerKind bounds how many tools (resources, resource templates,
	// prompts) one server may list; a longer list fails discovery.
	MaxPerKind = 500
	// maxPages bounds the list pages read per kind.
	maxPages = MaxPerKind
	// MaxName bounds a name or URI; longer entries are skipped.
	MaxName = 1000
	// MaxTitle and MaxDescription clip what is stored (the definition
	// holds the clipped values too, so it matches the columns).
	MaxTitle       = 200
	MaxDescription = 10000
	// MaxDefinition bounds one capability's canonical definition; a larger
	// one fails discovery.
	MaxDefinition = 64 << 10
)

// Capability is one tool, resource, resource template or prompt as a
// server lists it.
type Capability struct {
	Kind        string
	Name        string // tool or prompt name, resource URI, or URI template
	Title       string
	Description string
	// Definition is the canonical JSON of the full listed object (without
	// _meta, which servers use for volatile data).
	Definition  json.RawMessage
	Fingerprint string
}

// Canonical re-encodes a JSON document canonically: object keys sorted,
// no insignificant whitespace, numbers as written, no HTML escaping. Two
// documents with the same value have the same canonical form.
func Canonical(doc []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(doc))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return encodeCanonical(v)
}

func encodeCanonical(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// Fingerprint is the SHA-256 (hex) of a document's canonical JSON.
func Fingerprint(canonical []byte) string {
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])
}

// newCapability builds a Capability from a listed object (an SDK struct
// whose title and description are already clipped).
func newCapability(kind, name, title, description string, listed any) (Capability, error) {
	raw, err := json.Marshal(listed)
	if err != nil {
		return Capability{}, &Error{Class: ClassBadResponse, Message: fmt.Sprintf("The server listed a %s that can't be read.", kindLabel(kind))}
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return Capability{}, &Error{Class: ClassBadResponse, Message: fmt.Sprintf("The server listed a %s that can't be read.", kindLabel(kind))}
	}
	if m, ok := v.(map[string]any); ok {
		delete(m, "_meta")
	}
	def, err := encodeCanonical(v)
	if err != nil {
		return Capability{}, err
	}
	if len(def) > MaxDefinition {
		return Capability{}, &Error{Class: ClassTooLarge,
			Message: fmt.Sprintf("The %s %q has a definition larger than %d KiB.", kindLabel(kind), clip(name, 100), MaxDefinition>>10)}
	}
	return Capability{Kind: kind, Name: name, Title: title, Description: description, Definition: def, Fingerprint: Fingerprint(def)}, nil
}

func kindLabel(kind string) string {
	return strings.ReplaceAll(kind, "_", " ")
}

// validName reports whether a name or URI can be stored.
func validName(s string) bool {
	if s == "" || len(s) > MaxName || !utf8.ValidString(s) {
		return false
	}
	return strings.IndexFunc(s, unicode.IsControl) < 0
}

// clip makes s valid UTF-8 of at most n characters.
func clip(s string, n int) string {
	s = strings.ToValidUTF8(s, "")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// Counts are a discovery's capabilities per kind.
type Counts struct {
	Tools, Resources, ResourceTemplates, Prompts int
}

// CountKinds counts capabilities per kind.
func CountKinds(caps []Capability) Counts {
	var c Counts
	for _, cp := range caps {
		switch cp.Kind {
		case KindTool:
			c.Tools++
		case KindResource:
			c.Resources++
		case KindResourceTemplate:
			c.ResourceTemplates++
		case KindPrompt:
			c.Prompts++
		}
	}
	return c
}
