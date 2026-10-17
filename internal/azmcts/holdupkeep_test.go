package azmcts

import (
	"context"
	"math"
	"reflect"
	"testing"

	"github.com/adams-shaun/gorge/botpolicy"
	"github.com/adams-shaun/gorge/cards"
	"github.com/adams-shaun/gorge/decision"
	"github.com/adams-shaun/gorge/internal/searchprobe"
	"github.com/adams-shaun/gorge/rules"
	"github.com/adams-shaun/gorge/seat"
	"github.com/adams-shaun/gorge/state"
	"github.com/adams-shaun/gorge/view"
)

// The priority windows holdWindows finds, each with an instant castable from
// hand: seat 0's, and one of seat 1's.
const (
	winOwnUpkeep      = iota // seat 0's own upkeep, empty stack: held
	winOwnDraw               // seat 0's own draw step, empty stack: held
	winOwnMain               // seat 0's own first main phase, empty stack
	winOwnUpkeepStack        // seat 0's own upkeep with its own instant on the stack
	winOppUpkeep             // seat 1's upkeep, empty stack: seat 0 not active
	winOppOwnUpkeep          // seat 1's own upkeep, empty stack, seat 1 to act
	numHoldWindows
)

var holdWindowNames = [numHoldWindows]string{"own upkeep", "own draw", "own main", "own upkeep, stack", "opponent's upkeep", "seat 1's own upkeep"}

// holdWindows plays a mirror of Islands and two one-mana draw instants (Test
// Insight, Test Glimpse) with the plain bot and returns an engine at each
// window (holdWindowNames), seat 0 to act with an instant castable through a
// payment action (the plans built). Both bots hold every cast (a cast
// profile whose threshold no score reaches), so they keep instants in hand
// and lands untapped into the other's turn too.
func holdWindows(t *testing.T) [numHoldWindows]*rules.Engine {
	t.Helper()
	insight := payWindowCard(t, "Name:Test Insight\nManaCost:U\nTypes:Instant\nA:SP$ Draw | SpellDescription$ Draw a card.\nOracle:Draw a card.\n")
	glimpse := payWindowCard(t, "Name:Test Glimpse\nManaCost:U\nTypes:Instant\nA:SP$ Draw | SpellDescription$ Draw a card.\nOracle:Draw a card.\n")
	island := payWindowCard(t, "Name:Island\nTypes:Basic Land Island\nOracle:x\n")
	var deck []*cards.Card
	for i := 0; i < 18; i++ {
		deck = append(deck, island)
	}
	for i := 0; i < 11; i++ {
		deck = append(deck, insight, glimpse)
	}
	var out [numHoldWindows]*rules.Engine
	found := 0
	keep := func(w int, c *rules.Engine) {
		if out[w] == nil && c != nil {
			out[w] = c
			found++
		}
	}
	for seed := uint64(1); seed < 40 && found < numHoldWindows; seed++ {
		e := rules.New(rules.Config{Seed: seed, Names: []string{"a", "b"}, Decks: [][]*cards.Card{deck, deck}})
		e.Advance()
		rngs := searchprobe.BotRandoms(seed, 2)
		boards := [2]botpolicy.Board{botpolicy.NewBoard(2), botpolicy.NewBoard(2)}
		for p := range boards {
			boards[p].Cast = botpolicy.DefaultCastWeights
			boards[p].Cast.CastThreshold = math.MaxInt32
		}
		for steps := 0; steps < 3000 && !e.G.Over && found < numHoldWindows; steps++ {
			d := e.Pending()
			if w := holdWindowOf(e, d); w >= 0 && out[w] == nil {
				keep(w, castableClone(e))
			}
			if holdWindowOf(e, d) == winOwnUpkeep && out[winOwnUpkeepStack] == nil {
				keep(winOwnUpkeepStack, stackedUpkeep(e))
			}
			in := botpolicy.Decide(botpolicy.BoardFromGameInto(e.G, e, d.Player, &boards[d.Player]), d, rngs[d.Player])
			if err := e.Submit(in); err != nil {
				t.Fatal(err)
			}
		}
	}
	for w, e := range out {
		if e == nil {
			t.Fatalf("no %s window reached", holdWindowNames[w])
		}
	}
	return out
}

