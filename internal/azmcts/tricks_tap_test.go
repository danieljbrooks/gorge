package azmcts

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/adams-shaun/gorge/botpolicy"
	"github.com/adams-shaun/gorge/decision"
	"github.com/adams-shaun/gorge/internal/searchprobe"
	"github.com/adams-shaun/gorge/internal/searchseat"
	"github.com/adams-shaun/gorge/rules"
	"github.com/adams-shaun/gorge/state"
)

// tapPosition is a trickDeck position at seat 0's own main phase, the stack
// empty and the payment plans built, where the auto-pay bot taps mana by hand
// while the rule drops a cast, and nothing the rule keeps is left to cast
// (castLeft false): the tap gate is aimed at a card the engine offers no cast
// of (beyond the seat's mana, or with no legal target, which the Board still
// prices castable), so its mana could pay only the dropped trick.
func tapPosition(t *testing.T) *rules.Engine {
	t.Helper()
	return trickPosition(t, "seat 0 taps in its own main phase, the rule dropping a cast, nothing left to cast", 0,
		func(e *rules.Engine, d *decision.Decision, bot decision.Intent) bool {
			if e.G.Active != 0 || !e.G.Step.IsMain() || len(e.G.Stack) != 0 || priorityBase(d, bot) != BaseActivate {
				return false
			}
			kept, _, _, ok := newTrickRule("").keptPriority(e, d)
			return ok && !castLeft(e, d, &kept)
		})
}

// optionOf is the index of d's first option of kind kind, -1 when none.
func optionOf(d *decision.Decision, kind string) int {
	for _, o := range d.Options {
		if o.Kind == kind {
			return o.Index
		}
	}
	return -1
}

