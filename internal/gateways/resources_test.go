package gateways

import "testing"

func TestExposedURI(t *testing.T) {
	if got := ExposedURI("status", "status://services"); got != "omp://status/status://services" {
		t.Fatalf("ExposedURI = %q", got)
	}
	if got := Exposed("resource_template", "status", "status://services/{service}"); got != "omp://status/status://services/{service}" {
		t.Fatalf("Exposed template = %q", got)
	}
	if got := Exposed("tool", "status", "echo"); got != "status__echo" {
		t.Fatalf("Exposed tool = %q", got)
	}
	for _, c := range []struct {
		in, slug, orig string
		ok             bool
	}{
		{"omp://status/status://services", "status", "status://services", true},
		{"omp://status/file:///a/b", "status", "file:///a/b", true},
		{"omp://status/", "", "", false},
		{"omp://status", "", "", false},
		{"omp://Bad_Slug/x://y", "", "", false},
		{"status://services", "", "", false},
		{"OMP://status/x://y", "", "", false},
	} {
		slug, orig, ok := SplitExposedURI(c.in)
		if ok != c.ok || slug != c.slug || orig != c.orig {
			t.Errorf("SplitExposedURI(%q) = %q %q %v", c.in, slug, orig, ok)
		}
	}
}

func TestTemplateSupported(t *testing.T) {
	for tmpl, want := range map[string]bool{
		"status://services/{service}":     true,
		"file:///{+path}":                 true,
		"db://{schema}/{table.name}":      true,
		"x://{a%20b}":                     true,
		"plain://no/variables":            true,
		"x://{#frag}":                     false,
		"x://items{/id}":                  false,
		"x://search{?q}":                  false,
		"x://{a,b}":                       false,
		"x://{var:3}":                     false,
		"x://{list*}":                     false,
		"x://{}":                          false,
		"x://{unclosed":                   false,
		"x://closed}":                     false,
		"x://{.dot}":                      false,
		"x://{;p}":                        false,
		"x://{&q}":                        false,
		"x://{=reserved}":                 false,
		"status://services/{+a}/{b}/tail": true,
	} {
		if got := TemplateSupported(tmpl); got != want {
			t.Errorf("TemplateSupported(%q) = %v, want %v", tmpl, got, want)
		}
	}
}

func TestMatchTemplate(t *testing.T) {
	cases := []struct {
		tmpl, uri string
		want      bool
	}{
		{"status://services/{service}", "status://services/email", true},
		{"status://services/{service}", "status://services/e%20mail", true},
		{"status://services/{service}", "status://services/", false},          // empty value
		{"status://services/{service}", "status://services/a/b", false},       // "/" needs {+var}
		{"status://services/{service}", "status://services/email?x=1", false}, // reserved chars
		{"status://services/{service}", "status://services/..", false},        // dot segment
		{"status://services/{service}", "status://services/%2E%2e", false},
		{"status://services/{service}", "status://services/a%2F..", false},
		{"status://services/{service}", "status://servicesX/email", false},
		{"status://services/{service}", "STATUS://services/email", false},
		{"file:///docs/{+path}", "file:///docs/a/b/c.md", true},
		{"file:///docs/{+path}", "file:///docs/a/../../etc/passwd", false},
		{"file:///docs/{+path}", "file:///docs/./a", false},
		{"file:///docs/{+path}", "file:///docs/a/b?q=1#x", true},
		{"file:///docs/{+path}", "file:///docs/a b", false},
		{"db://{schema}/{table}", "db://public/users", true},
		{"db://{schema}/{table}", "db://public", false},
		{"a.b://{x}", "aXb://y", false},  // literals are quoted
		{"x://{#frag}", "x://#a", false}, // unsupported template
		{"plain://fixed", "plain://fixed", true},
	}
	for _, c := range cases {
		if got := MatchTemplate(c.tmpl, c.uri); got != c.want {
			t.Errorf("MatchTemplate(%q, %q) = %v, want %v", c.tmpl, c.uri, got, c.want)
		}
	}
}
