package azmcts

import (
	"context"
	"sync"
	"testing"

	"github.com/adams-shaun/gorge/botpolicy"
	"github.com/adams-shaun/gorge/cards"
	"github.com/adams-shaun/gorge/decision"
	"github.com/adams-shaun/gorge/internal/searchprobe"
	"github.com/adams-shaun/gorge/internal/searchseat"
	"github.com/adams-shaun/gorge/rules"
	"github.com/adams-shaun/gorge/seat"
	"github.com/adams-shaun/gorge/state"
)

func payWindowCard(t *testing.T, src string) *cards.Card {
	t.Helper()
	c, diags := cards.ParseBytes("paywindow_test.txt", []byte(src))
	if len(diags) != 0 {
		t.Fatalf("parse: %v", diags)
	}
	c.Link()
	for _, f := range c.Faces {
		f.ApplyIntrinsics()
	}
	return c
}

// counterWindow plays a bear deck (seat 0) into a counterspell deck (seat 1)
// with the plain bot until seat 1 holds priority over seat 0's spell with a
// counter castable from untapped Islands. The engine is left at that
// decision, with no payment plans built.
func counterWindow(t *testing.T) *rules.Engine {
	t.Helper()
	counter := payWindowCard(t, "Name:Test Counterspell\nManaCost:U U\nTypes:Instant\nA:SP$ Counter | TargetType$ Spell | ValidTgts$ Card | SpellDescription:Counter target spell.\nOracle:Counter target spell.\n")
	testCounter = counter
	bear := payWindowCard(t, "Name:Bear\nManaCost:1\nTypes:Creature Bear\nPT:2/2\nOracle:x\n")
	island := payWindowCard(t, "Name:Island\nTypes:Basic Land Island\nOracle:x\n")
	var d0, d1 []*cards.Card
	for i := 0; i < 19; i++ {
		d0, d1 = append(d0, island), append(d1, island)
	}
	for i := 0; i < 5; i++ {
		d0, d1 = append(d0, bear), append(d1, counter)
	}
	for seed := uint64(1); seed < 40; seed++ {
		e := rules.New(rules.Config{Seed: seed, Names: []string{"a", "b"}, Decks: [][]*cards.Card{d0, d1}})
		e.Advance()
		rngs := searchprobe.BotRandoms(seed, 2)
		board := botpolicy.NewBoard(2)
		for steps := 0; steps < 3000 && !e.G.Over; steps++ {
			d := e.Pending()
			if d.Player == 1 && d.Kind == decision.KPriority && foreignSpellOnStack(e.G, 1) && untappedIslands(e.G, 1) >= 2 && holdsCounter(e.G, 1) {
				return e
			}
			in := botpolicy.Decide(botpolicy.BoardFromGameInto(e.G, e, d.Player, &board), d, rngs[d.Player])
			if err := e.Submit(in); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Fatal("no counterspell window reached")
	return nil
}

// testCounter is the counter card counterWindow built (the hand probe's identity).
var testCounter *cards.Card

func foreignSpellOnStack(g *state.Game, me state.PlayerID) bool {
	for _, id := range g.Stack {
		if o := g.Obj(id); o != nil && o.Controller != me && o.Card != nil {
			return true
		}
	}
	return false
}

func untappedIslands(g *state.Game, p state.PlayerID) int {
	n := 0
	for _, id := range g.Zone(state.ZBattlefield, p) {
		if o := g.Obj(id); o != nil && o.Controller == p && !o.Tapped {
			n++
		}
	}
	return n
}

func holdsCounter(g *state.Game, p state.PlayerID) bool {
	for _, id := range g.Zone(state.ZHand, p) {
		if o := g.Obj(id); o != nil && o.Card == testCounter {
			return true
		}
	}
	return false
}

func payWindowSeat(t *testing.T) *Seat {
	t.Helper()
	sc := DefaultSeatConfig()
	sc.Search.AutoPayment = true
	sc.Search.Sims = 8
	sc.Source = testSeatSource
	s, err := NewSeat(5, nil, sc)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func watchDiags(t *testing.T) *[]Diag {
	var mu sync.Mutex
	var out []Diag
	prev := Watch
	Watch = func(d Diag) { mu.Lock(); out = append(out, d); mu.Unlock() }
	t.Cleanup(func() { Watch = prev })
	return &out
}

// An auto-payment az seat is a payment-plan consumer, so a driver builds the
// plans before it decides; at an opponent-spell-on-stack window with a
// castable counter the decision is searched, not played as the bot's tap.
func TestAZSearchesTheCounterspellWindow(t *testing.T) {
	e := counterWindow(t)
	s := payWindowSeat(t)
	if !s.WantsPaymentActions() {
		t.Fatal("an AutoPayment az seat must consume payment plans")
	}
	e.EnsurePaymentActions() // the driver's gate (internal/bench, host)
	diags := watchDiags(t)
	d := *e.Pending()
	board := botpolicy.NewBoard(2)
	b := botpolicy.BoardFromGameInto(e.G, e, d.Player, &board)
	if _, err := s.DecideSearch(context.Background(), searchseat.Env{Engine: e, Board: b}, d); err != nil {
		t.Fatal(err)
	}
	if len(*diags) != 1 || !(*diags)[0].Searched || (*diags)[0].Kind != "priority" {
		t.Fatalf("the counterspell window was not searched: %+v", *diags)
	}
}

// The backstop: with no plans built, the auto-pay bot answers a manual tap;
// the seat searches the full payment vocabulary with no bot candidate and
// counts it (Stats.BotTapRerooted) instead of playing the tap unsearched.
func TestAZReroutesABotTapIntoTheSearch(t *testing.T) {
	e := counterWindow(t)
	s := payWindowSeat(t)
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
	s = payWindowSeat(t) // a fresh rng stream, the same as the probe's
	if _, err := s.DecideSearch(context.Background(), searchseat.Env{Engine: e, Board: b}, d); err != nil {
		t.Fatal(err)
	}
	if len(*diags) != 1 || !(*diags)[0].Searched || (*diags)[0].Stats.BotTapRerooted != 1 {
		t.Fatalf("the bot's tap was not rerooted into a search: %+v", *diags)
	}
}

var _ seat.PaymentPlanConsumer = (*Seat)(nil)
