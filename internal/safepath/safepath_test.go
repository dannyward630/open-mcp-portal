package safepath

import (
	"strings"
	"testing"
)

func TestLocal(t *testing.T) {
	for p, ok := range map[string]bool{
		"/": true, "/settings/connections": true, "/connect/done?x=1": true, "/a%20b": true, "/caf%C3%A9": true,
		"": false, "relative": false, "//evil.example": false, "/\\evil.example": false, "https://evil.example": false,
		"javascript:alert(1)": false, "/a//b": false, "/a\nb": false, "/" + strings.Repeat("a", 100): false,
		"/%2Fevil.example": false, "/%5cevil.example": false, "/%252Fevil.example": false, "/a?next=%2F%2Fevil": false,
		// F-4: control characters are dropped by browsers, so "/\t/evil.example" is "//evil.example".
		"/\t/evil.example": false, "/%09/evil.example": false, "/%2509/evil.example": false, "/\v/evil.example": false,
		"/\x00/evil": false, "/%0b/evil": false, "/\u0085/evil": false, "/a%zz": false, "/a%2525252525": false,
	} {
		if got := Local(p, 100); got != ok {
			t.Errorf("Local(%q) = %v, want %v", p, got, ok)
		}
	}
}

func TestLocalWithQuery(t *testing.T) {
	oauth := "/oauth/authorize?client_id=abc&redirect_uri=https%3A%2F%2Fclient.example%2Fcb&state=x%2Fy&resource=https://gw.example/mcp"
	for p, ok := range map[string]bool{
		"/admin/teams": true, "/teams/a?tab=members": true, oauth: true, "/connect/x#frag": true, "/search?q=a%26b": true,
		"/?//evil.example": true, // a query can't change the host
		"":                 false, "//evil.example": false, "/\\evil.example": false, "https://evil.example": false, "relative": false,
		"/\t/evil.example": false, "/%09/evil.example": false, "/%2509/evil.example": false, "/%2F/evil.example": false,
		"/%5C/evil.example": false, "/x\r\nSet-Cookie: a": false, "/a?b=c\td": false, "/a#f\ng": false,
		"/" + strings.Repeat("a", 500): false, // over the limit
	} {
		if got := LocalWithQuery(p, 400); got != ok {
			t.Errorf("LocalWithQuery(%q) = %v, want %v", p, got, ok)
		}
	}
}
