package main

import (
	"reflect"
	"strings"
	"testing"

	"github.com/adams-shaun/gorge/cards"
	"github.com/adams-shaun/gorge/internal/testutil"
)

// :pcr=, :cheap= and :pcrk= parse into the az seat's playout-cap settings: cheap= defaults to
// defaultCheapSims under pcr=, pcrk= takes ksims='s kind.P list (or kind=P), pcr=0 is off, and
// every bad range, kind name or combination is refused at parse time.
func TestParsePCR(t *testing.T) {
	for _, tc := range []struct {
		spec  string
		pcr   float64
		cheap int
		pcrk  map[string]float64
	}{
		{"az:sims=8", 0, 0, nil},
		{"az:sims=8:pcr=0", 0, 0, nil},
		{"az:sims=8:pcr=0.5", 0.5, defaultCheapSims, nil},
		{"az:sims=8:pcr=1:cheap=4", 1, 4, nil},
		{"az:sims=8:explore:pcr=0.25:cheap=4", 0.25, 4, nil},
		{"az:sims=8:morekinds:pcr=0.5:pcrk=priority.0.65,attackers.0.5,optional.0", 0.5, defaultCheapSims,
			map[string]float64{"priority": 0.65, "attackers": 0.5, "optional": 0}},
		{"az:sims=8:pcr=0.4:cheap=10:pcrk=priority=0.65,blockers=1", 0.4, 10,
			map[string]float64{"priority": 0.65, "blockers": 1}},
	} {
		p, err := parsePolicy(tc.spec)
		if err != nil {
			t.Fatalf("%s: %v", tc.spec, err)
		}
		if p.PCR != tc.pcr || p.Cheap != tc.cheap || !reflect.DeepEqual(p.PCRK, tc.pcrk) {
			t.Fatalf("%s: pcr %g cheap %d pcrk %v, want %g %d %v", tc.spec, p.PCR, p.Cheap, p.PCRK, tc.pcr, tc.cheap, tc.pcrk)
		}
		cfg := p.azConfig()
		if cfg.FullProb != tc.pcr || cfg.CheapSims != tc.cheap || !reflect.DeepEqual(cfg.FullProbByKind, tc.pcrk) {
			t.Fatalf("%s: seat config FullProb %g CheapSims %d FullProbByKind %v", tc.spec, cfg.FullProb, cfg.CheapSims, cfg.FullProbByKind)
		}
		if _, err := p.seat(1); err != nil {
			t.Fatalf("%s: %v", tc.spec, err)
		}
	}
	for _, tc := range []struct{ spec, want string }{
		{"az:pcr=1.5", "full-search probability in [0,1]"},
		{"az:pcr=-0.1", "full-search probability in [0,1]"},
		{"az:pcr=NaN", "full-search probability in [0,1]"},
		{"az:pcr=half", "invalid syntax"},
		{"az:cheap=10", "need pcr=P > 0"},
		{"az:pcr=0:cheap=10", "need pcr=P > 0"},
		{"az:pcrk=priority.0.5", "need pcr=P > 0"},
		{"az:pcr=0.5:cheap=0", "want >= 1 simulations"},
		{"az:pcr=0.5:cheap=x", "invalid syntax"},
		{"az:pcr=0.5:pcrk=casting.0.5", "unknown searched kind"},
		{"az:pcr=0.5:pcrk=priority.1.5", "FullProbByKind[priority] 1.5 must be in [0,1]"},
		{"az:pcr=0.5:pcrk=priority", "want kind.P"},
		{"az:pcr=0.5:pcrk=priority.x", "invalid syntax"},
		{"az:pcr=0.5:pcrk=priority.0.5,priority.0.6", "listed twice"},
		{"bot:pcr=0.5", "are az keys"},
		{"sbtactical:cheap=10", "are az keys"},
	} {
		if _, err := parsePolicy(tc.spec); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: error %v, want one mentioning %q", tc.spec, err, tc.want)
		}
	}
}

// A pcr= seat plays a recorded game in which it searches both full and cheap decisions, and
// only full ones reach the corpus.
func TestPCRPlaysAndRecordsOnlyFullSearches(t *testing.T) {
	reg := testutil.CorpusRegistry(t)
	dc := &deckCache{reg: reg, m: map[string][]*cards.Card{
		"red":   testutil.RepoDeck(t, reg, "mono-red-goblins"),
		"green": testutil.RepoDeck(t, reg, "mono-green-stompy"),
	}}
	specs := make([]*policySpec, 2)
	for i, s := range []string{"az:sims=6:explore:pcr=0.5:cheap=2", "bot"} {
		p, err := parsePolicy(s)
		if err != nil {
			t.Fatal(err)
		}
		specs[i] = p
	}
	jb := job{game: 0, seed: 42, decks: [2]string{"red", "green"}, sides: [2]int{0, 1}}
	rec, member := playOne(jb, specs, dc, reg, 40, 5000, 0, true)
	if rec.Result == "error" {
		t.Fatalf("error %s (stall %q)", rec.Err, rec.Stall)
	}
	full, cheap := rec.pcr[0][0], rec.pcr[0][1]
	if full == 0 || cheap == 0 || rec.pcr[1] != [2]int{} {
		t.Fatalf("pcr counts %v: want full and cheap searches on seat 0 only", rec.pcr)
	}
	if rec.Visits == 0 || rec.Visits > full || member == nil {
		t.Fatalf("%d visit records for %d full and %d cheap searched decisions", rec.Visits, full, cheap)
	}
	t.Logf("%s after %d turns: %d full, %d cheap, %d records", rec.Result, rec.Turns, full, cheap, rec.Visits)
}