// holdWindowOf is the window d is (an empty-stack priority), -1 for none.
func holdWindowOf(e *rules.Engine, d *decision.Decision) int {
	g := e.G
	if d == nil || d.Kind != decision.KPriority || len(g.Stack) != 0 {
		return -1
	}
	if d.Player == 1 {
		if g.Active == 1 && g.Step == state.StepUpkeep {
			return winOppOwnUpkeep
		}
		return -1
	}
	switch {
	case g.Active == 0 && g.Step == state.StepUpkeep:
		return winOwnUpkeep
	case g.Active == 0 && g.Step == state.StepDraw:
		return winOwnDraw
	case g.Active == 0 && g.Step == state.StepMain1:
		return winOwnMain
	case g.Active != 0 && g.Step == state.StepUpkeep:
		return winOppUpkeep
	}
	return -1
}

// castableClone is a clone of e with its pending decision's payment plans
// built, nil when no payment action casts anything there.
func castableClone(e *rules.Engine) *rules.Engine {
	c := e.Clone()
	for _, a := range c.EnsurePaymentActions() {
		if len(a.Plans) > 0 {
			return c
		}
	}
	return nil
}

// stackedUpkeep is a clone of e (at seat 0's own empty-stack upkeep) after
// seat 0 casts an instant through its first payment action: seat 0 holds
// priority again over its own spell with another instant castable, nil when
// it cannot.
func stackedUpkeep(e *rules.Engine) *rules.Engine {
	c := e.Clone()
	d := c.Pending()
	for _, a := range c.EnsurePaymentActions() {
		if len(a.Plans) == 0 {
			continue
		}
		in := decision.Intent{Seq: d.Seq, Player: d.Player, Payment: &decision.PaymentSelection{ActionID: a.ID, Plan: decision.ClonePaymentPlan(a.Plans[0])}}
		if err := c.Submit(in); err != nil {
			return nil
		}
		nd := c.Pending()
		if nd == nil || nd.Player != 0 || nd.Kind != decision.KPriority || len(c.G.Stack) != 1 || c.G.Step != state.StepUpkeep {
			return nil
		}
		return castableClone(c)
	}
	return nil
}

// passOf is the pass answer at d.
func passOf(t *testing.T, d *decision.Decision) decision.Intent {
	t.Helper()
	for _, o := range d.Options {
		if o.Kind == "pass" {
			return decision.Intent{Seq: d.Seq, Player: d.Player, Choices: []int{o.Index}}
		}
	}
	t.Fatal("no pass option")
	return decision.Intent{}
}

// castsSpell reports whether candidate intent in casts a spell at d: a
// payment cast, or a legacy "cast" option.
func castsSpell(d *decision.Decision, in decision.Intent) bool {
	if in.Payment != nil {
		return true
	}
	for _, c := range in.Choices {
		if c >= 0 && c < len(d.Options) && d.Options[c].Kind == "cast" {
			return true
		}
	}
	return false
}

func countCasts(d *decision.Decision, ins []decision.Intent) int {
	n := 0
	for _, in := range ins {
		if castsSpell(d, in) {
			n++
		}
	}
	return n
}

func intentsOf(cands []cand) []decision.Intent {
	out := make([]decision.Intent, len(cands))
	for i, c := range cands {
		out[i] = c.in
	}
	return out
}

// heldKinds is AllKinds under the HoldUpkeep rule (exempt '|'-separated).
func heldKinds(exempt string) Kinds {
	k := AllKinds()
	k.hold = newHoldUpkeep(exempt)
	return k
}

// withoutCasts is the keys of cands that cast no spell at d, in order.
func withoutCasts(d *decision.Decision, cands []cand) []Key {
	var out []Key
	for _, c := range cands {
		if !castsSpell(d, c.in) {
			out = append(out, c.key)
		}
	}
	return out
}