// tapAnswer with a stub bot, at three positions: seat 0's own main phase
// with G and W floating (the dropped legacy casts of Giant Growth and
// Fleeting Flight the first options, so the kept decision is renumbered; a
// cast left, by the pool), the same without mana floating (the gated
// casts' payment actions dropped, Goblin Surprise's kept), and tapPosition
// (nothing left to cast):
//
//   - the bot is asked over the kept decision on a board of the deciding
//     seat's with the dropped cards uncastable and every other card as it
//     was, and its answer is mapped back onto the decision: pass, a mana
//     activation, a kept payment cast;
//   - its error, or an index the kept decision does not have, plays pass;
//   - a tap with nothing left to cast plays pass; with a cast left (a kept
//     payment action, a floating pool) it stands;
//   - an answer that is no tap, or a decision where the rule drops nothing,
//     is left alone and the bot is not asked.
func TestTricksTapAnswer(t *testing.T) {
	ws := trickWindows(t)
	floating := ws[twMain].Clone()
	floatMana(t, floating, "G")
	floatMana(t, floating, "W")
	floating.EnsurePaymentActions()
	for _, tc := range []struct {
		name string
		e    *rules.Engine
	}{{"own main, G and W floating", floating}, {"own main", ws[twMain]}, {"nothing left to cast", tapPosition(t)}} {
		e := tc.e
		d := e.Pending()
		r := newTrickRule("")
		kept, back, dropped, ok := r.keptPriority(e, d)
		if !ok || len(dropped) == 0 {
			t.Fatalf("%s: the rule drops nothing", tc.name)
		}
		left := castLeft(e, d, &kept)
		tap := decision.Intent{Seq: d.Seq, Player: d.Player, Choices: []int{optionOf(d, "activate")}}
		if tap.Choices[0] < 0 {
			t.Fatalf("%s: no mana activation offered", tc.name)
		}
		pass := passOf(t, d)
		keptIndex := func(kind string) int {
			for i, o := range kept.Options {
				if o.Kind == kind {
					return i
				}
			}
			t.Fatalf("%s: no %s option kept", tc.name, kind)
			return -1
		}
		reply := func(c ...int) func(botpolicy.Board, decision.Decision) (decision.Intent, error) {
			return func(_ botpolicy.Board, k decision.Decision) (decision.Intent, error) {
				return decision.Intent{Seq: k.Seq, Player: k.Player, Choices: c}, nil
			}
		}
		check := func(what string, ask func(botpolicy.Board, decision.Decision) (decision.Intent, error), want decision.Intent) {
			t.Helper()
			got, ok := r.tapAnswer(e, d, tap, ask)
			if !ok || !reflect.DeepEqual(got, want) {
				t.Fatalf("%s, %s: answered %s (%+v, ok %v), want %s", tc.name, what, CandidateLabel(d, got), got, ok, CandidateLabel(d, want))
			}
		}
		// The board the bot reads: the deciding seat's, the dropped cards
		// uncastable, the others untouched.
		asked := 0
		check("the board", func(b botpolicy.Board, k decision.Decision) (decision.Intent, error) {
			asked++
			fresh := botpolicy.BoardFromGame(e.G, e, d.Player)
			gone := map[state.ObjID]bool{}
			for _, id := range dropped {
				gone[id] = true
			}
			for _, id := range e.G.Zone(state.ZHand, d.Player) {
				c, have := b.Cards.Lookup(id)
				f := fresh.Cards.Get(id)
				if !have || c.Castable != (f.Castable && !gone[id]) {
					t.Fatalf("%s: %s in hand on the bot's board %v castable %v, want castable %v", tc.name, cardName(e, id), have, c.Castable, f.Castable && !gone[id])
				}
			}
			if !reflect.DeepEqual(k.Options, kept.Options) || len(k.PaymentActions) != len(kept.PaymentActions) {
				t.Fatalf("%s: the bot was asked over %d options and %d payment actions, want the kept %d and %d", tc.name, len(k.Options), len(k.PaymentActions), len(kept.Options), len(kept.PaymentActions))
			}
			return decision.Intent{Seq: k.Seq, Player: k.Player, Choices: []int{keptIndex("pass")}}, nil
		}, pass)
		if asked != 1 {
			t.Fatalf("%s: the bot asked %d times", tc.name, asked)
		}
		if tc.e == floating && back[keptIndex("pass")] == keptIndex("pass") {
			t.Fatalf("%s: the kept decision is not renumbered (pass at %d in both)", tc.name, keptIndex("pass"))
		}
		// A mana activation: it stands with a cast left, pass without.
		want := pass
		if left {
			want = decision.Intent{Seq: d.Seq, Player: d.Player, Choices: []int{back[keptIndex("activate")]}}
		}
		check("a tap asked again", reply(keptIndex("activate")), want)
		// A kept payment cast.
		if len(kept.PaymentActions) > 0 {
			a := kept.PaymentActions[0]
			if len(a.Plans) == 0 {
				t.Fatalf("%s: kept %s has no plan", tc.name, a.Label)
			}
			pay := decision.Intent{Seq: d.Seq, Player: d.Player, Payment: &decision.PaymentSelection{ActionID: a.ID, Plan: decision.ClonePaymentPlan(a.Plans[0])}}
			check("a kept payment cast", func(botpolicy.Board, decision.Decision) (decision.Intent, error) { return pay, nil }, pay)
		}
		// Failures play pass.
		check("an error", func(_ botpolicy.Board, k decision.Decision) (decision.Intent, error) {
			return decision.Intent{Seq: k.Seq, Player: k.Player, Choices: []int{keptIndex("activate")}}, errors.New("no answer")
		}, pass)
		check("an index past the kept options", reply(len(kept.Options)), pass)
		check("a negative index", reply(-1), pass)
		// No tap, or nothing dropped: left alone, the bot unasked.
		never := func(botpolicy.Board, decision.Decision) (decision.Intent, error) {
			t.Fatalf("%s: the bot was asked again", tc.name)
			return decision.Intent{}, nil
		}
		if got, ok := r.tapAnswer(e, d, pass, never); ok || !reflect.DeepEqual(got, pass) {
			t.Fatalf("%s: a pass answered %+v (ok %v)", tc.name, got, ok)
		}
		if got, ok := newTrickRule(nSureStrike).tapAnswer(e, d, tap, never); ok || !reflect.DeepEqual(got, tap) {
			t.Fatalf("%s, Sure Strike listed alone: a tap answered %+v (ok %v)", tc.name, got, ok)
		}
		t.Logf("%s: %d options, %d kept, %d payment actions kept, dropped %d, a cast left %v", tc.name, len(d.Options), len(kept.Options), len(kept.PaymentActions), len(dropped), left)
		if nothing := tc.name == "nothing left to cast"; left == nothing {
			t.Fatalf("%s: a cast left %v", tc.name, left)
		}
	}
}

