package azmcts

import (
	"context"
	"testing"

	"github.com/adams-shaun/gorge/botpolicy"
	"github.com/adams-shaun/gorge/internal/policynet"
	"github.com/adams-shaun/gorge/internal/searchseat"
	"github.com/adams-shaun/gorge/rules"
)

// DraftZero: the visit record of the bot-tap reroute (Result.NoBot). Under Root.NoBot candidate 0 is Pass, not the
// bot's answer (a manual mana tap, no candidate's), so the record flags no stored option BotPick -- gonet reads
// the flag as an input (pack's opt_bot) -- and says no_bot; at the same window with the plans built, the bot's
// answer is candidate 0 and the record still flags its options.
func TestRerootedRecordFlagsNoBotPick(t *testing.T) {
	decide := func(e *rules.Engine) (*policynet.VisitRecord, []Diag) {
		t.Helper()
		d := *e.Pending()
		board := botpolicy.NewBoard(2)
		b := botpolicy.BoardFromGameInto(e.G, e, d.Player, &board)
		s := payWindowSeat(t)
		var rec *policynet.VisitRecord
		s.SetRecorder(func(r policynet.VisitRecord) { rec = &r })
		diags := watchDiags(t)
		if _, err := s.DecideSearch(context.Background(), searchseat.Env{Engine: e, Board: b}, d); err != nil {
			t.Fatal(err)
		}
		if rec == nil {
			t.Fatalf("no record: %+v", *diags)
		}
		return rec, *diags
	}

	// Rerouted: no plans built, so the auto-pay bot taps by hand (TestAZReroutesABotTapIntoTheSearch).
	e := counterWindow(t)
	d := *e.Pending()
	board := botpolicy.NewBoard(2)
	botIn, err := payWindowSeat(t).def.DecideBoard(context.Background(), botpolicy.BoardFromGameInto(e.G, e, d.Player, &board), d)
	if err != nil {
		t.Fatal(err)
	}
	if priorityBase(&d, botIn) != BaseActivate {
		t.Skipf("the plan-less bot did not tap here (%+v); the reroute is not exercised", botIn)
	}
	rec, diags := decide(e)
	if len(diags) != 1 || diags[0].Stats.BotTapRerooted != 1 {
		t.Fatalf("the bot's tap was not rerooted: %+v", diags)
	}
	if !rec.NoBot {
		t.Fatal("a rerooted record must say no_bot")
	}
	for i, o := range rec.Options {
		if o.BotPick {
			t.Fatalf("rerooted record: stored option %d is flagged BotPick, but no candidate is the bot's (cands %v)", i, rec.Cands)
		}
	}

	// Normal: the driver builds the plans first (TestAZSearchesTheCounterspellWindow); candidate 0 is the bot's.
	e = counterWindow(t)
	e.EnsurePaymentActions()
	rec, diags = decide(e)
	if len(diags) != 1 || !diags[0].Searched || diags[0].Stats.BotTapRerooted != 0 {
		t.Fatalf("the window with plans was not searched as an ordinary root: %+v", diags)
	}
	if rec.NoBot || len(rec.Cands) < 2 || len(rec.Cands[0]) == 0 {
		t.Fatalf("normal record: no_bot %v, cands %v", rec.NoBot, rec.Cands)
	}
	bot := map[int]bool{}
	for _, p := range rec.Cands[0] {
		bot[p] = true
		if !rec.Options[p].BotPick {
			t.Fatalf("normal record: candidate 0's stored option %d is not flagged BotPick (cands %v)", p, rec.Cands)
		}
	}
	for i, o := range rec.Options {
		if o.BotPick && !bot[i] {
			t.Fatalf("normal record: stored option %d is flagged BotPick but is not candidate 0's (cands %v)", i, rec.Cands)
		}
	}
}
