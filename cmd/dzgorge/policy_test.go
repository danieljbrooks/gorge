package main

import (
	"math/rand/v2"
	"path/filepath"
	"testing"

	"github.com/adams-shaun/gorge/botpolicy"
	"github.com/adams-shaun/gorge/cards"
	"github.com/adams-shaun/gorge/internal/policynet"
	"github.com/adams-shaun/gorge/internal/spellbench/builtins"
	"github.com/adams-shaun/gorge/internal/testutil"
)

func TestParseSBTactical(t *testing.T) {
	for _, tc := range []struct {
		spec string
		mull botpolicy.MulliganRule
	}{
		{"sbtactical", botpolicy.MulliganCoin},
		{"sbtactical:mull", botpolicy.MulliganLands},
		{"sbtactical:keep7", botpolicy.MulliganNever},
	} {
		p, err := parsePolicy(tc.spec)
		if err != nil {
			t.Fatalf("%s: %v", tc.spec, err)
		}
		if p.Kind != "sbtactical" || p.Mull != tc.mull || p.searches() {
			t.Fatalf("%s: kind %q mull %d searches %v", tc.spec, p.Kind, p.Mull, p.searches())
		}
		st, err := p.seat(7)
		if err != nil {
			t.Fatal(err)
		}
		b, ok := st.(*builtins.Seat)
		if !ok {
			// playOne installs the planner and the refusal fallback on *builtins.Seat only.
			t.Fatalf("%s: seat is %T, want *builtins.Seat", tc.spec, st)
		}
		if !b.WantsPaymentActions() {
			t.Fatalf("%s: not an auto-pay seat", tc.spec)
		}
	}
	if _, err := parsePolicy("sbtactical:bogus"); err == nil {
		t.Fatal("sbtactical:bogus parsed")
	}
}

// TestSBTacticalPlaysAGame plays sb-tactical against bot under the London round, both
// seat orders: no error, and :keep7 never mulligans while the other side may.
func TestSBTacticalPlaysAGame(t *testing.T) {
	reg := testutil.CorpusRegistry(t)
	dc := &deckCache{reg: reg, m: map[string][]*cards.Card{
		"red":   testutil.RepoDeck(t, reg, "mono-red-goblins"),
		"green": testutil.RepoDeck(t, reg, "mono-green-stompy"),
	}}
	specs := make([]*policySpec, 2)
	for i, s := range []string{"sbtactical:keep7", "bot:mull"} {
		p, err := parsePolicy(s)
		if err != nil {
			t.Fatal(err)
		}
		p.setLookup(reg)
		specs[i] = p
	}
	if specs[0].lookup == nil {
		t.Fatal("sbtactical has no card lookup")
	}
	for leg := 0; leg < 2; leg++ {
		jb := job{game: leg, leg: leg, seed: 42, decks: [2]string{"red", "green"}, sides: [2]int{leg, 1 - leg}}
		rec, _ := playOne(jb, specs, dc, reg, 60, 5000, 2, false)
		if rec.Result == "error" {
			t.Fatalf("leg %d: error %s (stall %q)", leg, rec.Err, rec.Stall)
		}
		if rec.Turns == 0 || rec.Intents == 0 {
			t.Fatalf("leg %d: empty game %+v", leg, rec)
		}
		if m := rec.Mull[leg]; m != 0 { // seat leg holds side A (sbtactical:keep7)
			t.Fatalf("leg %d: keep7 sbtactical took %d mulligans", leg, m)
		}
		t.Logf("leg %d: %s after %d turns, %d intents, mulligans %v, %.0f ms", leg, rec.Result, rec.Turns, rec.Intents, rec.Mull, rec.MS)
	}
}