// checkHeld enumerates d (autoPay: the payment vocabulary, else the manual
// one) with and without the rule: without it a spell is cast; with it the
// candidates are the same minus every cast -- or, fewer than two left, the
// decision is skipped (SkipFewCandidates).
func checkHeld(t *testing.T, name string, e *rules.Engine, d *decision.Decision, bot decision.Intent, autoPay bool) {
	t.Helper()
	all, kind, _, ok, _ := enumerateCut(searchprobe.NewCollector(d.Player), e, d, bot, AllKinds(), BenchCandidateLimit, autoPay)
	if !ok || kind != "priority" {
		t.Fatalf("%s: without the rule the window is not searched (%v %q)", name, ok, kind)
	}
	if countCasts(d, intentsOf(all)) == 0 {
		t.Fatalf("%s: without the rule no candidate casts a spell: %v", name, keysOf(all))
	}
	k := heldKinds("")
	held, _, why, ok, _ := enumerateCut(searchprobe.NewCollector(d.Player), e, d, bot, k, BenchCandidateLimit, autoPay)
	want := withoutCasts(d, all)
	if k.hold.dropped == 0 {
		t.Fatalf("%s: the rule counted no drop", name)
	}
	if len(want) < 2 {
		if ok || why != SkipFewCandidates {
			t.Fatalf("%s: with the rule %v (%v), want skipped for few candidates (left %v)", name, keysOf(held), why, want)
		}
		return
	}
	if !ok || !reflect.DeepEqual(keysOf(held), want) {
		t.Fatalf("%s: with the rule %v (ok %v), want %v", name, keysOf(held), ok, want)
	}
}

// checkUnheld enumerates d with and without the rule and wants the same
// candidates, a spell cast among them.
func checkUnheld(t *testing.T, name string, e *rules.Engine, d *decision.Decision, bot decision.Intent, autoPay bool) {
	t.Helper()
	all, _, _, ok, _ := enumerateCut(searchprobe.NewCollector(d.Player), e, d, bot, AllKinds(), BenchCandidateLimit, autoPay)
	if !ok || countCasts(d, intentsOf(all)) == 0 {
		t.Fatalf("%s: without the rule %v (ok %v), want a searched window with a cast", name, keysOf(all), ok)
	}
	k := heldKinds("")
	held, _, _, ok, _ := enumerateCut(searchprobe.NewCollector(d.Player), e, d, bot, k, BenchCandidateLimit, autoPay)
	if !ok || !reflect.DeepEqual(keysOf(held), keysOf(all)) || k.hold.dropped != 0 {
		t.Fatalf("%s: with the rule %v (ok %v, dropped %d), want %v unchanged", name, keysOf(held), ok, k.hold.dropped, keysOf(all))
	}
}

// At the active player's own empty-stack upkeep and draw, the rule drops
// every payment cast from the candidates; at its own main phase, over a
// spell on the stack in its upkeep, and in the opponent's upkeep nothing
// changes.
func TestHoldUpkeepDropsOwnUpkeepAndDrawCasts(t *testing.T) {
	ws := holdWindows(t)
	for w, e := range ws {
		d := e.Pending()
		bot := passOf(t, d)
		name := holdWindowNames[w]
		switch w {
		case winOwnUpkeep, winOwnDraw, winOppOwnUpkeep:
			checkHeld(t, name, e, d, bot, true)
		default:
			checkUnheld(t, name, e, d, bot, true)
		}
	}
}

// With mana floating the manual vocabulary offers a legacy "cast" option in
// the upkeep too: the rule drops it, and only outside its window keeps it.
func TestHoldUpkeepDropsFloatingManaCasts(t *testing.T) {
	ws := holdWindows(t)
	for _, w := range []int{winOwnUpkeep, winOwnMain, winOppUpkeep} {
		e := ws[w].Clone()
		d := e.Pending()
		tapped := false
		for _, o := range d.Options {
			if o.Kind == "activate" {
				if err := e.Submit(decision.Intent{Seq: d.Seq, Player: d.Player, Choices: []int{o.Index}}); err != nil {
					t.Fatal(err)
				}
				tapped = true
				break
			}
		}
		if !tapped {
			t.Fatalf("%s: no mana to tap", holdWindowNames[w])
		}
		d = e.Pending()
		if holdWindowOf(e, d) != w {
			t.Fatalf("%s: tapping a land left the window", holdWindowNames[w])
		}
		name := holdWindowNames[w] + ", mana floating"
		if w == winOwnUpkeep {
			checkHeld(t, name, e, d, passOf(t, d), false)
			e.EnsurePaymentActions()
			checkHeld(t, name+", payment vocabulary", e, d, passOf(t, d), true)
			continue
		}
		checkUnheld(t, name, e, d, passOf(t, d), false)
	}
}

