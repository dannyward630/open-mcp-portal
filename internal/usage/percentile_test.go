package usage

import "testing"

func TestPercentile(t *testing.T) {
	b := make([]int64, len(LatencyBucketsMs)+1)
	if Percentile(b, 0.5) != nil {
		t.Fatal("no calls: nil")
	}
	// 10 calls in (10, 25]: the median is halfway through the bucket.
	b[2] = 10
	if p := Percentile(b, 0.5); p == nil || *p != 18 {
		t.Fatalf("p50 = %v", p)
	}
	// 90 more calls in (0, 5]: p50 falls in the first bucket, p95 in (10, 25].
	b[0] = 90
	if p := Percentile(b, 0.5); *p != 3 {
		t.Fatalf("p50 = %d", *p)
	}
	if p := Percentile(b, 0.95); *p < 17 || *p > 18 {
		t.Fatalf("p95 = %d", *p)
	}
	// Beyond the last bound: the bound.
	over := make([]int64, len(LatencyBucketsMs)+1)
	over[len(over)-1] = 3
	if p := Percentile(over, 0.95); *p != 120000 {
		t.Fatalf("+Inf = %d", *p)
	}
}