// :manualtaps parses into the az or prior seat's SeatConfig.ManualTaps (an az and prior key): such a seat is no
// payment-plan consumer (the seat before gorge 9afeacb2e), while the same seat without the key consumes the
// engine's plans under :autopay.
func TestParseManualTaps(t *testing.T) {
	type planConsumer interface{ WantsPaymentActions() bool }
	// A prior seat needs a network (with a value head, as every search seat's does): a small untrained one,
	// written to a temp file through the real writer (the path holds no colon, which would end net='s value).
	m := policynet.NewModel(policynet.TableRows, 4, 3, rand.New(rand.NewPCG(1, 2)))
	m.InitValue(2, rand.New(rand.NewPCG(3, 4)))
	net := filepath.Join(t.TempDir(), "prior.gpol")
	if err := m.SaveCheckpoint(net); err != nil {
		t.Fatal(err)
	}
	prior := "prior:net=" + net
	for _, tc := range []struct {
		spec          string
		manual, plans bool
	}{
		{"az:sims=8", false, false},
		{"az:sims=8:autopay", false, true},
		{"az:sims=8:autopay:manualtaps", true, false},
		{"az:sims=200:manualtaps:autopay:oppnodes:oppfull:morekinds:cpuct=0.25", true, false},
		{"az:sims=8:manualtaps", true, false},
		{prior, false, false},
		{prior + ":autopay", false, true},
		{prior + ":autopay:manualtaps", true, false},
		{prior + ":manualtaps", true, false},
	} {
		p, err := parsePolicy(tc.spec)
		if err != nil {
			t.Fatalf("%s: %v", tc.spec, err)
		}
		if p.ManualTaps != tc.manual || p.azConfig().ManualTaps != tc.manual {
			t.Fatalf("%s: manualtaps %v (config %v), want %v", tc.spec, p.ManualTaps, p.azConfig().ManualTaps, tc.manual)
		}
		st, err := p.seat(7)
		if err != nil {
			t.Fatal(err)
		}
		pc, ok := st.(planConsumer)
		if !ok || pc.WantsPaymentActions() != tc.plans {
			t.Fatalf("%s: %T wants payment plans %v, want %v", tc.spec, st, ok && pc.WantsPaymentActions(), tc.plans)
		}
	}
	for _, bad := range []string{"bot:manualtaps", "bot:autopay:manualtaps", "random:manualtaps", "sbtactical:manualtaps"} {
		if _, err := parsePolicy(bad); err == nil {
			t.Fatalf("%s parsed: manualtaps is an az and prior key", bad)
		}
	}
}

// :holdupkeep[=NAMES] sets azmcts Options.HoldUpkeep (and HoldUpkeepExempt, as written) on az and prior seats;
// it is refused on the bots, and an empty exempt name is an error.
func TestParseHoldUpkeep(t *testing.T) {
	m := policynet.NewModel(policynet.TableRows, 4, 3, rand.New(rand.NewPCG(1, 2)))
	m.InitValue(2, rand.New(rand.NewPCG(3, 4)))
	net := filepath.Join(t.TempDir(), "prior.gpol")
	if err := m.SaveCheckpoint(net); err != nil {
		t.Fatal(err)
	}
	prior := "prior:net=" + net
	for _, tc := range []struct {
		spec   string
		hold   bool
		exempt string
	}{
		{"az:sims=8", false, ""},
		{"az:sims=8:autopay", false, ""},
		{"az:sims=8:autopay:holdupkeep", true, ""},
		{"az:sims=200:holdupkeep:autopay:oppnodes:oppfull:topk=4:cpuct=0.25", true, ""},
		{"az:sims=8:holdupkeep=Opt", true, "Opt"},
		{"az:sims=8:autopay:holdupkeep=Opt|Consider|Jace's Erasure:mull", true, "Opt|Consider|Jace's Erasure"},
		{prior + ":autopay:holdupkeep", true, ""},
		{prior, false, ""},
	} {
		p, err := parsePolicy(tc.spec)
		if err != nil {
			t.Fatalf("%s: %v", tc.spec, err)
		}
		cfg := p.azConfig()
		if p.HoldUpkeep != tc.hold || cfg.Search.HoldUpkeep != tc.hold || p.HoldExempt != tc.exempt || cfg.Search.HoldUpkeepExempt != tc.exempt {
			t.Fatalf("%s: holdupkeep %v %q (config %v %q), want %v %q", tc.spec, p.HoldUpkeep, p.HoldExempt,
				cfg.Search.HoldUpkeep, cfg.Search.HoldUpkeepExempt, tc.hold, tc.exempt)
		}
		if cfg.ManualTaps || !cfg.Search.Kinds.Priority {
			t.Fatalf("%s: holdupkeep changed another setting: %+v", tc.spec, cfg)
		}
		if _, err := p.seat(7); err != nil {
			t.Fatalf("%s: %v", tc.spec, err)
		}
	}
	for _, bad := range []string{"bot:holdupkeep", "bot:autopay:holdupkeep", "random:holdupkeep", "sbtactical:holdupkeep",
		"az:holdupkeep=Opt|", "az:holdupkeep=|Opt", "az:holdupkeep=Opt||Consider", "az:holdupkeep= "} {
		if _, err := parsePolicy(bad); err == nil {
			t.Fatalf("%s parsed", bad)
		}
	}
}

