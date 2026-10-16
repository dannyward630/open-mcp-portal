package mcpgw

import (
	"encoding/json"
	"testing"
)

func TestScrubMeta(t *testing.T) {
	cases := []struct{ in, want string }{
		{`{"content":[],"_meta":{"io.modelcontextprotocol/serverInfo":{"name":"up"}}}`, `{"content":[]}`},
		{`{"content":[],"_meta":{"io.modelcontextprotocol/serverInfo":{"name":"up"},"x":1}}`, `{"_meta":{"x":1},"content":[]}`},
		{`{"content":[],"_meta":{"x":1}}`, `{"content":[],"_meta":{"x":1}}`},
		{`{"content":[]}`, `{"content":[]}`},
	}
	for _, c := range cases {
		got, err := scrubMeta([]byte(c.in))
		if err != nil {
			t.Fatal(err)
		}
		var g, w any
		if err := json.Unmarshal(got, &g); err != nil {
			t.Fatal(err)
		}
		_ = json.Unmarshal([]byte(c.want), &w)
		gb, _ := json.Marshal(g)
		wb, _ := json.Marshal(w)
		if string(gb) != string(wb) {
			t.Errorf("scrubMeta(%s) = %s, want %s", c.in, gb, wb)
		}
	}
}
