// Package safepath decides whether a user-supplied string is a path on this
// site, for redirects that must never leave it (the connect flow's returnTo,
// the sign-in's next). Both callers share one rule so a bypass found in one
// is closed in the other (docs/threat-model.md, F-4).
package safepath

import (
	"net/url"
	"strings"
	"unicode"
)

// maxDecodeLayers is how many times a path may be percent-decoded before it
// is refused: more layers of encoding than any legitimate path has.
const maxDecodeLayers = 4

// Local reports whether p is a path on this site: one leading slash, no
// "//", backslash, scheme or control character (tab and newline included,
// since browsers drop them when parsing a URL: "/\t/evil.example" is
// "//evil.example"), at most max bytes. The same holds for every
// percent-decoding of p (browsers and proxies may decode a path once or
// more), and an encoded slash or backslash (%2F, %5C, also double-encoded)
// is refused outright. The whole string is held to this, query included.
func Local(p string, max int) bool {
	if p == "" || len(p) > max || !strings.HasPrefix(p, "/") {
		return false
	}
	for layer, i := p, 0; ; i++ {
		lower := strings.ToLower(layer)
		if !strings.HasPrefix(layer, "/") || strings.Contains(layer, "//") || strings.Contains(layer, `\`) ||
			strings.Contains(lower, "%2f") || strings.Contains(lower, "%5c") || !oneLine(layer) {
			return false
		}
		if !strings.Contains(layer, "%") {
			break
		}
		if i == maxDecodeLayers {
			return false
		}
		decoded, err := url.PathUnescape(layer)
		if err != nil {
			return false
		}
		layer = decoded
	}
	u, err := url.Parse(p)
	return err == nil && u.Scheme == "" && u.Host == "" && u.User == nil
}

// LocalWithQuery is Local for a path that may carry a query or fragment
// whose own encoding is legitimate: the sign-in's next can be an OAuth
// authorization request ("/oauth/authorize?redirect_uri=https%3A%2F%2F..."),
// whose query holds encoded slashes. The path (up to the first "?" or "#")
// is held to Local's rule; the rest only has to be free of control
// characters, since it can't change where the redirect goes.
func LocalWithQuery(p string, max int) bool {
	if len(p) > max {
		return false
	}
	path, rest := p, ""
	if i := strings.IndexAny(p, "?#"); i >= 0 {
		path, rest = p[:i], p[i:]
	}
	if !oneLine(rest) || !Local(path, max) {
		return false
	}
	u, err := url.Parse(p)
	return err == nil && u.Scheme == "" && u.Host == "" && u.User == nil
}

func oneLine(s string) bool { return strings.IndexFunc(s, unicode.IsControl) < 0 }