// :tricks[=NAMES] sets azmcts Options.Tricks (and TricksCards, as written; "" is the default list) on az and prior
// seats, composes with :holdupkeep, and is refused on the bots; an empty name is an error. Without the key nothing
// changes.
func TestParseTricks(t *testing.T) {
	m := policynet.NewModel(policynet.TableRows, 4, 3, rand.New(rand.NewPCG(1, 2)))
	m.InitValue(2, rand.New(rand.NewPCG(3, 4)))
	net := filepath.Join(t.TempDir(), "prior.gpol")
	if err := m.SaveCheckpoint(net); err != nil {
		t.Fatal(err)
	}
	prior := "prior:net=" + net
	for _, tc := range []struct {
		spec   string
		tricks bool
		cards  string
		hold   bool
	}{
		{"az:sims=8", false, "", false},
		{"az:sims=8:autopay:holdupkeep", false, "", true},
		{"az:sims=8:autopay:tricks", true, "", false},
		{"az:sims=200:tricks:autopay:oppnodes:oppfull:morekinds:topk=4:cpuct=0.25:holdupkeep", true, "", true},
		{"az:sims=8:tricks=Giant Growth", true, "Giant Growth", false},
		{"az:sims=8:autopay:tricks=Giant Growth|Fleeting Flight|An Offer You Can't Refuse:mull", true, "Giant Growth|Fleeting Flight|An Offer You Can't Refuse", false},
		{prior + ":autopay:tricks", true, "", false},
		{prior, false, "", false},
	} {
		p, err := parsePolicy(tc.spec)
		if err != nil {
			t.Fatalf("%s: %v", tc.spec, err)
		}
		cfg := p.azConfig()
		if p.Tricks != tc.tricks || cfg.Search.Tricks != tc.tricks || p.TricksCards != tc.cards || cfg.Search.TricksCards != tc.cards || cfg.Search.HoldUpkeep != tc.hold {
			t.Fatalf("%s: tricks %v %q (config %v %q, holdupkeep %v), want %v %q (holdupkeep %v)", tc.spec, p.Tricks, p.TricksCards,
				cfg.Search.Tricks, cfg.Search.TricksCards, cfg.Search.HoldUpkeep, tc.tricks, tc.cards, tc.hold)
		}
		if cfg.ManualTaps || !cfg.Search.Kinds.Priority || cfg.Search.HoldUpkeepExempt != "" {
			t.Fatalf("%s: tricks changed another setting: %+v", tc.spec, cfg)
		}
		if _, err := p.seat(7); err != nil {
			t.Fatalf("%s: %v", tc.spec, err)
		}
	}
	for _, bad := range []string{"bot:tricks", "bot:autopay:tricks", "random:tricks", "sbtactical:tricks",
		"az:tricks=Giant Growth|", "az:tricks=|Giant Growth", "az:tricks=Giant Growth||Sure Strike", "az:tricks= "} {
		if _, err := parsePolicy(bad); err == nil {
			t.Fatalf("%s parsed", bad)
		}
	}
}
