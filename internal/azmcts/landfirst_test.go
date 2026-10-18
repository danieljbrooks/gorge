package azmcts

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/adams-shaun/gorge/botpolicy"
	"github.com/adams-shaun/gorge/cards"
	"github.com/adams-shaun/gorge/decision"
	"github.com/adams-shaun/gorge/internal/searchprobe"
	"github.com/adams-shaun/gorge/internal/searchseat"
	"github.com/adams-shaun/gorge/rules"
	"github.com/adams-shaun/gorge/state"
)

// lfOption is the index of d's first option of kind kind, -1 when none.
func lfOption(d *decision.Decision, kind string) int {
	for _, o := range d.Options {
		if o.Kind == kind {
			return o.Index
		}
	}
	return -1
}

// lfHolds reports whether player p holds a card named name.
func lfHolds(g *state.Game, p state.PlayerID, name string) bool {
	for _, id := range g.Zone(state.ZHand, p) {
		if o := g.Obj(id); o != nil && o.Card != nil && len(o.Card.Faces) > 0 && o.Card.Faces[0].Name == name {
			return true
		}
	}
	return false
}

// landTapWindow plays an Island and Bear mirror with the plain bot until seat
// 0's own first main phase with the stack empty, its land drop not yet made,
// an Island and a Bear in hand and an Island untapped. The engine is left at
// that decision, with no payment plans built, where the seat's auto-pay bot
// taps the Island by hand: the Bear is castable now and no plan is built, so
// it takes the manual policy, whose tap gate runs before its land drop.
func landTapWindow(t *testing.T) *rules.Engine {
	t.Helper()
	bear := payWindowCard(t, "Name:Bear\nManaCost:1\nTypes:Creature Bear\nPT:2/2\nOracle:x\n")
	island := payWindowCard(t, "Name:Island\nTypes:Basic Land Island\nOracle:x\n")
	var deck []*cards.Card
	for i := 0; i < 14; i++ {
		deck = append(deck, island)
	}
	for i := 0; i < 10; i++ {
		deck = append(deck, bear)
	}
	for seed := uint64(1); seed < 40; seed++ {
		e := rules.New(rules.Config{Seed: seed, Names: []string{"a", "b"}, Decks: [][]*cards.Card{deck, deck}})
		e.Advance()
		rngs := searchprobe.BotRandoms(seed, 2)
		board := botpolicy.NewBoard(2)
		for steps := 0; steps < 3000 && !e.G.Over; steps++ {
			d := e.Pending()
			if d.Player == 0 && d.Kind == decision.KPriority && e.G.Active == 0 && e.G.Step == state.StepMain1 &&
				len(e.G.Stack) == 0 && e.G.Players[0].LandsPlayed == 0 && lfHolds(e.G, 0, "Island") && lfHolds(e.G, 0, "Bear") &&
				untappedIslands(e.G, 0) >= 1 && lfOption(d, "play_land") >= 0 {
				return e
			}
			in := botpolicy.Decide(botpolicy.BoardFromGameInto(e.G, e, d.Player, &board), d, rngs[d.Player])
			if err := e.Submit(in); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Fatal("no own main phase with a land drop to make and a Bear castable reached")
	return nil
}

// botLandDrop asks the bot over the decision without its mana activations
// (renumbered, the payment actions kept) and maps its answer back; only a
// land play is a land drop, and a decision with no land play offered is not
// asked.
func TestBotLandDropAsksWithoutActivations(t *testing.T) {
	d := decision.Decision{Seq: 9, Player: 1, Kind: decision.KPriority,
		PaymentActions: []decision.PaymentAction{{ID: "p"}},
		Options: []decision.Option{
			{Index: 0, Kind: "activate", Label: "Tap A"}, {Index: 1, Kind: "activate", Label: "Tap B"},
			{Index: 2, Kind: "play_land", Label: "Play Island"}, {Index: 3, Kind: "cast", Label: "Cast Bear"},
			{Index: 4, Kind: "activate", Label: "Tap C"}, {Index: 5, Kind: "play_land", Label: "Play Forest"},
			{Index: 6, Kind: "pass", Label: "Pass"},
		}}
	without, back := withoutActivations(d)
	if !reflect.DeepEqual(back, []int{2, 3, 5, 6}) || len(without.Options) != 4 || len(without.PaymentActions) != 1 || len(d.Options) != 7 {
		t.Fatalf("without activations: back %v, %d options, %d payment actions (the decision's own %d options)", back, len(without.Options), len(without.PaymentActions), len(d.Options))
	}
	for i, o := range without.Options {
		if o.Index != i || o.Kind == "activate" || o.Label != d.Options[back[i]].Label {
			t.Fatalf("without activations, option %d: %+v", i, o)
		}
	}
	asked := 0
	reply := func(in decision.Intent, err error) func(decision.Decision) (decision.Intent, error) {
		return func(k decision.Decision) (decision.Intent, error) {
			asked++
			if !reflect.DeepEqual(k.Options, without.Options) || len(k.PaymentActions) != 1 || k.Seq != d.Seq {
				t.Fatalf("the bot was asked over %+v", k)
			}
			return in, err
		}
	}
	pick := func(c ...int) decision.Intent { return decision.Intent{Seq: 9, Player: 1, Choices: c} }
	for _, tc := range []struct {
		name string
		in   decision.Intent
		err  error
		want []int // nil: no land drop
	}{
		{"the first land", pick(0), nil, []int{2}},
		{"the second land", pick(2), nil, []int{5}},
		{"a cast", pick(1), nil, nil},
		{"pass", pick(3), nil, nil},
		{"an index past the options", pick(4), nil, nil},
		{"a negative index", pick(-1), nil, nil},
		{"two options", pick(0, 2), nil, nil},
		{"a payment", decision.Intent{Seq: 9, Player: 1, Payment: &decision.PaymentSelection{ActionID: "p"}}, nil, nil},
		{"an error", pick(0), errors.New("no answer"), nil},
	} {
		asked = 0
		got, ok := botLandDrop(&d, reply(tc.in, tc.err))
		if asked != 1 || ok != (tc.want != nil) || ok && (!reflect.DeepEqual(got.Choices, tc.want) || got.Seq != 9 || got.Player != 1) {
			t.Fatalf("%s: %+v ok %v (asked %d), want %v", tc.name, got, ok, asked, tc.want)
		}
	}
	noLand := d
	noLand.Options = []decision.Option{{Index: 0, Kind: "activate"}, {Index: 1, Kind: "pass"}}
	asked = 0
	if _, ok := botLandDrop(&noLand, reply(pick(0), nil)); ok || asked != 0 {
		t.Fatalf("no land play offered: ok %v, the bot asked %d times", ok, asked)
	}
}

// The bot-tap backstop at seat 0's own main phase with its land drop to make
// (landTapWindow): the seat's auto-pay bot taps an Island by hand (its tap
// gate runs before its land drop). Before, the backstop searched the payment
// vocabulary with no bot candidate, which holds no land play, so a search
// that chose pass ended the main phase without the land drop. Now the bot is
// asked again without its mana activations and its land play is played,
// unsearched (Stats.LandFirst); the next decision's tap, with no land play
// left, is rerooted as before. A ManualTaps seat still plays the tap.
func TestAZPlaysTheLandBeforeRerootingABotTap(t *testing.T) {
	ctx := context.Background()
	e := landTapWindow(t)
	start := e.Clone()
	d := *e.Pending()
	board := botpolicy.NewBoard(2)
	b := botpolicy.BoardFromGameInto(e.G, e, d.Player, &board)
	tap, err := payWindowSeat(t).def.DecideBoard(ctx, b, d)
	if err != nil || priorityBase(&d, tap) != BaseActivate {
		t.Fatalf("the seat's bot answered %+v (%v), want a mana tap", tap, err)
	}
	without, back := withoutActivations(d)
	land, err := payWindowSeat(t).def.DecideBoard(ctx, b, without)
	if err != nil || len(land.Choices) != 1 {
		t.Fatalf("the bot without activations answered %+v (%v)", land, err)
	}
	land.Choices = []int{back[land.Choices[0]]}
	if priorityBase(&d, land) != BasePlayLand {
		t.Fatalf("the bot without activations answered %s, want a land play", CandidateLabel(&d, land))
	}

	// Before: the backstop's root (NoBot, no BotAsk) is searched, and none of
	// its candidates plays a land.
	{
		c := start.Clone()
		cd := c.Pending()
		obs := searchprobe.NewCollector(cd.Player)
		src, _ := newTestClairvoyant(c, obs)
		opts := DefaultOptions()
		opts.Sims, opts.AutoPayment = 8, true
		res, err := Search(ctx, Root{Engine: c, Decision: cd, Bot: tap, Observer: obs, NoBot: true}, src, nil, opts)
		if err != nil || res.Stats.Searched != 1 || res.Stats.LandFirst != 0 {
			t.Fatalf("the backstop alone: searched %d, land first %d (%v)", res.Stats.Searched, res.Stats.LandFirst, err)
		}
		for i, in := range res.Candidates {
			if priorityBase(cd, in) == BasePlayLand {
				t.Fatalf("the backstop alone: candidate %d plays a land", i)
			}
		}
		t.Logf("the backstop alone: %d candidates %v, played %s", len(res.Candidates), res.Labels, CandidateLabel(cd, res.Intent))
	}

	// Now: the land drop, unsearched.
	diags := watchDiags(t)
	s := payWindowSeat(t)
	in, err := s.DecideSearch(ctx, searchseat.Env{Engine: e, Board: b}, d)
	if err != nil {
		t.Fatal(err)
	}
	if len(*diags) != 1 {
		t.Fatalf("%d diags", len(*diags))
	}
	var st Stats // through Stats.Add, as play's summary and botbench read it
	st.Add((*diags)[0].Stats)
	if dg := (*diags)[0]; !reflect.DeepEqual(in, land) || dg.Searched || st.LandFirst != 1 || st.Skipped != 1 || st.BotTapRerooted != 0 ||
		st.PrioritySkipped[BasePlayLand][SkipFewCandidates] != 1 {
		t.Fatalf("played %s (searched %v, land first %d, skipped %d, tap rerooted %d), want the bot's %s unsearched",
			CandidateLabel(&d, in), dg.Searched, st.LandFirst, st.Skipped, st.BotTapRerooted, CandidateLabel(&d, land))
	}
	if err := e.Submit(in); err != nil {
		t.Fatal(err)
	}
	if e.G.Players[0].LandsPlayed != 1 {
		t.Fatalf("lands played %d after the land drop", e.G.Players[0].LandsPlayed)
	}

	// The next decision: the bot taps again, no land play is offered, and the
	// backstop reroots as before.
	d2 := *e.Pending()
	b2 := botpolicy.BoardFromGameInto(e.G, e, d2.Player, &board)
	if d2.Player != 0 || d2.Kind != decision.KPriority || lfOption(&d2, "play_land") >= 0 {
		t.Fatalf("after the land drop: %s decision of player %d, land offered %v", d2.Kind, d2.Player, lfOption(&d2, "play_land") >= 0)
	}
	tap2, _ := payWindowSeat(t).def.DecideBoard(ctx, b2, d2)
	if priorityBase(&d2, tap2) != BaseActivate {
		t.Skipf("after the land drop the bot answered %s, not a tap; the reroot is not exercised", CandidateLabel(&d2, tap2))
	}
	*diags = nil
	if _, err := s.DecideSearch(ctx, searchseat.Env{Engine: e, Board: b2}, d2); err != nil {
		t.Fatal(err)
	}
	if len(*diags) != 1 || !(*diags)[0].Searched || (*diags)[0].Stats.BotTapRerooted != 1 || (*diags)[0].Stats.LandFirst != 0 {
		t.Fatalf("after the land drop the bot's tap was not rerooted: %+v", *diags)
	}

	// A ManualTaps seat: the tap, unsearched, no land drop first.
	{
		sc := DefaultSeatConfig()
		sc.Search.AutoPayment, sc.Search.Sims, sc.Source, sc.ManualTaps = true, 8, testSeatSource, true
		m, err := NewSeat(5, nil, sc)
		if err != nil {
			t.Fatal(err)
		}
		c := start.Clone()
		cd := *c.Pending()
		cb := botpolicy.BoardFromGameInto(c.G, c, cd.Player, &board)
		*diags = nil
		in, err := m.DecideSearch(ctx, searchseat.Env{Engine: c, Board: cb}, cd)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(in, tap) || len(*diags) != 1 || (*diags)[0].Searched || (*diags)[0].Stats.LandFirst != 0 {
			t.Fatalf("a ManualTaps seat played %s (%+v), want the bot's tap unsearched", CandidateLabel(&cd, in), *diags)
		}
	}
}

// Root.landFirst at Search: under NoBot with BotAsk set, a bot answering a
// land play makes the root skip and play it; a bot answering anything else,
// or failing, leaves the reroot searched as before; and it applies only to
// a NoBot root alone (no Macros, no BotKey) at a priority root under
// AutoPayment with priority searched.
func TestRootLandFirst(t *testing.T) {
	ctx := context.Background()
	start := landTapWindow(t)
	d0 := start.Pending()
	land := decision.Intent{Seq: d0.Seq, Player: d0.Player, Choices: []int{lfOption(d0, "play_land")}}
	tap := decision.Intent{Seq: d0.Seq, Player: d0.Player, Choices: []int{lfOption(d0, "activate")}}
	if land.Choices[0] < 0 || tap.Choices[0] < 0 {
		t.Fatalf("options %+v", d0.Options)
	}
	// The bot's answer over the decision without activations, by kind.
	answer := func(kind string) func(decision.Decision) (decision.Intent, error) {
		return func(k decision.Decision) (decision.Intent, error) {
			if lfOption(&k, "activate") >= 0 {
				t.Fatal("asked with a mana activation offered")
			}
			return decision.Intent{Seq: k.Seq, Player: k.Player, Choices: []int{lfOption(&k, kind)}}, nil
		}
	}
	failing := func(decision.Decision) (decision.Intent, error) { return decision.Intent{}, errors.New("no answer") }
	for _, tc := range []struct {
		name   string
		ask    func(decision.Decision) (decision.Intent, error)
		mod    func(*Root, *Options)
		landed bool
	}{
		{"the bot plays a land", answer("play_land"), nil, true},
		{"the bot passes", answer("pass"), nil, false},
		{"the bot fails", failing, nil, false},
		{"no BotAsk", nil, nil, false},
		{"not rerooted", answer("play_land"), func(r *Root, _ *Options) { r.NoBot = false }, false},
		{"a BotKey", answer("play_land"), func(r *Root, _ *Options) { r.BotKey = "macro:x" }, false},
		{"macros", answer("play_land"), func(r *Root, _ *Options) {
			r.Macros = []Macro{{Key: "macro:x", Label: "x", Steps: []MacroStep{{Player: r.Decision.Player, Kind: decision.KPriority, PayCast: 1 << 30}}}}
		}, false},
		{"no AutoPayment", answer("play_land"), func(_ *Root, o *Options) { o.AutoPayment = false }, false},
		{"priority not searched", answer("play_land"), func(_ *Root, o *Options) { o.Kinds.Priority = false }, false},
	} {
		c := start.Clone()
		cd := c.Pending()
		obs := searchprobe.NewCollector(cd.Player)
		src, _ := newTestClairvoyant(c, obs)
		opts := DefaultOptions()
		opts.Sims, opts.AutoPayment = 8, true
		root := Root{Engine: c, Decision: cd, Bot: tap, Observer: obs, NoBot: true, BotAsk: tc.ask}
		if tc.mod != nil {
			tc.mod(&root, &opts)
		}
		res, err := Search(ctx, root, src, nil, opts)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if tc.landed {
			if !reflect.DeepEqual(res.Intent, land) || res.Stats.LandFirst != 1 || res.Stats.Searched != 0 || res.NoBot {
				t.Fatalf("%s: played %s, land first %d, searched %d, no bot %v; want the land unsearched", tc.name, CandidateLabel(cd, res.Intent), res.Stats.LandFirst, res.Stats.Searched, res.NoBot)
			}
			continue
		}
		if res.Stats.LandFirst != 0 || priorityBase(cd, res.Intent) == BasePlayLand {
			t.Fatalf("%s: played %s, land first %d; want no land drop", tc.name, CandidateLabel(cd, res.Intent), res.Stats.LandFirst)
		}
		if root.NoBot && root.BotKey == "" && len(root.Macros) == 0 && opts.AutoPayment && opts.Kinds.Priority && (res.Stats.Searched != 1 || !res.NoBot) {
			t.Fatalf("%s: searched %d, no bot %v; want the reroot as before", tc.name, res.Stats.Searched, res.NoBot)
		}
	}
}

// The reroot without a land play offered is unchanged: at the counterspell
// window (the opponent's turn), the seat's rerooted search is exactly the
// backstop's own (Search with NoBot and no BotAsk, the seat's options and
// world): the same answer, choice, candidates and counters.
func TestAZRerootWithoutALandUnchanged(t *testing.T) {
	ctx := context.Background()
	e := counterWindow(t)
	d0 := *e.Pending()
	if lfOption(&d0, "play_land") >= 0 {
		t.Fatal("the counterspell window offers a land play")
	}
	board := botpolicy.NewBoard(2)
	s := payWindowSeat(t)
	tap, err := s.def.DecideBoard(ctx, botpolicy.BoardFromGameInto(e.G, e, d0.Player, &board), d0)
	if err != nil || priorityBase(&d0, tap) != BaseActivate {
		t.Skipf("the plan-less bot did not tap here (%+v); the backstop is not exercised", tap)
	}
	c := e.Clone()
	cd := *c.Pending()
	obs := searchprobe.NewCollector(cd.Player)
	src, _ := newTestClairvoyant(c, obs)
	opts, _ := s.rootOptions(&cd, c.G.Turn)
	want, err := Search(ctx, Root{Engine: c, Decision: &cd, Bot: tap, Observer: obs, NoBot: true}, src, nil, opts)
	if err != nil || want.Stats.Searched != 1 {
		t.Fatalf("the backstop alone: searched %d (%v)", want.Stats.Searched, err)
	}
	want.Stats.BotTapRerooted = 1

	diags := watchDiags(t)
	c2 := e.Clone()
	d := *c2.Pending()
	in, err := payWindowSeat(t).DecideSearch(ctx, searchseat.Env{Engine: c2, Board: botpolicy.BoardFromGameInto(c2.G, c2, d.Player, &board)}, d)
	if err != nil {
		t.Fatal(err)
	}
	if len(*diags) != 1 {
		t.Fatalf("%d diags", len(*diags))
	}
	dg := (*diags)[0]
	if !reflect.DeepEqual(in, want.Intent) || dg.Choice != want.Choice || dg.Candidates != len(want.Candidates) || !reflect.DeepEqual(dg.Stats, want.Stats) {
		t.Fatalf("the seat's reroot: played %s, choice %d of %d, stats %+v; the backstop's: %s, choice %d of %d, stats %+v",
			CandidateLabel(&d, in), dg.Choice, dg.Candidates, dg.Stats, CandidateLabel(&cd, want.Intent), want.Choice, len(want.Candidates), want.Stats)
	}
}
