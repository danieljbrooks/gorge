package azmcts

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/adams-shaun/gorge/botpolicy"
	"github.com/adams-shaun/gorge/decision"
	"github.com/adams-shaun/gorge/internal/searchprobe"
	"github.com/adams-shaun/gorge/internal/searchseat"
	"github.com/adams-shaun/gorge/rules"
	"github.com/adams-shaun/gorge/seat"
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

// tapLandPosition is tapPosition with a land play offered: seat 0's own main
// phase, its land drop not made, the auto-pay bot tapping by hand (its tap
// gate runs before its land drop) while the rule drops a cast and nothing it
// keeps is left to cast.
func tapLandPosition(t *testing.T) *rules.Engine {
	t.Helper()
	return trickPosition(t, "seat 0 taps in its own main phase with a land play offered, the rule dropping a cast, nothing left to cast", 0,
		func(e *rules.Engine, d *decision.Decision, bot decision.Intent) bool {
			if e.G.Active != 0 || !e.G.Step.IsMain() || len(e.G.Stack) != 0 || priorityBase(d, bot) != BaseActivate || optionOf(d, "play_land") < 0 {
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
//   - a tap with nothing left to cast: the bot is asked once more, over the
//     kept decision without its mana activations, and its pass, land drop
//     or ability is mapped back through both index maps and played (the
//     ability where tapLandPosition's renumbered decision also offers one
//     last); a failure there plays pass. With a cast left (a kept payment
//     action, a floating pool) the tap stands;
//   - an answer that is no tap, or a decision where the rule drops nothing,
//     is left alone and the bot is not asked.
func TestTricksTapAnswer(t *testing.T) {
	ws := trickWindows(t)
	floating := ws[twMain].Clone()
	floatMana(t, floating, "G")
	floatMana(t, floating, "W")
	floating.EnsurePaymentActions()
	landPos := tapLandPosition(t)
	// tapLandPosition's decision with a "cast" option of the dropped card
	// first (a legacy cast the rule drops), so the kept decision is
	// renumbered while nothing is left to cast.
	renumbered := *landPos.Pending()
	{
		_, _, dropped, _ := newTrickRule("").keptPriority(landPos, &renumbered)
		opts := []decision.Option{{Index: 0, Kind: "cast", Obj: dropped[0], Player: renumbered.Player, Label: "Cast " + cardName(landPos, dropped[0])}}
		for _, o := range renumbered.Options {
			o.Index = len(opts)
			opts = append(opts, o)
		}
		renumbered.Options = opts
	}
	// The same with an ability offered last, after the activations, so the
	// ability's index differs in d, in the kept decision and without the
	// activations.
	withAbility := renumbered
	{
		prism := renumbered.Options[optionOf(&renumbered, "activate")].Obj
		withAbility.Options = append(append([]decision.Option(nil), renumbered.Options...), decision.Option{
			Index: len(renumbered.Options), Kind: "ability", Obj: prism, Player: renumbered.Player, Label: "Activate " + cardName(landPos, prism) + " (an ability)"})
	}
	for _, tc := range []struct {
		name string
		e    *rules.Engine
		d    *decision.Decision // nil: e's pending decision
	}{{"own main, G and W floating", floating, nil}, {"own main", ws[twMain], nil}, {"nothing left to cast", tapPosition(t), nil},
		{"nothing left to cast, a land play offered", landPos, nil},
		{"nothing left to cast, a land play offered, a dropped cast option first", landPos, &renumbered},
		{"nothing left to cast, a land play offered, a dropped cast option first and an ability last", landPos, &withAbility}} {
		e := tc.e
		d := tc.d
		if d == nil {
			d = e.Pending()
		}
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
		if (tc.e == floating || tc.d != nil) && back[keptIndex("pass")] == keptIndex("pass") {
			t.Fatalf("%s: the kept decision is not renumbered (pass at %d in both)", tc.name, keptIndex("pass"))
		}
		// A mana activation: it stands with a cast left. With nothing left
		// the bot is asked once more, over the kept decision without its mana
		// activations, and that answer is mapped back through both index
		// maps: pass, the land drop; a failure plays pass.
		without, wback := withoutActivations(kept)
		for i, k := range wback {
			wback[i] = back[k]
		}
		withoutIndex := func(kind string) int {
			for i, o := range without.Options {
				if o.Kind == kind {
					return i
				}
			}
			t.Fatalf("%s: no %s option kept without the activations", tc.name, kind)
			return -1
		}
		// tapThen answers the tap first, then (over the decision without
		// activations, checked) the answer second gives.
		tapThen := func(second func(decision.Decision) (decision.Intent, error)) func(botpolicy.Board, decision.Decision) (decision.Intent, error) {
			asks := 0
			return func(b botpolicy.Board, k decision.Decision) (decision.Intent, error) {
				asks++
				switch asks {
				case 1:
					return decision.Intent{Seq: k.Seq, Player: k.Player, Choices: []int{keptIndex("activate")}}, nil
				case 2:
					if left {
						t.Fatalf("%s: the bot asked a second time with a cast left", tc.name)
					}
					// The same marked board: the dropped cards uncastable.
					for _, id := range dropped {
						if c, have := b.Cards.Lookup(id); have && c.Castable {
							t.Fatalf("%s: the second ask's board calls the dropped %s castable", tc.name, cardName(e, id))
						}
					}
					if !reflect.DeepEqual(k.Options, without.Options) || !reflect.DeepEqual(k.PaymentActions, without.PaymentActions) {
						t.Fatalf("%s: the second ask was over %d options and %d payment actions, want the kept decision's without its activations (%d, %d)",
							tc.name, len(k.Options), len(k.PaymentActions), len(without.Options), len(without.PaymentActions))
					}
					return second(k)
				}
				t.Fatalf("%s: the bot asked %d times", tc.name, asks)
				return decision.Intent{}, nil
			}
		}
		answer := func(c ...int) func(decision.Decision) (decision.Intent, error) {
			return func(k decision.Decision) (decision.Intent, error) {
				return decision.Intent{Seq: k.Seq, Player: k.Player, Choices: c}, nil
			}
		}
		if left {
			check("a tap asked again", tapThen(nil), decision.Intent{Seq: d.Seq, Player: d.Player, Choices: []int{back[keptIndex("activate")]}})
		} else {
			check("a tap asked again, then pass", tapThen(answer(withoutIndex("pass"))), pass)
			if wback[withoutIndex("pass")] != pass.Choices[0] {
				t.Fatalf("%s: pass maps to %d, want %d", tc.name, wback[withoutIndex("pass")], pass.Choices[0])
			}
			// An error with an answer that is no pass (the land play where one
			// is offered): pass all the same.
			other := withoutIndex("pass")
			for i, o := range without.Options {
				if o.Kind != "pass" && (o.Kind == "play_land" || without.Options[other].Kind == "pass") {
					other = i
				}
			}
			check("a tap asked again, then an error", tapThen(func(k decision.Decision) (decision.Intent, error) {
				return decision.Intent{Seq: k.Seq, Player: k.Player, Choices: []int{other}}, errors.New("no answer")
			}), pass)
			check("a tap asked again, then an index past the options", tapThen(answer(len(without.Options))), pass)
			check("a tap asked again, then a negative index", tapThen(answer(-1)), pass)
			if optionOf(d, "play_land") >= 0 {
				land := decision.Intent{Seq: d.Seq, Player: d.Player, Choices: []int{wback[withoutIndex("play_land")]}}
				if priorityBase(d, land) != BasePlayLand {
					t.Fatalf("%s: the land play maps to %s", tc.name, CandidateLabel(d, land))
				}
				check("a tap asked again, then the land drop", tapThen(answer(withoutIndex("play_land"))), land)
			}
			// The second ask answers the ability, as found in the decision it
			// is asked over: mapped back through both maps, d's own ability is
			// played.
			if a := optionOf(d, "ability"); a >= 0 {
				ability := decision.Intent{Seq: d.Seq, Player: d.Player, Choices: []int{a}}
				check("a tap asked again, then an ability", tapThen(func(k decision.Decision) (decision.Intent, error) {
					return decision.Intent{Seq: k.Seq, Player: k.Player, Choices: []int{optionOf(&k, "ability")}}, nil
				}), ability)
				if wi, ki := withoutIndex("ability"), keptIndex("ability"); wi == ki || ki == a || priorityBase(d, ability) != BaseAbility {
					t.Fatalf("%s: the ability at %d without the activations, %d kept, %d in d: want it renumbered by both maps", tc.name, wi, ki, a)
				}
			}
		}
		// A land play asked again stands, whatever is left.
		if optionOf(d, "play_land") >= 0 {
			check("a land play asked again", reply(keptIndex("play_land")), decision.Intent{Seq: d.Seq, Player: d.Player, Choices: []int{back[keptIndex("play_land")]}})
		}
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
		if nothing := strings.HasPrefix(tc.name, "nothing left to cast"); left == nothing {
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
		{"an ability and a land play", e, d, decision.Decision{Options: []decision.Option{{Kind: "pass"}, {Index: 1, Kind: "activate"},
			{Index: 2, Kind: "ability", Obj: gg}, {Index: 3, Kind: "play_land", Obj: gg}}}, false},
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

// The seat's root at tapLandPosition (nothing left to cast, a land play
// offered; the verifier's v3probe: the bot asked again there passed, so the
// seat passed and its main phase ended without the land drop at all 36 such
// positions of playTricks' games):
//
//   - with :tricks (also with :holdupkeep), the bot asked again taps again
//     with nothing left, is asked once more without its mana activations,
//     and its land drop is played (TricksTap 1); the prior-only seat plays
//     it too;
//   - with the rule dropping nothing (Sure Strike listed alone) or off, the
//     tap goes to the backstop, whose land drop comes first (LandFirst 1):
//     the same land, unsearched, and no replaced bot answer (TricksBot 0,
//     as on every row);
//   - a seat with Sims 0 plays its bot's tap, the rule unapplied.
func TestTricksRootPlaysTheLandDropOverATap(t *testing.T) {
	e := tapLandPosition(t)
	d := *e.Pending()
	b := botpolicy.BoardFromGame(e.G, e, d.Player)
	ctx := context.Background()
	tap, err := tapSeat(t, true, false, false, "").def.DecideBoard(ctx, b, d)
	if err != nil || priorityBase(&d, tap) != BaseActivate {
		t.Fatalf("the seat's bot answered %+v (%v), want a tap", tap, err)
	}
	// The bot's land drop: its answer over the decision without activations.
	without, back := withoutActivations(d)
	asked, _ := tapSeat(t, true, false, false, "").def.DecideBoard(ctx, b, without)
	land, ok := mapBack(asked, back)
	if !ok || priorityBase(&d, land) != BasePlayLand {
		t.Fatalf("the bot without its activations answered %s, want a land play", CandidateLabel(&d, land))
	}
	for _, tc := range []struct {
		name                string
		tricks, hold, prior bool
		cards               string
		tapped, landFirst   int
	}{
		{"tricks", true, false, false, "", 1, 0},
		{"tricks and holdupkeep", true, true, false, "", 1, 0},
		{"the prior-only seat", true, false, true, "", 0, 0}, // no Diag below two candidates
		{"Sure Strike listed alone", true, false, false, nSureStrike, 0, 1},
		{"tricks off", false, false, false, "", 0, 1},
	} {
		diags := watchDiags(t)
		in, err := tapSeat(t, tc.tricks, tc.hold, tc.prior, tc.cards).DecideSearch(ctx, searchseat.Env{Engine: e, Board: b}, d)
		if err != nil {
			t.Fatal(err)
		}
		var st Stats
		for _, dg := range *diags {
			st.Add(dg.Stats)
		}
		// TricksBot stays 0 on every row: the rule never replaced the root's
		// bot answer (the tap, or the land drop trickTap left), and where it
		// drops nothing the backstop's land drop is no replacement of its.
		if !reflect.DeepEqual(in, land) || st.TricksTap != tc.tapped || st.LandFirst != tc.landFirst || st.Searched != 0 || st.BotTapRerooted != 0 || st.TricksBot != 0 {
			t.Fatalf("%s: played %s (tricks tap %d, land first %d, searched %d, tap rerooted %d, tricks bot %d), want the land drop %s (tricks tap %d, land first %d, tricks bot 0)",
				tc.name, CandidateLabel(&d, in), st.TricksTap, st.LandFirst, st.Searched, st.BotTapRerooted, st.TricksBot, CandidateLabel(&d, land), tc.tapped, tc.landFirst)
		}
	}
	// Sims 0: no Search, the bot's tap as it answered.
	for _, at := range []struct {
		name string
		e    *rules.Engine
	}{{"a land play offered", e}, {"nothing left to cast", tapPosition(t)}} {
		sc := DefaultSeatConfig()
		sc.Search.AutoPayment, sc.Search.Sims, sc.Source, sc.Search.Tricks = true, 0, testSeatSource, true
		s, err := NewSeat(5, nil, sc)
		if err != nil {
			t.Fatal(err)
		}
		ad := *at.e.Pending()
		ab := botpolicy.BoardFromGame(at.e.G, at.e, ad.Player)
		want, _ := s.def.DecideBoard(ctx, ab, ad)
		in, err := s.DecideSearch(ctx, searchseat.Env{Engine: at.e, Board: ab}, ad)
		if err != nil || !reflect.DeepEqual(in, want) || priorityBase(&ad, in) != BaseActivate {
			t.Fatalf("Sims 0, %s: played %s (%v), want the bot's tap %s", at.name, CandidateLabel(&ad, in), err, CandidateLabel(&ad, want))
		}
	}
}

// The tap is asked of the seat's own wrapped bot (Seat.def), the auto-pay
// bot under AutoPayment: at its own main phase, the rule dropping the gated
// casts and keeping Goblin Surprise's planned cast, a tap is answered by what
// that bot answers over the kept decision on the marked board (the planned
// cast, or the land drop: no tap), where a plain manual bot would tap again.
func TestTricksRootTapAsksTheSeatsBot(t *testing.T) {
	ctx := context.Background()
	e := trickWindows(t)[twMain]
	d := *e.Pending()
	tap := decision.Intent{Seq: d.Seq, Player: d.Player, Choices: []int{optionOf(&d, "activate")}}
	if tap.Choices[0] < 0 {
		t.Fatal("own main: no mana activation offered")
	}
	r := newTrickRule("")
	kept, back, dropped, ok := r.keptPriority(e, &d)
	if !ok {
		t.Fatal("own main: the rule drops nothing")
	}
	marked := func() botpolicy.Board {
		b := botpolicy.BoardFromGame(e.G, e, d.Player)
		uncastable(b, dropped)
		return b
	}
	s := tapSeat(t, true, false, false, "")
	want, _ := s.def.DecideBoard(ctx, marked(), kept)
	want, _ = mapBack(want, back)
	manual, _ := seat.NewBot(5).DecideBoard(ctx, marked(), kept)
	manual, _ = mapBack(manual, back)
	if priorityBase(&d, want) == BaseActivate || priorityBase(&d, manual) != BaseActivate {
		t.Fatalf("own main: the auto-pay bot asked again answers %s and a manual bot %s; want no tap and a tap", CandidateLabel(&d, want), CandidateLabel(&d, manual))
	}
	got, tapped := s.trickTap(ctx, e, &d, tap)
	if !tapped || !reflect.DeepEqual(got, want) {
		t.Fatalf("own main: the seat's tap answered %s (tapped %v), want its bot's %s", CandidateLabel(&d, got), tapped, CandidateLabel(&d, want))
	}
	t.Logf("own main: the seat's tap answered %s (a manual bot: %s)", CandidateLabel(&d, got), CandidateLabel(&d, manual))
}

// The prior-only seat counts a replaced tap (Stats.TricksTap) at a root it
// reports, one with two or more candidates: own main, the answer trickTap
// left searched over pass, Goblin Surprise and the other kept casts.
func TestTricksPriorSeatCountsAReplacedTap(t *testing.T) {
	ctx := context.Background()
	e := trickWindows(t)[twMain]
	d := *e.Pending()
	b := botpolicy.BoardFromGame(e.G, e, d.Player)
	for _, tapped := range []bool{true, false} {
		diags := watchDiags(t)
		if _, err := tapSeat(t, true, false, true, "").decidePrior(ctx, searchseat.Env{Engine: e, Board: b}, d, passOf(t, &d), tapped); err != nil {
			t.Fatal(err)
		}
		want := map[bool]int{true: 1, false: 0}[tapped]
		if len(*diags) != 1 || (*diags)[0].Candidates < 2 || (*diags)[0].Stats.TricksTap != want {
			t.Fatalf("tapped %v: diags %+v, want one root of two or more candidates with TricksTap %d", tapped, *diags, want)
		}
	}
}

// Options.Tricks' reroot of a dropped bot cast plays the bot's land drop
// first, as the bot-tap backstop does: at its own main phase with a land
// play offered, a bot answer of Giant Growth (outside its windows) is
// replaced by the land the bot plays when asked again without its mana
// activations (Root.BotAsk; LandFirst and TricksBot 1, unsearched). Without
// BotAsk it is rerooted as before; the prior-only seat plays the land too.
func TestTricksRerootPlaysTheLandDropFirst(t *testing.T) {
	ctx := context.Background()
	e := trickPosition(t, "seat 0's own main phase with a land play offered and Giant Growth castable", 0,
		func(e *rules.Engine, d *decision.Decision, _ decision.Intent) bool {
			return e.G.Active == 0 && e.G.Step.IsMain() && len(e.G.Stack) == 0 && optionOf(d, "play_land") >= 0 && optionOf(d, "pass") >= 0 &&
				castableNames(e, d)[nGiantGrowth]
		})
	d := e.Pending()
	gg := payCastOf(t, e, d, nGiantGrowth)
	bot := seat.NewBot(5).EnableAutoPayMana()
	ask := func(k decision.Decision) (decision.Intent, error) {
		return bot.DecideBoard(ctx, botpolicy.BoardFromGame(e.G, e, d.Player), k)
	}
	land, ok := botLandDrop(d, ask)
	if !ok {
		t.Fatal("the bot plays no land when asked without its activations")
	}
	for _, hold := range []bool{false, true} {
		for _, withAsk := range []bool{true, false} {
			c := e.Clone()
			cd := c.Pending()
			obs := searchprobe.NewCollector(cd.Player)
			src, _ := newTestClairvoyant(c, obs)
			opts := DefaultOptions()
			opts.Sims, opts.AutoPayment, opts.Tricks, opts.HoldUpkeep = 8, true, true, hold
			root := Root{Engine: c, Decision: cd, Bot: gg, Observer: obs}
			if withAsk {
				root.BotAsk = ask
			}
			res, err := Search(ctx, root, src, nil, opts)
			if err != nil {
				t.Fatal(err)
			}
			if res.Stats.TricksBot != 1 || castName(c, cd, res.Intent) == nGiantGrowth {
				t.Fatalf("hold %v, BotAsk %v: played %s, tricks bot %d", hold, withAsk, CandidateLabel(cd, res.Intent), res.Stats.TricksBot)
			}
			if withAsk {
				if !reflect.DeepEqual(res.Intent, land) || res.Stats.LandFirst != 1 || res.Stats.Searched != 0 || res.NoBot {
					t.Fatalf("hold %v: played %s, land first %d, searched %d, no bot %v; want the land drop unsearched",
						hold, CandidateLabel(cd, res.Intent), res.Stats.LandFirst, res.Stats.Searched, res.NoBot)
				}
				continue
			}
			if res.Stats.LandFirst != 0 || priorityBase(cd, res.Intent) == BasePlayLand {
				t.Fatalf("hold %v, no BotAsk: played %s, land first %d; want the reroot as before", hold, CandidateLabel(cd, res.Intent), res.Stats.LandFirst)
			}
		}
	}
	// The prior-only seat, Giant Growth its bot's answer.
	s := tapSeat(t, true, false, true, "")
	in, err := s.decidePrior(ctx, searchseat.Env{Engine: e, Board: botpolicy.BoardFromGame(e.G, e, d.Player)}, *d, gg, false)
	if err != nil || !reflect.DeepEqual(in, land) {
		t.Fatalf("the prior-only seat played %s (%v), want the land drop %s", CandidateLabel(d, in), err, CandidateLabel(d, land))
	}
}

// The land :tricks' reroot and the backstop play first is the bot's own
// choice, read from the seat's board (Root.BotAsk over env.Board): at
// colourLandWindow with Giant Growth as the green card (an Island and a
// Forest offered, the Island first, Giant Growth's {G} unmet), where a bot
// that reads no board plays the first land offered, the Island:
//
//   - the prior-only seat (decidePrior), its bot's answer Giant Growth's
//     cast outside its windows (the cast option added: the auto-pay bot
//     itself plays a land before any cast): the rule reroots it and plays
//     the Forest, with :holdupkeep too;
//   - DecideSearch with :tricks (also with :holdupkeep), its bot tapping by
//     hand toward a Bear: the backstop plays the Forest unsearched
//     (LandFirst 1), no replaced bot answer (TricksBot 0).
func TestTricksPlaysTheBotsColourLandFirst(t *testing.T) {
	ctx := context.Background()
	e := colourLandWindow(t, payWindowCard(t, scriptGiantGrowth))
	d := *e.Pending()
	b := botpolicy.BoardFromGame(e.G, e, d.Player)
	forest, island := lfLandPlay(e, &d, "Forest"), lfLandPlay(e, &d, "Island")
	without, back := withoutActivations(d)
	blind, err := tapSeat(t, true, false, false, "").def.DecideBoard(ctx, botpolicy.NewBoard(2), without)
	if blind, ok := mapBack(blind, back); err != nil || !ok || !reflect.DeepEqual(blind, island) {
		t.Fatalf("a bot reading no board answered %s (%v), want the first land offered, %s", CandidateLabel(&d, blind), err, CandidateLabel(&d, island))
	}
	var gg state.ObjID
	for _, id := range e.G.Zone(state.ZHand, d.Player) {
		if cardName(e, id) == nGiantGrowth {
			gg = id
		}
	}
	cd := d
	cd.Options = append(append([]decision.Option(nil), d.Options...), decision.Option{
		Index: len(d.Options), Kind: "cast", Obj: gg, Player: d.Player, Label: "Cast " + nGiantGrowth})
	cast := decision.Intent{Seq: d.Seq, Player: d.Player, Choices: []int{len(d.Options)}}
	for _, hold := range []bool{false, true} {
		in, err := tapSeat(t, true, hold, true, "").decidePrior(ctx, searchseat.Env{Engine: e, Board: b}, cd, cast, false)
		if err != nil || !reflect.DeepEqual(in, forest) {
			t.Fatalf("the prior-only seat, holdupkeep %v, its bot casting Giant Growth: played %s (%v), want %s", hold, CandidateLabel(&cd, in), err, CandidateLabel(&d, forest))
		}
	}
	for _, hold := range []bool{false, true} {
		diags := watchDiags(t)
		in, err := tapSeat(t, true, hold, false, "").DecideSearch(ctx, searchseat.Env{Engine: e, Board: b}, d)
		if err != nil {
			t.Fatal(err)
		}
		var st Stats
		for _, dg := range *diags {
			st.Add(dg.Stats)
		}
		t.Logf("DecideSearch, holdupkeep %v: played %s (land first %d, tricks tap %d, tricks bot %d)", hold, CandidateLabel(&d, in), st.LandFirst, st.TricksTap, st.TricksBot)
		if !reflect.DeepEqual(in, forest) || st.LandFirst != 1 || st.Searched != 0 || st.TricksBot != 0 {
			t.Fatalf("DecideSearch, holdupkeep %v: played %s (land first %d, searched %d, tricks bot %d, tricks tap %d), want %s unsearched",
				hold, CandidateLabel(&d, in), st.LandFirst, st.Searched, st.TricksBot, st.TricksTap, CandidateLabel(&d, forest))
		}
	}
	if err := e.Submit(forest); err != nil {
		t.Fatal(err)
	}
	if !lfInPlay(e.G, 0, "Forest") {
		t.Fatal("no Forest in play after the land drop")
	}
}
