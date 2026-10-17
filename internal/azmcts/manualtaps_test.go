package azmcts

import (
	"context"
	"reflect"
	"testing"

	"github.com/adams-shaun/gorge/botpolicy"
	"github.com/adams-shaun/gorge/internal/searchseat"
)

// SeatConfig.ManualTaps (DraftZero) is the auto-payment seat as before upstream 9afeacb2e: it is no
// payment-plan consumer, and at the counterspell window (no plans built) its bot's manual tap is played
// unsearched -- the decision skipped, never rerouted into a search (TestAZReroutesABotTapIntoTheSearch's
// seat without the key).
func TestManualTapsSeatPlaysTheBotTapUnsearched(t *testing.T) {
	e := counterWindow(t)
	manual := func() *Seat {
		sc := DefaultSeatConfig()
		sc.Search.AutoPayment, sc.Search.Sims, sc.Source, sc.ManualTaps = true, 8, testSeatSource, true
		s, err := NewSeat(5, nil, sc)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	s := manual()
	if s.WantsPaymentActions() {
		t.Fatal("a ManualTaps seat must not consume payment plans")
	}
	d := *e.Pending()
	board := botpolicy.NewBoard(2)
	b := botpolicy.BoardFromGameInto(e.G, e, d.Player, &board)
	botIn, err := s.def.DecideBoard(context.Background(), b, d)
	if err != nil {
		t.Fatal(err)
	}
	if priorityBase(&d, botIn) != BaseActivate {
		t.Skipf("the plan-less bot did not tap here (%+v); the backstop is not exercised", botIn)
	}
	diags := watchDiags(t)
	s = manual() // a fresh rng stream, the same as the probe's
	in, err := s.DecideSearch(context.Background(), searchseat.Env{Engine: e, Board: b}, d)
	if err != nil {
		t.Fatal(err)
	}
	if len(*diags) != 1 || (*diags)[0].Searched || (*diags)[0].Stats.Skipped != 1 || (*diags)[0].Stats.BotTapRerooted != 0 {
		t.Fatalf("a ManualTaps seat searched or rerooted the bot's tap: %+v", *diags)
	}
	if !reflect.DeepEqual(in, botIn) {
		t.Fatalf("a ManualTaps seat played %+v, want the bot's tap %+v", in, botIn)
	}
}