// Search with Options.HoldUpkeep at the own-upkeep root: no candidate casts
// (the root skipped when only pass is left, the bot's pass played), counted
// in Stats.HeldUpkeep; without the option, or with every instant exempt, the
// root is searched with its casts. The full root (Root.NoBot, a macro that
// casts) is held the same way.
func TestHoldUpkeepAtTheSearchRoot(t *testing.T) {
	ws := holdWindows(t)
	search := func(w int, hold bool, exempt string, root func(*Root)) Result {
		t.Helper()
		e := ws[w].Clone()
		d := e.Pending()
		obs := searchprobe.NewCollector(d.Player)
		src, err := newTestClairvoyant(e, obs)
		if err != nil {
			t.Fatal(err)
		}
		opts := DefaultOptions()
		opts.Sims, opts.AutoPayment, opts.HoldUpkeep, opts.HoldUpkeepExempt = 4, true, hold, exempt
		r := Root{Engine: e, Decision: d, Bot: passOf(t, d), Observer: obs}
		if root != nil {
			root(&r)
		}
		res, err := Search(context.Background(), r, src, nil, opts)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	for _, w := range []int{winOwnUpkeep, winOwnDraw} {
		name := holdWindowNames[w]
		d := ws[w].Pending()
		off := search(w, false, "", nil)
		if off.Stats.Searched != 1 || countCasts(d, off.Candidates) == 0 || off.Stats.HeldUpkeep != 0 {
			t.Fatalf("%s, off: searched %d, %d casts among %d candidates, held %d", name, off.Stats.Searched, countCasts(d, off.Candidates), len(off.Candidates), off.Stats.HeldUpkeep)
		}
		on := search(w, true, "", nil)
		if on.Stats.HeldUpkeep != 1 || countCasts(d, on.Candidates) != 0 {
			t.Fatalf("%s, on: held %d, %d casts among %v", name, on.Stats.HeldUpkeep, countCasts(d, on.Candidates), on.Labels)
		}
		if len(on.Candidates) == 0 && (on.Stats.Skipped != 1 || !reflect.DeepEqual(on.Intent, passOf(t, d))) {
			t.Fatalf("%s, on: nothing searched, yet skipped %d and played %+v", name, on.Stats.Skipped, on.Intent)
		}
		ex := search(w, true, "Test Insight|Test Glimpse", nil)
		if ex.Stats.HeldUpkeep != 0 || !reflect.DeepEqual(ex.Keys, off.Keys) {
			t.Fatalf("%s, every instant exempt: held %d, keys %v, want %v", name, ex.Stats.HeldUpkeep, ex.Keys, off.Keys)
		}
		// The full root: no bot candidate, plus a macro casting the first
		// payment action's card.
		var obj state.ObjID
		for _, a := range ws[w].Clone().EnsurePaymentActions() {
			if len(a.Plans) > 0 {
				obj = a.Cast.Object
				break
			}
		}
		full := func(r *Root) {
			r.NoBot = true
			r.Macros = []Macro{{Key: MacroKeyPrefix + "insight", Label: "macro cast", Steps: []MacroStep{{Player: 0, Kind: decision.KPriority, PayCast: obj}}}}
		}
		fullOff := search(w, false, "", full)
		if fullOff.Stats.Searched != 1 || countCasts(d, fullOff.Candidates) == 0 || !containsKey(fullOff.Keys, MacroKeyPrefix+"insight") {
			t.Fatalf("%s, full root off: searched %d, keys %v", name, fullOff.Stats.Searched, fullOff.Keys)
		}
		fullOn := search(w, true, "", full)
		if fullOn.Stats.HeldUpkeep != 1 || countCasts(d, fullOn.Candidates) != 0 || containsKey(fullOn.Keys, MacroKeyPrefix+"insight") {
			t.Fatalf("%s, full root on: held %d, keys %v", name, fullOn.Stats.HeldUpkeep, fullOn.Keys)
		}
	}
	for _, w := range []int{winOwnMain, winOwnUpkeepStack, winOppUpkeep} {
		off, on := search(w, false, "", nil), search(w, true, "", nil)
		if on.Stats.HeldUpkeep != 0 || !reflect.DeepEqual(on.Keys, off.Keys) || !reflect.DeepEqual(on.Visits, off.Visits) {
			t.Fatalf("%s: the rule changed the search: keys %v visits %v, want %v %v", holdWindowNames[w], on.Keys, on.Visits, off.Keys, off.Visits)
		}
	}
	bad := DefaultOptions()
	bad.HoldUpkeepExempt = "Opt"
	if err := bad.Validate(nil); err == nil {
		t.Fatal("an exemption list without HoldUpkeep validated")
	}
}

func containsKey(keys []Key, k Key) bool {
	for _, x := range keys {
		if string(x) == string(k) {
			return true
		}
	}
	return false
}

// holdWalkPoint is one in-walk point: whose it was, whether the rule's
// window held there, and how many of its candidates cast a spell.
type holdWalkPoint struct {
	opp, window bool
	casts       int
	keys        []Key
}

// holdWalk walks one clairvoyant world from seat 0's opponent's-upkeep
// window, always playing candidate 0, and returns the points it reached.
// hold is the rule (nil off); opp puts the opponent in the tree mirroring
// the seat under mirror (OpponentMirror: auto-pay, and the rule when hold).
func holdWalk(t *testing.T, e0 *rules.Engine, hold *holdUpkeep, opp, mirror bool) []holdWalkPoint {
	t.Helper()
	e := e0.Clone()
	d := e.Pending()
	bot, err := seat.NewBot(7).EnableAutoPayMana().Decide(context.Background(), view.Project(e.G, e, d.Player, d), *d)
	if err != nil {
		t.Fatal(err)
	}
	kinds := AllKinds()
	kinds.hold = hold
	obs := searchprobe.NewCollector(d.Player)
	cands, _, _, ok, _ := enumerateCut(obs, e, d, bot, kinds, DefaultOptions().Limit, true)
	if !ok {
		t.Fatal("the walk's root is not searched")
	}
	var st Stats
	cfg := &walkConfig{
		kinds: kinds, limit: DefaultOptions().Limit, maxSteps: 2000, envSeed: 0x686f6c64, actor: d.Player, autoPayment: true,
		rootRefs: obs.Introduced(), root: &Point{Keys: keysOf(cands), Prior: uniform(len(cands))}, rootCands: cands, rootDec: d, stats: &st,
	}
	if opp {
		cfg.oppNodes = true
		cfg.oppForks = make([]*searchprobe.Forker, len(e.G.Players))
		for p := range cfg.oppForks {
			if pl := state.PlayerID(p); pl != d.Player {
				cfg.oppForks[p] = searchprobe.NewCollector(pl).Forker()
			}
		}
		if mirror {
			cfg.oppAutoPay, cfg.oppHold = true, hold != nil
		}
	}
	src, err := newTestClairvoyant(e, obs)
	if err != nil {
		t.Fatal(err)
	}
	w, err := src.World(0)
	if err != nil {
		t.Fatal(err)
	}
	env, err := newEngineEnv(w, cfg)
	if err != nil {
		t.Fatal(err)
	}
	probe := newHoldUpkeep("")
	var out []holdWalkPoint
	pt := env.Root()
	for i := 0; i < 400 && pt != nil; i++ {
		if pt, err = env.Play(pt.Keys[0]); err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
		if pt == nil {
			break
		}
		out = append(out, holdWalkPoint{opp: pt.Opp, window: probe.on(env.e, env.cur), casts: countCasts(env.cur, intentsOf(env.cands)), keys: pt.Keys})
	}
	return out
}

// In the walk the seat's own upkeep and draw points cast nothing under the
// rule (and are points with casts without it); with the opponent in the
// tree mirroring the seat, the opponent's own upkeep and draw are held the
// same way, and without the mirror its points are what they were.
func TestHoldUpkeepInTheWalk(t *testing.T) {
	root := holdWindows(t)[winOppUpkeep]
	count := func(pts []holdWalkPoint, opp bool) (points, heldCasts int) {
		for _, p := range pts {
			if p.opp != opp {
				continue
			}
			points++
			if p.window && p.casts > 0 {
				heldCasts++
			}
		}
		return points, heldCasts
	}
	for _, tc := range []struct {
		name        string
		opp, mirror bool
	}{{"seat only", false, false}, {"opponent mirrored", true, true}, {"opponent unmirrored", true, false}} {
		off := holdWalk(t, root, nil, tc.opp, tc.mirror)
		on := holdWalk(t, root, newHoldUpkeep(""), tc.opp, tc.mirror)
		offPts, offCasts := count(off, false)
		onPts, onCasts := count(on, false)
		if offCasts == 0 {
			t.Fatalf("%s: off, no own upkeep or draw point with a cast among %d points", tc.name, offPts)
		}
		if onCasts != 0 || onPts == 0 {
			t.Fatalf("%s: on, %d own upkeep or draw points with a cast among %d points", tc.name, onCasts, onPts)
		}
		offOpp, offOppCasts := count(off, true)
		onOpp, onOppCasts := count(on, true)
		t.Logf("%s: seat points %d off (%d own upkeep/draw with a cast), %d on (%d); opponent points %d off (%d), %d on (%d)",
			tc.name, offPts, offCasts, onPts, onCasts, offOpp, offOppCasts, onOpp, onOppCasts)
		if !tc.opp {
			continue
		}
		if tc.mirror {
			if offOppCasts == 0 || onOppCasts != 0 || onOpp == 0 {
				t.Fatalf("%s: the opponent's own upkeep or draw points with a cast: %d of %d off, %d of %d on", tc.name, offOppCasts, offOpp, onOppCasts, onOpp)
			}
			continue
		}
		var offKeys, onKeys [][]Key
		for _, p := range off {
			if p.opp {
				offKeys = append(offKeys, p.keys)
			}
		}
		for _, p := range on {
			if p.opp {
				onKeys = append(onKeys, p.keys)
			}
		}
		if offOpp == 0 || !reflect.DeepEqual(onKeys, offKeys) {
			t.Fatalf("%s: the rule changed the opponent's %d points (%d with it)", tc.name, offOpp, onOpp)
		}
	}
}

// An opponent's point (OpponentNodes) at its own empty-stack upkeep holds
// its casts only under oppHold (OpponentMirror with HoldUpkeep): the rule
// on the walk's kinds alone leaves its candidates as they were, in either
// vocabulary.
func TestHoldUpkeepAtOpponentPointsOnlyMirrored(t *testing.T) {
	ws := holdWindows(t)
	for _, autoPay := range []bool{false, true} {
		e := ws[winOppOwnUpkeep].Clone()
		d := e.Pending()
		if !autoPay {
			// The manual vocabulary offers a cast once mana floats.
			for _, o := range d.Options {
				if o.Kind == "activate" {
					if err := e.Submit(decision.Intent{Seq: d.Seq, Player: d.Player, Choices: []int{o.Index}}); err != nil {
						t.Fatal(err)
					}
					break
				}
			}
			if d = e.Pending(); holdWindowOf(e, d) != winOppOwnUpkeep {
				t.Fatal("tapping a land left seat 1's upkeep window")
			}
		}
		point := func(oppHold bool) (*Point, []cand) {
			var st Stats
			kinds := AllKinds()
			kinds.hold = newHoldUpkeep("")
			cfg := &walkConfig{kinds: kinds, limit: BenchCandidateLimit, actor: 0, stats: &st, oppNodes: true,
				oppForks: []*searchprobe.Forker{nil, searchprobe.NewCollector(1).Forker()}, oppAutoPay: autoPay, oppHold: oppHold}
			env := &engineEnv{e: e, cfg: cfg}
			pt := env.oppPoint(d, passOf(t, d))
			return pt, env.cands
		}
		off, offCands := point(false)
		if off == nil || countCasts(d, intentsOf(offCands)) == 0 {
			t.Fatalf("autopay %v, oppHold off: point %v, want one with a cast", autoPay, off)
		}
		on, onCands := point(true)
		if on != nil && countCasts(d, intentsOf(onCands)) != 0 {
			t.Fatalf("autopay %v, oppHold on: %d casts among %v", autoPay, countCasts(d, intentsOf(onCands)), on.Keys)
		}
	}
}
