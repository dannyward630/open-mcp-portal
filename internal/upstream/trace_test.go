package upstream

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTraceValidation(t *testing.T) {
	const tp = "00-0af7651916cd43dd8448eb211c80319c-00f067aa0ba902b7-01"
	for s, want := range map[string]bool{
		tp: true,
		"00-0af7651916cd43dd8448eb211c80319c-00f067aa0ba902b7-00":  true,
		"01-0af7651916cd43dd8448eb211c80319c-00f067aa0ba902b7-01":  true,
		"ff-0af7651916cd43dd8448eb211c80319c-00f067aa0ba902b7-01":  false, // version ff is invalid
		"00-00000000000000000000000000000000-00f067aa0ba902b7-01":  false,
		"00-0af7651916cd43dd8448eb211c80319c-0000000000000000-01":  false,
		"00-0AF7651916CD43DD8448EB211C80319C-00f067aa0ba902b7-01":  false,
		"00-0af7651916cd43dd8448eb211c80319c-00f067aa0ba902b7":     false,
		"00-0af7651916cd43dd8448eb211c80319c-00f067aa0ba902b7-01 ": false,
		"":       false,
		tp + "x": false,
	} {
		if got := ValidTraceparent(s); got != want {
			t.Errorf("traceparent %q: %v, want %v", s, got, want)
		}
	}
	for s, want := range map[string]bool{
		"congo=t61rcWkgMzE,rojo=00f067aa0ba902b7": true,
		"a=b":                              true,
		"tenant@vendor=x":                  true,
		"BAD=x":                            false,
		"a=b,":                             false,
		"a b=c":                            false,
		"a=b=c":                            false,
		"":                                 false,
		strings.Repeat("a=b,", 33) + "a=b": false,
		"k=" + strings.Repeat("v", 600):    false,
	} {
		if got := ValidTracestate(s); got != want {
			t.Errorf("tracestate %.40q: %v, want %v", s, got, want)
		}
	}
	for s, want := range map[string]bool{
		"userId=alice,serverNode=DF%2028,isProduction=false": true,
		"key=value;prop=1":          true,
		"a=b, c=d":                  true,
		"a":                         false,
		"=b":                        false,
		`a="b"`:                     false,
		"a=b c":                     false,
		"":                          false,
		strings.Repeat("a=b,", 600): false,
	} {
		if got := ValidBaggage(s); got != want {
			t.Errorf("baggage %.40q: %v, want %v", s, got, want)
		}
	}
}

func TestTraceFromMeta(t *testing.T) {
	meta := func(s string) map[string]json.RawMessage {
		var m map[string]json.RawMessage
		_ = json.Unmarshal([]byte(s), &m)
		return m
	}
	const tp = "00-0af7651916cd43dd8448eb211c80319c-00f067aa0ba902b7-01"
	got := TraceFromMeta(meta(`{"traceparent":"` + tp + `","tracestate":"a=b","baggage":"k=v","other":1}`))
	if got != (Trace{tp, "a=b", "k=v"}) || got.Empty() {
		t.Fatalf("got %+v", got)
	}
	// A tracestate means nothing without its traceparent; baggage stands alone.
	if got := TraceFromMeta(meta(`{"tracestate":"a=b","baggage":"k=v"}`)); got != (Trace{Baggage: "k=v"}) {
		t.Fatalf("got %+v", got)
	}
	if got := TraceFromMeta(meta(`{"traceparent":5,"baggage":["x"]}`)); !got.Empty() {
		t.Fatalf("got %+v", got)
	}
	if !TraceFromMeta(nil).Empty() {
		t.Fatal("nil meta")
	}
	m := map[string]any{}
	Trace{}.addTo(m)
	if len(m) != 0 {
		t.Fatalf("an empty trace added %v", m)
	}
}
