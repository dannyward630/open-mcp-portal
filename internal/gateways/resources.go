package gateways

import (
	"regexp"
	"strings"

	"github.com/ncecere/open-mcp-gateway/internal/upstream"
)

// Resources and resource templates (docs/phase3-gateways.md, "Resources").
//
// Clients see a resource's URI as omcp://{serverSlug}/{originalUri}, and a
// template as the same literal prefix before the original template, so its
// RFC 6570 variables are unchanged. A read strips the prefix, finds the
// server by slug, and is allowed only for the URI of a selected resource or
// a URI matching a selected template.

// ExposedScheme starts every exposed resource URI.
const ExposedScheme = "omcp://"

// IsResourceKind reports whether kind is a resource or resource template.
func IsResourceKind(kind string) bool {
	return kind == upstream.KindResource || kind == upstream.KindResourceTemplate
}

// ExposedURI is the URI (or URI template) clients see for a server's
// resource (or template): omcp://{serverSlug}/{original}.
func ExposedURI(serverSlug, original string) string {
	return ExposedScheme + serverSlug + "/" + original
}

// SplitExposedURI reverses ExposedURI: the server slug and the original URI.
// ok is false when uri isn't an exposed URI.
func SplitExposedURI(uri string) (serverSlug, original string, ok bool) {
	rest, found := strings.CutPrefix(uri, ExposedScheme)
	if !found {
		return "", "", false
	}
	serverSlug, original, found = strings.Cut(rest, "/")
	if !found || original == "" || !slugRE.MatchString(serverSlug) {
		return "", "", false
	}
	return serverSlug, original, true
}

// Exposed is the name (tools, prompts) or URI (resources, templates)
// clients see for a capability.
func Exposed(kind, serverSlug, name string) string {
	if IsResourceKind(kind) {
		return ExposedURI(serverSlug, name)
	}
	return ExposedName(serverSlug, name)
}

// varname is an RFC 6570 variable name: varchar *( ["."] varchar ), where a
// varchar is ALPHA / DIGIT / "_" / pct-encoded (a leading pct-encoded
// character is not accepted). Kept in step with ListGatewayOptions.
const varname = `[A-Za-z0-9_](?:[A-Za-z0-9_.]|%[0-9A-Fa-f]{2})*`

var (
	supportedTemplateRE = regexp.MustCompile(`^(?:[^{}]|\{\+?` + varname + `\})*$`)
	expressionRE        = regexp.MustCompile(`\{(\+?)` + varname + `\}`)
)

// TemplateSupported reports whether a URI template uses only expressions
// the gateway can match: RFC 6570 level 1 simple {var} and level 2 reserved
// {+var}, one variable each. Templates with other operators ({#var},
// {/var}, {?var}, …), several variables or modifiers can't be selected.
func TemplateSupported(t string) bool { return supportedTemplateRE.MatchString(t) }

// Characters an expansion may produce: simple expansion allows unreserved
// characters and percent-encoded octets; reserved expansion also allows
// reserved characters (RFC 6570 §3.2.2, §3.2.3).
const (
	simpleValue   = `(?:[A-Za-z0-9\-._~]|%[0-9A-Fa-f]{2})+`
	reservedValue = `(?:[A-Za-z0-9\-._~:/?#\[\]@!$&'()*+,;=]|%[0-9A-Fa-f]{2})+`
)

// Template matches URIs against a supported URI template.
type Template struct {
	re *regexp.Regexp
}

// CompileTemplate compiles a supported template (ok false otherwise).
func CompileTemplate(t string) (Template, bool) {
	if !TemplateSupported(t) {
		return Template{}, false
	}
	var b strings.Builder
	b.WriteString("^")
	last := 0
	for _, m := range expressionRE.FindAllStringSubmatchIndex(t, -1) {
		b.WriteString(regexp.QuoteMeta(t[last:m[0]]))
		if m[3] > m[2] { // {+var}
			b.WriteString("(" + reservedValue + ")")
		} else {
			b.WriteString("(" + simpleValue + ")")
		}
		last = m[1]
	}
	b.WriteString(regexp.QuoteMeta(t[last:]))
	b.WriteString("$")
	re, err := regexp.Compile(b.String())
	if err != nil {
		return Template{}, false
	}
	return Template{re: re}, true
}

// Match reports whether uri is an expansion of the template. Every variable
// must expand to at least one character, and no expansion may contain a
// "." or ".." path segment (also percent-encoded, or separated by an
// encoded slash or backslash), so a template can't be used to step outside
// the paths it names.
func (t Template) Match(uri string) bool {
	if t.re == nil {
		return false
	}
	m := t.re.FindStringSubmatch(uri)
	if m == nil {
		return false
	}
	for _, v := range m[1:] {
		v = strings.NewReplacer("%2e", ".", "%2f", "/", "%5c", "/").Replace(strings.ToLower(v))
		for _, seg := range strings.Split(v, "/") {
			if seg == "." || seg == ".." {
				return false
			}
		}
	}
	return true
}

// MatchTemplate reports whether uri is an expansion of the template t.
func MatchTemplate(t, uri string) bool {
	tm, ok := CompileTemplate(t)
	return ok && tm.Match(uri)
}
