package ratelimit

import (
	"testing"
	"time"
)

func TestLimiterWindows(t *testing.T) {
	l := New()
	now := time.Unix(1000, 0)
	l.now = func() time.Time { return now }
	for i := 0; i < 3; i++ {
		if ok, _ := l.Allow("a", 3, time.Minute); !ok {
			t.Fatalf("event %d refused", i+1)
		}
	}
	if ok, wait := l.Allow("a", 3, time.Minute); ok || wait != time.Minute {
		t.Fatalf("4th = %v %v", ok, wait)
	}
	if ok, _ := l.Allow("b", 3, time.Minute); !ok {
		t.Fatal("keys share a window")
	}
	now = now.Add(time.Minute)
	if ok, _ := l.Allow("a", 3, time.Minute); !ok {
		t.Fatal("the window didn't reset")
	}
}