// castLeft: a kept payment action (planned or not) or spell "cast" option, a
// floating pool, or a payment census not built, leaves a cast; a special
// action (Foretell) does not.
func TestTricksCastLeft(t *testing.T) {
	ws := trickWindows(t)
	e := ws[twMain]
	d := e.Pending()
	floating := e.Clone()
	fd := floatMana(t, floating, "G")
	floating.EnsurePaymentActions()
	if !d.PaymentActionsBuilt || !fd.PaymentActionsBuilt {
		t.Fatalf("own main: payment actions built %v; floating built %v", d.PaymentActionsBuilt, fd.PaymentActionsBuilt)
	}
	var gg state.ObjID
	for _, id := range e.G.Zone(state.ZHand, 0) {
		if cardName(e, id) == nGiantGrowth {
			gg = id
		}
	}
	unbuilt := *d
	unbuilt.PaymentActionsBuilt = false
	for _, tc := range []struct {
		name string
		e    *rules.Engine
		d    *decision.Decision
		kept decision.Decision
		want bool
	}{
		{"nothing kept", e, d, decision.Decision{}, false},
		{"a payment action without plans", e, d, decision.Decision{PaymentActions: []decision.PaymentAction{{ID: "x", Cast: decision.PlannedCast{Object: gg}}}}, true},
		{"a cast option", e, d, decision.Decision{Options: []decision.Option{{Kind: "cast", Obj: gg}}}, true},
		{"a Foretell option", e, d, decision.Decision{Options: []decision.Option{{Kind: "cast", Obj: gg, Mode: "foretell"}}}, false},
		{"a pass and a mana activation", e, d, decision.Decision{Options: []decision.Option{{Kind: "pass"}, {Index: 1, Kind: "activate"}}}, false},
		{"mana floating", floating, fd, decision.Decision{}, true},
		{"no payment census", e, &unbuilt, decision.Decision{}, true},
	} {
		if got := castLeft(tc.e, tc.d, &tc.kept); got != tc.want {
			t.Fatalf("%s: a cast left %v, want %v", tc.name, got, tc.want)
		}
	}
}

// nSureStrike is a listed card in neither of the tests' decks.
const nSureStrike = "Sure Strike"

// mapBack maps a kept decision's indices back, refusing one it does not
// have; a payment answer has none to map.
func TestTricksMapBack(t *testing.T) {
	back := []int{3, 5, 9}
	for _, tc := range []struct {
		in   []int
		want []int
		ok   bool
	}{{[]int{1}, []int{5}, true}, {[]int{2, 0}, []int{9, 3}, true}, {[]int{3}, nil, false}, {[]int{0, -1}, nil, false}, {nil, nil, true}} {
		got, ok := mapBack(decision.Intent{Seq: 4, Choices: tc.in}, back)
		if ok != tc.ok || ok && (!reflect.DeepEqual(got.Choices, tc.want) || got.Seq != 4) {
			t.Fatalf("mapBack(%v): %v %v, want %v %v", tc.in, got.Choices, ok, tc.want, tc.ok)
		}
	}
	pay := decision.Intent{Payment: &decision.PaymentSelection{ActionID: "a"}}
	if got, ok := mapBack(pay, back); !ok || !reflect.DeepEqual(got, pay) {
		t.Fatalf("mapBack of a payment answer: %+v %v", got, ok)
	}
}

