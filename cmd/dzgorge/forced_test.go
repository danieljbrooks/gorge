package main

import (
	"strings"
	"testing"
)

// :forced[=K] parses into the az seat's Options.ForcedK (bare :forced is 2, forced=0 is off), and a
// bad k or a non-az seat is refused at parse time.
func TestParseForced(t *testing.T) {
	for _, tc := range []struct {
		spec string
		k    float64
	}{
		{"az:sims=8", 0},
		{"az:sims=8:forced=0", 0},
		{"az:sims=8:forced", defaultForcedK},
		{"az:sims=8:explore:forced=1.5:pcr=0.5:decided", 1.5},
	} {
		p, err := parsePolicy(tc.spec)
		if err != nil {
			t.Fatalf("%s: %v", tc.spec, err)
		}
		if p.Forced != tc.k || p.azConfig().Search.ForcedK != tc.k {
			t.Fatalf("%s: forced %g, seat ForcedK %g, want %g", tc.spec, p.Forced, p.azConfig().Search.ForcedK, tc.k)
		}
		if _, err := p.seat(1); err != nil {
			t.Fatalf("%s: %v", tc.spec, err)
		}
	}
	for _, tc := range []struct{ spec, want string }{
		{"az:forced=-1", "finite k > 0"},
		{"az:forced=NaN", "finite k > 0"},
		{"az:forced=Inf", "finite k > 0"},
		{"az:forced=x", "invalid syntax"},
		{"bot:forced", "is an az key"},
		{"random:forced=2", "is an az key"},
	} {
		if _, err := parsePolicy(tc.spec); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: error %v, want one mentioning %q", tc.spec, err, tc.want)
		}
	}
}

// A forced= game records full searches whose targets are pruned: no candidate is left at one
// visit, and some record holds fewer visits than its simulations.
func TestForcedRecordsPrunedTargets(t *testing.T) {
	rec, member := decidedGame(t, "az:sims=12:explore:cpuct=0.25:forced=2")
	recs := corpusRecords(t, member)
	pruned := 0
	for _, r := range recs {
		sum := 0
		for _, v := range r.Visits {
			if v == 1 {
				t.Fatalf("seq %d: target %v keeps a one-visit candidate", r.Sequence, r.Visits)
			}
			sum += v
		}
		if sum < r.Sims {
			pruned++
		}
	}
	if len(recs) == 0 || pruned == 0 {
		t.Fatalf("%d records, %d pruned", len(recs), pruned)
	}
	t.Logf("%s after %d turns: %d of %d records pruned", rec.Result, rec.Turns, pruned, len(recs))
}
