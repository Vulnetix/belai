package agent

import "testing"

// A job started early is recorded only when its result is used, once; an
// unused one is never recorded. A nil early runs the fallback.
func TestEarlyRecordsOnlyWhenUsed(t *testing.T) {
	records := 0
	e := startEarly(func() (int, func()) { return 7, func() { records++ } })
	if v := e.use(func() int { return 0 }); v != 7 || records != 1 {
		t.Fatalf("v=%d records=%d", v, records)
	}
	e.use(func() int { return 0 })
	if records != 1 {
		t.Fatal("recorded twice")
	}
	unused := startEarly(func() (int, func()) { return 1, func() { records++ } })
	<-unused.done
	if records != 1 {
		t.Fatal("an unused result was recorded")
	}
	var none *early[int]
	if none.use(func() int { return 3 }) != 3 {
		t.Fatal("nil early did not run the fallback")
	}
}