// tapSeat is an auto-payment az seat (8 simulations, the test clairvoyant
// world) with Options.Tricks on or off, :holdupkeep's rule with hold, the
// prior-only seat with prior.
func tapSeat(t *testing.T, tricks, hold, prior bool, cards string) *Seat {
	t.Helper()
	sc := DefaultSeatConfig()
	sc.Search.AutoPayment, sc.Search.Sims, sc.Source = true, 8, testSeatSource
	sc.Search.Tricks, sc.Search.TricksCards, sc.Search.HoldUpkeep, sc.PriorOnly = tricks, cards, hold, prior
	s, err := NewSeat(5, nil, sc)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// The seat's root, its own bot tapping mana by hand where the rule drops a
// cast (the verifier's b-probe2: Forest and Swamp tapped in the opponent's
// main phase toward Bite Down, which the engine offered no cast of, so the
// mana could pay only the dropped Fake Your Own Death):
//
//   - nothing left to cast (tapPosition): pass is played, not the tap, and
//     counted (Stats.TricksTap), with :holdupkeep too and by the prior-only
//     seat. Before, the tap backstop searched the rule's vocabulary, found
//     pass alone and played the tap (Search with Root.NoBot shows it);
//   - the rule dropping nothing there (Sure Strike, in neither deck, listed
//     alone), or the option off: the tap goes to the backstop as before,
//     uncounted;
//   - a cast left (Fleeting Flight listed alone, Giant Growth castable): the
//     answer of the bot asked again is searched -- the same tap by the
//     backstop with no bot candidate (Stats.BotTapRerooted), uncounted, or
//     another answer as candidate 0, counted. With the default list nothing
//     is left there, and pass is the answer.
func TestTricksRootReasksABotTap(t *testing.T) {
	e := tapPosition(t)
	d := *e.Pending()
	b := botpolicy.BoardFromGame(e.G, e, d.Player)
	pass := passOf(t, &d)
	decide := func(s *Seat) (decision.Intent, []Diag) {
		t.Helper()
		diags := watchDiags(t)
		in, err := s.DecideSearch(context.Background(), searchseat.Env{Engine: e, Board: b}, d)
		if err != nil {
			t.Fatal(err)
		}
		return in, *diags
	}
	tap, err := tapSeat(t, true, false, false, "").def.DecideBoard(context.Background(), b, d)
	if err != nil || priorityBase(&d, tap) != BaseActivate {
		t.Fatalf("nothing left to cast: the seat's bot answered %+v (%v), want a tap", tap, err)
	}
	// Before: the backstop's full root with the rule's vocabulary is pass
	// alone, so the bot's tap was played.
	{
		c := e.Clone()
		obs := searchprobe.NewCollector(d.Player)
		src, _ := newTestClairvoyant(c, obs)
		opts := DefaultOptions()
		opts.Sims, opts.AutoPayment, opts.Tricks = 8, true, true
		cd := c.Pending()
		res, err := Search(context.Background(), Root{Engine: c, Decision: cd, Bot: tap, Observer: obs, NoBot: true}, src, nil, opts)
		if err != nil || res.Stats.Searched != 0 || !reflect.DeepEqual(res.Intent, tap) {
			t.Fatalf("nothing left to cast, the backstop alone: searched %d, played %s (%v)", res.Stats.Searched, CandidateLabel(cd, res.Intent), err)
		}
	}
	for _, tc := range []struct {
		name                string
		tricks, hold, prior bool
		cards               string
		reasked             bool
	}{
		{"tricks", true, false, false, "", true},
		{"tricks and holdupkeep", true, true, false, "", true},
		{"the prior-only seat", true, false, true, "", true},
		{"Sure Strike listed alone", true, false, false, nSureStrike, false},
		{"tricks off", false, false, false, "", false},
	} {
		in, diags := decide(tapSeat(t, tc.tricks, tc.hold, tc.prior, tc.cards))
		var st Stats
		for _, dg := range diags {
			st.Add(dg.Stats)
		}
		if tc.reasked {
			// The prior-only seat reports no root it does not search.
			wantTap := 1
			if tc.prior {
				wantTap = 0
			}
			if !reflect.DeepEqual(in, pass) || st.TricksTap != wantTap || st.BotTapRerooted != 0 {
				t.Fatalf("nothing left to cast, %s: played %s, tricks tap %d, tap rerooted %d; want pass, counted", tc.name, CandidateLabel(&d, in), st.TricksTap, st.BotTapRerooted)
			}
			continue
		}
		if st.TricksTap != 0 || st.BotTapRerooted != 1 || priorityBase(&d, in) == BaseActivate {
			t.Fatalf("nothing left to cast, %s: played %s, tricks tap %d, tap rerooted %d; want the backstop's search", tc.name, CandidateLabel(&d, in), st.TricksTap, st.BotTapRerooted)
		}
	}
	// A cast left: Fleeting Flight listed alone at a position where the bot
	// taps with Fleeting Flight and Giant Growth castable, so Giant Growth is
	// kept. The bot asked again is the seat's answer: the same tap, which the
	// backstop searches with no bot candidate (Giant Growth weighed), or
	// another answer, counted and searched as candidate 0.
	{
		q := trickPosition(t, "seat 1 taps in a main phase, Fleeting Flight dropped and Giant Growth castable", 1,
			func(e *rules.Engine, d *decision.Decision, bot decision.Intent) bool {
				if !e.G.Step.IsMain() || len(e.G.Stack) != 0 || priorityBase(d, bot) != BaseActivate || !castableNames(e, d)[nGiantGrowth] {
					return false
				}
				kept, _, _, ok := newTrickRule(nFlight).keptPriority(e, d)
				return ok && castLeft(e, d, &kept)
			})
		qd := *q.Pending()
		qb := botpolicy.BoardFromGame(q.G, q, qd.Player)
		s := tapSeat(t, true, true, false, nFlight)
		bot, _ := s.def.DecideBoard(context.Background(), qb, qd)
		again, ok := newTrickRule(nFlight).tapAnswer(q, &qd, bot, func(b botpolicy.Board, k decision.Decision) (decision.Intent, error) {
			return s.def.DecideBoard(context.Background(), b, k)
		})
		if !ok {
			t.Fatalf("Fleeting Flight alone: the bot's %s is not asked again", CandidateLabel(&qd, bot))
		}
		same := reflect.DeepEqual(again, bot)
		diags := watchDiags(t)
		in, err := s.DecideSearch(context.Background(), searchseat.Env{Engine: q, Board: qb}, qd)
		if err != nil {
			t.Fatal(err)
		}
		if len(*diags) != 1 {
			t.Fatalf("Fleeting Flight alone: %d diags", len(*diags))
		}
		st := (*diags)[0].Stats
		t.Logf("Fleeting Flight alone, seat 1 at turn %d %v: the bot %s, asked again %s; played %s; searched %d, tap rerooted %d, tricks tap %d",
			q.G.Turn, q.G.Step, CandidateLabel(&qd, bot), CandidateLabel(&qd, again), CandidateLabel(&qd, in), st.Searched, st.BotTapRerooted, st.TricksTap)
		if st.Searched != 1 || castName(q, &qd, in) == nFlight || priorityBase(&qd, in) == BaseActivate ||
			same && (st.TricksTap != 0 || st.BotTapRerooted != 1) || !same && (st.TricksTap != 1 || st.BotTapRerooted != 0) {
			t.Fatalf("Fleeting Flight alone: played %s, searched %d, tap rerooted %d, tricks tap %d (asked again the same %v); want it searched",
				CandidateLabel(&qd, in), st.Searched, st.BotTapRerooted, st.TricksTap, same)
		}
		// The default list drops Giant Growth too: nothing is left, pass.
		if got, ok := newTrickRule("").tapAnswer(q, &qd, bot, func(b botpolicy.Board, k decision.Decision) (decision.Intent, error) {
			return s.def.DecideBoard(context.Background(), b, k)
		}); !ok || !reflect.DeepEqual(got, passOf(t, &qd)) {
			t.Fatalf("the default list: the bot's tap answered %s (ok %v), want pass", CandidateLabel(&qd, got), ok)
		}
	}
}
