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
	"github.com/adams-shaun/gorge/internal/searchseat"
	"github.com/adams-shaun/gorge/rules"
	"github.com/adams-shaun/gorge/seat"
	"github.com/adams-shaun/gorge/state"
	"github.com/adams-shaun/gorge/view"
)

// The priority windows holdWindows finds, each with an instant castable from
// hand through a payment action and an empty stack (but winOwnUpkeepStack):
// seat 0's, and one of seat 1's. The rule is on at the first four
// (heldWindows); every one of those but winOwnUpkeepSpent also offers a Test
// Beacon's ability, so the rule leaves at least pass and the ability there.
const (
	winOwnUpkeep      = iota // seat 0's own upkeep: held
	winOwnDraw               // seat 0's own draw step: held
	winOwnUpkeepSpent        // seat 0's own upkeep once its Beacons' abilities resolved: held, pass alone left
	winOppOwnUpkeep          // seat 1's own upkeep, seat 1 to act: held
	winOwnMain               // seat 0's own first main phase
	winOwnCombat             // seat 0's own beginning of combat
	winOwnMain2              // seat 0's own second main phase
	winOwnEnd                // seat 0's own end step
	winOwnUpkeepStack        // seat 0's own upkeep with its own instant on the stack
	winOppUpkeep             // seat 1's upkeep: seat 0 not active
	winOppEnd                // seat 1's end step: seat 0 not active, its own upkeep next
	numHoldWindows
)

var holdWindowNames = [numHoldWindows]string{"own upkeep", "own draw", "own upkeep, abilities spent", "seat 1's own upkeep",
	"own main", "own beginning of combat", "own second main", "own end step", "own upkeep, stack", "opponent's upkeep", "opponent's end step"}

// heldWindows are the windows the rule is on at, unheldWindows the others.
var (
	heldWindows   = []int{winOwnUpkeep, winOwnDraw, winOwnUpkeepSpent, winOppOwnUpkeep}
	unheldWindows = []int{winOwnMain, winOwnCombat, winOwnMain2, winOwnEnd, winOwnUpkeepStack, winOppUpkeep, winOppEnd}
)

// keepsAbility reports whether held window w offers the Beacon's ability.
func keepsAbility(w int) bool { return w == winOwnUpkeep || w == winOwnDraw || w == winOppOwnUpkeep }

// holdDeck is the mirror holdWindows plays: Islands, two one-mana draw
// instants (Test Insight, Test Glimpse) and Test Beacon, a land with an
// instant-speed non-mana ability ({T}: you gain 1 life; Food-like, but
// repeatable and free of mana, so it is offered with no mana floating, where
// the engine prices every offer).
func holdDeck(t *testing.T) []*cards.Card {
	t.Helper()
	insight := payWindowCard(t, "Name:Test Insight\nManaCost:U\nTypes:Instant\nA:SP$ Draw | SpellDescription$ Draw a card.\nOracle:Draw a card.\n")
	glimpse := payWindowCard(t, "Name:Test Glimpse\nManaCost:U\nTypes:Instant\nA:SP$ Draw | SpellDescription$ Draw a card.\nOracle:Draw a card.\n")
	island := payWindowCard(t, "Name:Island\nTypes:Basic Land Island\nOracle:x\n")
	beacon := payWindowCard(t, "Name:Test Beacon\nTypes:Land\nA:AB$ GainLife | Cost$ T | LifeAmount$ 1 | SpellDescription$ You gain 1 life.\nOracle:{T}: You gain 1 life.\n")
	var deck []*cards.Card
	for i := 0; i < 18; i++ {
		deck = append(deck, island)
	}
	for i := 0; i < 11; i++ {
		deck = append(deck, insight, glimpse)
	}
	for i := 0; i < 3; i++ {
		deck = append(deck, beacon)
	}
	return deck
}

// playHolding plays mirrors of deck, seed after seed, with the plain bot
// under a cast profile that holds every cast (a threshold no score reaches),
// so both seats keep their instants in hand and their lands untapped into
// the other's turn too -- except that a Test Beacon is played whenever one
// can be (the bot's land ranking prefers a basic Island). visit sees every
// pending decision before it is answered and returns false to stop.
func playHolding(t *testing.T, deck []*cards.Card, visit func(e *rules.Engine, d *decision.Decision) bool) {
	t.Helper()
	for seed := uint64(1); seed < 60; seed++ {
		e := rules.New(rules.Config{Seed: seed, Names: []string{"a", "b"}, Decks: [][]*cards.Card{deck, deck}})
		e.Advance()
		rngs := searchprobe.BotRandoms(seed, 2)
		boards := [2]botpolicy.Board{botpolicy.NewBoard(2), botpolicy.NewBoard(2)}
		for p := range boards {
			boards[p].Cast = botpolicy.DefaultCastWeights
			boards[p].Cast.CastThreshold = math.MaxInt32
		}
		for steps := 0; steps < 3000 && !e.G.Over; steps++ {
			d := e.Pending()
			if !visit(e, d) {
				return
			}
			in := botpolicy.Decide(botpolicy.BoardFromGameInto(e.G, e, d.Player, &boards[d.Player]), d, rngs[d.Player])
			for _, o := range d.Options {
				if o.Kind == "play_land" && cardName(e, o.Obj) == "Test Beacon" {
					in = decision.Intent{Seq: d.Seq, Player: d.Player, Choices: []int{o.Index}}
					break
				}
			}
			if err := e.Submit(in); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// holdWindows plays holdDeck's mirror (playHolding) and returns an engine at
// each window (holdWindowNames), the payment plans built.
func holdWindows(t *testing.T) [numHoldWindows]*rules.Engine {
	t.Helper()
	var out [numHoldWindows]*rules.Engine
	found := 0
	keep := func(w int, c *rules.Engine) {
		if out[w] == nil && c != nil {
			out[w] = c
			found++
		}
	}
	playHolding(t, holdDeck(t), func(e *rules.Engine, d *decision.Decision) bool {
		w := holdWindowOf(e, d)
		if w >= 0 && (!keepsAbility(w) || offersAbility(d)) && (w != winOppUpkeep || controls(e, 0, "Test Beacon") && controls(e, 1, "Test Beacon")) {
			keep(w, castableClone(e))
		}
		if w == winOwnUpkeep {
			keep(winOwnUpkeepStack, stackedUpkeep(e))
			keep(winOwnUpkeepSpent, spentUpkeep(e))
		}
		return found < numHoldWindows
	})
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
	if g.Active != 0 {
		switch g.Step {
		case state.StepUpkeep:
			return winOppUpkeep
		case state.StepEnd:
			return winOppEnd
		}
		return -1
	}
	switch g.Step {
	case state.StepUpkeep:
		return winOwnUpkeep
	case state.StepDraw:
		return winOwnDraw
	case state.StepMain1:
		return winOwnMain
	case state.StepBeginCombat:
		return winOwnCombat
	case state.StepMain2:
		return winOwnMain2
	case state.StepEnd:
		return winOwnEnd
	}
	return -1
}

func cardName(e *rules.Engine, id state.ObjID) string {
	if o := e.G.Obj(id); o != nil && o.Card != nil && len(o.Card.Faces) > 0 && o.Card.Faces[0] != nil {
		return o.Card.Faces[0].Name
	}
	return ""
}

// controls reports whether seat p controls a permanent named name.
func controls(e *rules.Engine, p state.PlayerID, name string) bool {
	for _, id := range e.G.Zone(state.ZBattlefield, p) {
		if o := e.G.Obj(id); o != nil && o.Controller == p && cardName(e, id) == name {
			return true
		}
	}
	return false
}

// offersAbility reports whether d offers an "ability" option.
func offersAbility(d *decision.Decision) bool {
	for _, o := range d.Options {
		if o.Kind == "ability" {
			return true
		}
	}
	return false
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

// spentUpkeep is a clone of e (at seat 0's own empty-stack upkeep, a Beacon's
// ability offered) after seat 0 activates every ability offered there and
// both seats pass until each resolves: seat 0's own upkeep again with an
// empty stack, an instant still castable and no ability left, nil when that
// is not where it ends.
func spentUpkeep(e *rules.Engine) *rules.Engine {
	if !offersAbility(e.Pending()) {
		return nil
	}
	c := e.Clone()
	for i := 0; i < 40; i++ {
		d := c.Pending()
		if d == nil || d.Kind != decision.KPriority || c.G.Active != 0 || c.G.Step != state.StepUpkeep {
			return nil
		}
		in, ok := passAt(d)
		if len(c.G.Stack) == 0 {
			if d.Player != 0 {
				return nil
			}
			ok = false
			for _, o := range d.Options {
				if o.Kind == "ability" {
					in, ok = decision.Intent{Seq: d.Seq, Player: d.Player, Choices: []int{o.Index}}, true
					break
				}
			}
			if !ok {
				return castableClone(c)
			}
		}
		if !ok || c.Submit(in) != nil {
			return nil
		}
	}
	return nil
}

// passAt is the pass answer at d, false when d offers none.
func passAt(d *decision.Decision) (decision.Intent, bool) {
	for _, o := range d.Options {
		if o.Kind == "pass" {
			return decision.Intent{Seq: d.Seq, Player: d.Player, Choices: []int{o.Index}}, true
		}
	}
	return decision.Intent{}, false
}

// passOf is the pass answer at d.
func passOf(t *testing.T, d *decision.Decision) decision.Intent {
	t.Helper()
	in, ok := passAt(d)
	if !ok {
		t.Fatal("no pass option")
	}
	return in
}

// payCastAt is the first payment cast at engine e's pending decision d.
func payCastAt(t *testing.T, e *rules.Engine, d *decision.Decision) decision.Intent {
	t.Helper()
	for _, a := range e.EnsurePaymentActions() {
		if len(a.Plans) > 0 {
			return decision.Intent{Seq: d.Seq, Player: d.Player, Payment: &decision.PaymentSelection{ActionID: a.ID, Plan: decision.ClonePaymentPlan(a.Plans[0])}}
		}
	}
	t.Fatal("no payment cast")
	return decision.Intent{}
}

// tapIsland taps one of the acting seat's Islands for mana at e's pending
// priority decision; the pending decision after it.
func tapIsland(t *testing.T, e *rules.Engine) *decision.Decision {
	t.Helper()
	d := e.Pending()
	for _, o := range d.Options {
		if o.Kind == "activate" && cardName(e, o.Obj) == "Island" {
			if err := e.Submit(decision.Intent{Seq: d.Seq, Player: d.Player, Choices: []int{o.Index}}); err != nil {
				t.Fatal(err)
			}
			return e.Pending()
		}
	}
	t.Fatal("no Island to tap")
	return nil
}

// testSpecialCast is the test's own list of the "cast"-kind special actions
// (holdupkeep.go's specialCasts), so a mutation of that list cannot hide in
// the oracle.
func testSpecialCast(mode string) bool {
	return mode == "foretell" || mode == "suspend" || mode == "plot"
}

// castsSpell reports whether candidate intent in casts a spell at d: a
// payment cast, or a legacy "cast" option that is not a special action.
func castsSpell(d *decision.Decision, in decision.Intent) bool {
	if in.Payment != nil {
		return true
	}
	for _, c := range in.Choices {
		if c >= 0 && c < len(d.Options) && d.Options[c].Kind == "cast" && !testSpecialCast(d.Options[c].Mode) {
			return true
		}
	}
	return false
}

// pickedKind is the Kind of the single option candidate in picks at d, ""
// for a payment or a multi-option intent.
func pickedKind(d *decision.Decision, in decision.Intent) string {
	if in.Payment != nil || len(in.Choices) != 1 || in.Choices[0] < 0 || in.Choices[0] >= len(d.Options) {
		return ""
	}
	return d.Options[in.Choices[0]].Kind
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

// resultWithoutCasts is withoutCasts over a Search's root candidates.
func resultWithoutCasts(d *decision.Decision, r Result) []Key {
	var out []Key
	for i, in := range r.Candidates {
		if !castsSpell(d, in) {
			out = append(out, r.Keys[i])
		}
	}
	return out
}

// checkHeld enumerates d (autoPay: the payment vocabulary, else the manual
// one) with and without the rule: without it a spell is cast; with it the
// candidates are exactly the same minus every cast. With abilities (the
// window offers a Beacon's) at least pass and an ability are left and the
// decision is searched; without, fewer than two are left and it is skipped
// (SkipFewCandidates).
func checkHeld(t *testing.T, name string, e *rules.Engine, d *decision.Decision, bot decision.Intent, autoPay, abilities bool) {
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
	if !abilities {
		if len(want) >= 2 || ok || why != SkipFewCandidates {
			t.Fatalf("%s: with the rule %v (%v), want skipped for few candidates (left %v)", name, keysOf(held), why, want)
		}
		return
	}
	ability := false
	for _, c := range all {
		ability = ability || pickedKind(d, c.in) == "ability"
	}
	if !ability || len(want) < 2 {
		t.Fatalf("%s: without the rule %v, want an ability candidate besides pass", name, keysOf(all))
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

// At the active player's own empty-stack upkeep and draw (seat 1's too), the
// rule drops every payment cast from the candidates and keeps pass and the
// Beacon's ability; at its own main phases, beginning of combat and end
// step, over a spell on the stack in its upkeep, and in the opponent's
// upkeep nothing changes.
func TestHoldUpkeepDropsOwnUpkeepAndDrawCasts(t *testing.T) {
	ws := holdWindows(t)
	for _, w := range heldWindows {
		e := ws[w]
		d := e.Pending()
		checkHeld(t, holdWindowNames[w], e, d, passOf(t, d), true, keepsAbility(w))
	}
	for _, w := range unheldWindows {
		e := ws[w]
		d := e.Pending()
		checkUnheld(t, holdWindowNames[w], e, d, passOf(t, d), true)
	}
}

// With mana floating the manual vocabulary offers a legacy "cast" option in
// the upkeep too: the rule drops it, and only outside its window keeps it.
func TestHoldUpkeepDropsFloatingManaCasts(t *testing.T) {
	ws := holdWindows(t)
	for _, w := range append([]int{winOwnUpkeep, winOwnUpkeepSpent}, unheldWindows...) {
		e := ws[w].Clone()
		d := tapIsland(t, e)
		want := w
		switch w {
		case winOwnUpkeepStack:
			want = -1 // not an empty-stack window
		case winOwnUpkeepSpent:
			want = winOwnUpkeep
		}
		if holdWindowOf(e, d) != want || (w == winOwnUpkeepStack && (len(e.G.Stack) != 1 || e.G.Step != state.StepUpkeep)) {
			t.Fatalf("%s: tapping a land left the window", holdWindowNames[w])
		}
		name := holdWindowNames[w] + ", mana floating"
		if w == winOwnUpkeep || w == winOwnUpkeepSpent {
			checkHeld(t, name, e, d, passOf(t, d), false, w == winOwnUpkeep)
			e.EnsurePaymentActions()
			checkHeld(t, name+", payment vocabulary", e, d, passOf(t, d), true, w == winOwnUpkeep)
			continue
		}
		checkUnheld(t, name, e, d, passOf(t, d), false)
	}
}

// holdSearch is Search at a clone of ws[w] with the bot's pass (or what root
// sets) and the rule as given; Sims 4, auto-payment.
func holdSearch(t *testing.T, ws [numHoldWindows]*rules.Engine, w int, hold bool, exempt string, root func(*Root), mods ...func(*Options)) Result {
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
	for _, m := range mods {
		m(&opts)
	}
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

// Search with Options.HoldUpkeep at the own-upkeep and draw roots: the
// candidates are exactly the unheld ones minus every cast (pass and the
// Beacon's ability), counted in Stats.HeldUpkeep; with the abilities spent
// the root is skipped and the bot's pass played; without the option, or with
// every instant exempt, the root is searched with its casts. The full root
// (Root.NoBot, a macro that casts) is held the same way, and a macro the
// root cannot offer is not counted. Every unaffected window searches as
// without the rule.
func TestHoldUpkeepAtTheSearchRoot(t *testing.T) {
	ws := holdWindows(t)
	search := func(w int, hold bool, exempt string, root func(*Root), mods ...func(*Options)) Result {
		t.Helper()
		return holdSearch(t, ws, w, hold, exempt, root, mods...)
	}
	for _, w := range []int{winOwnUpkeep, winOwnDraw} {
		name := holdWindowNames[w]
		d := ws[w].Pending()
		off := search(w, false, "", nil)
		if off.Stats.Searched != 1 || countCasts(d, off.Candidates) == 0 || off.Stats.HeldUpkeep != 0 {
			t.Fatalf("%s, off: searched %d, %d casts among %d candidates, held %d", name, off.Stats.Searched, countCasts(d, off.Candidates), len(off.Candidates), off.Stats.HeldUpkeep)
		}
		// With the rule the root is the whole unheld vocabulary minus its
		// casts, capped: a dropped cast frees its slot.
		all, _, _, _, _ := enumerateCut(searchprobe.NewCollector(d.Player), ws[w].Clone(), d, passOf(t, d), AllKinds(), BenchCandidateLimit, true)
		want := withoutCasts(d, all)
		if len(want) > DefaultOptions().Limit {
			want = want[:DefaultOptions().Limit]
		}
		on := search(w, true, "", nil)
		if on.Stats.HeldUpkeep != 1 || on.Stats.HeldUpkeepBot != 0 || on.Stats.Searched != 1 || len(want) < 2 || !reflect.DeepEqual(on.Keys, want) {
			t.Fatalf("%s, on: held %d, bot passed %d, searched %d, keys %v, want %v", name, on.Stats.HeldUpkeep, on.Stats.HeldUpkeepBot, on.Stats.Searched, on.Keys, want)
		}
		ex := search(w, true, "Test Insight|Test Glimpse", nil)
		if ex.Stats.HeldUpkeep != 0 || !reflect.DeepEqual(ex.Keys, off.Keys) {
			t.Fatalf("%s, every instant exempt: held %d, keys %v, want %v", name, ex.Stats.HeldUpkeep, ex.Keys, off.Keys)
		}
		// The full root: no bot candidate, plus a macro casting the first
		// payment action's card (the cap wide enough to hold it).
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
		wide := func(o *Options) { o.Limit = BenchCandidateLimit }
		fullOff := search(w, false, "", full, wide)
		if fullOff.Stats.Searched != 1 || countCasts(d, fullOff.Candidates) == 0 || !containsKey(fullOff.Keys, MacroKeyPrefix+"insight") {
			t.Fatalf("%s, full root off: searched %d, keys %v", name, fullOff.Stats.Searched, fullOff.Keys)
		}
		fullOn := search(w, true, "", full, wide)
		if fullOn.Stats.HeldUpkeep != 1 || countCasts(d, fullOn.Candidates) != 0 || containsKey(fullOn.Keys, MacroKeyPrefix+"insight") {
			t.Fatalf("%s, full root on: held %d, keys %v", name, fullOn.Stats.HeldUpkeep, fullOn.Keys)
		}
	}
	// A macro whose first step the root does not offer (a payment cast of a
	// land on the battlefield) is not offered, so not counted either: with
	// both instants exempt nothing else is held.
	var land state.ObjID
	for _, id := range ws[winOwnUpkeep].G.Zone(state.ZBattlefield, 0) {
		if cardName(ws[winOwnUpkeep], id) == "Island" {
			land = id
			break
		}
	}
	unoffered := func(r *Root) {
		r.NoBot = true
		r.Macros = []Macro{{Key: MacroKeyPrefix + "land", Label: "no such cast", Steps: []MacroStep{{Player: 0, Kind: decision.KPriority, PayCast: land}}}}
	}
	if un := search(winOwnUpkeep, true, "Test Insight|Test Glimpse", unoffered); land == 0 || un.Stats.HeldUpkeep != 0 || containsKey(un.Keys, MacroKeyPrefix+"land") {
		t.Fatalf("a macro the root cannot offer: land %d, held %d, keys %v", land, un.Stats.HeldUpkeep, un.Keys)
	}
	// The Beacons spent, only pass is left: the root is skipped and the
	// bot's pass played.
	d := ws[winOwnUpkeepSpent].Pending()
	if off := search(winOwnUpkeepSpent, false, "", nil); off.Stats.Searched != 1 || countCasts(d, off.Candidates) == 0 {
		t.Fatalf("abilities spent, off: searched %d, keys %v", off.Stats.Searched, off.Keys)
	}
	if on := search(winOwnUpkeepSpent, true, "", nil); on.Stats.HeldUpkeep != 1 || on.Stats.Skipped != 1 || len(on.Candidates) != 0 || !reflect.DeepEqual(on.Intent, passOf(t, d)) {
		t.Fatalf("abilities spent, on: held %d, skipped %d, candidates %v, played %+v", on.Stats.HeldUpkeep, on.Stats.Skipped, on.Labels, on.Intent)
	}
	for _, w := range unheldWindows {
		off, on := search(w, false, "", nil), search(w, true, "", nil)
		if off.Stats.Searched != 1 || on.Stats.HeldUpkeep != 0 || !reflect.DeepEqual(on.Keys, off.Keys) || !reflect.DeepEqual(on.Visits, off.Visits) {
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

// A bot answer that casts a held spell is outside the held candidates: the
// rule plays pass in its place instead of the cast, at the root (counted in
// Stats.HeldUpkeepBot; the payment bot's cast and, with mana floating, the
// manual bot's), at the prior-only seat, at the searching seat's in-walk
// decisions (Stats.HeldUpkeepWalk) and, under oppHold, at the opponent's
// (Stats.HeldUpkeepOpp). Without the rule, or with the card exempt, the bot's
// cast stands.
func TestHoldUpkeepPassesForAHeldBotCast(t *testing.T) {
	ws := holdWindows(t)
	botCast := func(r *Root) { r.Bot = payCastAt(t, r.Engine, r.Decision) }
	for _, w := range []int{winOwnUpkeep, winOwnDraw} {
		name := holdWindowNames[w]
		d := ws[w].Pending()
		off := holdSearch(t, ws, w, false, "", botCast)
		if off.Stats.Searched != 1 || off.Stats.HeldUpkeepBot != 0 || len(off.Candidates) == 0 || off.Candidates[0].Payment == nil {
			t.Fatalf("%s, off: searched %d, bot passed %d, candidates %v", name, off.Stats.Searched, off.Stats.HeldUpkeepBot, off.Labels)
		}
		on := holdSearch(t, ws, w, true, "", botCast)
		if on.Stats.HeldUpkeepBot != 1 || on.Stats.HeldUpkeep != 1 || on.Stats.Skipped != 1 || !reflect.DeepEqual(on.Intent, passOf(t, d)) {
			t.Fatalf("%s, on: bot passed %d, held %d, skipped %d, played %+v", name, on.Stats.HeldUpkeepBot, on.Stats.HeldUpkeep, on.Stats.Skipped, on.Intent)
		}
		ex := holdSearch(t, ws, w, true, "Test Insight|Test Glimpse", botCast)
		if ex.Stats.HeldUpkeepBot != 0 || ex.Stats.Searched != 1 || !reflect.DeepEqual(ex.Keys, off.Keys) {
			t.Fatalf("%s, every instant exempt: bot passed %d, searched %d, keys %v", name, ex.Stats.HeldUpkeepBot, ex.Stats.Searched, ex.Keys)
		}
	}
	// Outside the window the bot's cast is the root's candidate 0, as
	// without the rule.
	for _, w := range unheldWindows {
		off, on := holdSearch(t, ws, w, false, "", botCast), holdSearch(t, ws, w, true, "", botCast)
		if on.Stats.HeldUpkeepBot != 0 || on.Stats.Searched != 1 || !reflect.DeepEqual(on.Keys, off.Keys) || !reflect.DeepEqual(on.Visits, off.Visits) || !reflect.DeepEqual(on.Intent, off.Intent) {
			t.Fatalf("%s: the rule changed a bot cast's search: bot passed %d, keys %v visits %v, want %v %v", holdWindowNames[w], on.Stats.HeldUpkeepBot, on.Keys, on.Visits, off.Keys, off.Visits)
		}
	}
	// The manual vocabulary with mana floating: the plain bot's legacy cast.
	{
		e := ws[winOwnUpkeep].Clone()
		d := tapIsland(t, e)
		var cast decision.Intent
		for _, o := range d.Options {
			if o.Kind == "cast" {
				cast = decision.Intent{Seq: d.Seq, Player: d.Player, Choices: []int{o.Index}}
				break
			}
		}
		if cast.Choices == nil {
			t.Fatal("own upkeep, mana floating: no legacy cast")
		}
		manual := func(hold bool) Result {
			c := e.Clone()
			opts := DefaultOptions()
			opts.Sims, opts.HoldUpkeep = 4, hold
			return searchAt(t, c, c.Pending(), cast, nil, opts)
		}
		if off := manual(false); off.Stats.Searched != 1 || !castsSpell(d, off.Candidates[0]) || off.Stats.HeldUpkeepBot != 0 {
			t.Fatalf("manual, off: searched %d, candidates %v", off.Stats.Searched, off.Labels)
		}
		if on := manual(true); on.Stats.HeldUpkeepBot != 1 || on.Stats.Skipped != 1 || !reflect.DeepEqual(on.Intent, passOf(t, d)) {
			t.Fatalf("manual, on: bot passed %d, skipped %d, played %+v", on.Stats.HeldUpkeepBot, on.Stats.Skipped, on.Intent)
		}
	}
	// The prior-only seat plays Search's answer too.
	for _, hold := range []bool{false, true} {
		e := ws[winOwnUpkeep].Clone()
		d := e.Pending()
		cast := payCastAt(t, e, d)
		sc := DefaultSeatConfig()
		sc.PriorOnly, sc.Source = true, testSeatSource
		sc.Search.AutoPayment, sc.Search.HoldUpkeep = true, hold
		az, err := NewSeat(5, nil, sc)
		if err != nil {
			t.Fatal(err)
		}
		got, err := az.decidePrior(context.Background(), searchseat.Env{Engine: e}, *d, cast)
		if err != nil {
			t.Fatal(err)
		}
		want := cast
		if hold {
			want = passOf(t, d)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("prior-only seat, hold %v: played %+v, want %+v", hold, got, want)
		}
	}
	// The walk, from a held decision with mana floating whose plain bot
	// casts: the searching seat's, and the opponent's in the tree. Passed,
	// the walk moves on with the instants still in hand; otherwise the
	// decision is a point with the bot's cast among its candidates.
	for _, tc := range []struct {
		name          string
		w             int
		hold, oppHold bool
		passed        bool
	}{
		{"seat, off", winOwnUpkeep, false, false, false},
		{"seat, on", winOwnUpkeep, true, false, true},
		{"opponent, off", winOppOwnUpkeep, false, false, false},
		{"opponent, on, unmirrored", winOppOwnUpkeep, true, false, false},
		{"opponent, on, mirrored", winOppOwnUpkeep, true, true, true},
	} {
		e := ws[tc.w].Clone()
		d := tapIsland(t, e)
		var hold *holdUpkeep
		if tc.hold {
			hold = newHoldUpkeep("")
		}
		env, st := heldEnv(t, e, hold, tc.oppHold)
		hand := append([]state.ObjID(nil), env.e.G.Zone(state.ZHand, d.Player)...)
		pt, err := env.advance()
		if err != nil || pt == nil {
			t.Fatalf("%s: point %v, %v", tc.name, pt, err)
		}
		kept := true
		for _, id := range hand {
			if o := env.e.G.Obj(id); o == nil || o.Zone != state.ZHand {
				kept = false
			}
		}
		counted := st.HeldUpkeepWalk
		if tc.w == winOppOwnUpkeep {
			counted = st.HeldUpkeepOpp
		}
		atRoot := env.cur.Seq == d.Seq
		if tc.passed != (kept && !atRoot && counted > 0) || !tc.passed && (!atRoot || counted != 0 || countCasts(env.cur, intentsOf(env.cands)) == 0) {
			t.Fatalf("%s: hand kept %v, at the root %v, counted %d (walk %d, opponent %d), want passed %v", tc.name, kept, atRoot, counted, st.HeldUpkeepWalk, st.HeldUpkeepOpp, tc.passed)
		}
	}
}

// heldEnv is a walk env on engine e at its pending decision (a clone's: e
// is not played), seat 0 searching with the plain bot answering (no
// auto-payment), the rule hold (nil off) on its kinds, and seat 1's points
// in the tree, holding under oppHold; its stats.
func heldEnv(t *testing.T, e *rules.Engine, hold *holdUpkeep, oppHold bool) (*engineEnv, *Stats) {
	t.Helper()
	c := e.Clone()
	kinds := AllKinds()
	kinds.hold = hold
	st := &Stats{}
	cfg := &walkConfig{kinds: kinds, limit: DefaultOptions().Limit, maxSteps: 2000, envSeed: 0x686f6c64, actor: 0, rootDec: c.Pending(), stats: st,
		oppNodes: true, oppForks: []*searchprobe.Forker{nil, searchprobe.NewCollector(1).Forker()}, oppHold: oppHold}
	env, err := newEngineEnv(World{Engine: c, Observer: searchprobe.NewCollector(0)}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return env, st
}

// Search from a root the rule leaves alone, deep enough to reach a held
// decision in the walk: from the opponent's end step the seat's own next
// upkeep, where the rule changes the tree through the walk alone
// (Stats.HeldUpkeepWalk); from the seat's own end step, with the opponent
// mirrored in the tree, the opponent's own next upkeep and draw
// (HeldUpkeepOpp), never an unmirrored opponent's.
func TestHoldUpkeepSearchHoldsTheWalk(t *testing.T) {
	ws := holdWindows(t)
	search := func(w int, hold, opp, mirror bool) Result {
		t.Helper()
		e := ws[w].Clone()
		d := e.Pending()
		opts := DefaultOptions()
		opts.Sims, opts.AutoPayment, opts.HoldUpkeep = 400, true, hold
		opts.OpponentNodes, opts.OpponentMirror = opp, mirror
		return searchAt(t, e, d, passOf(t, d), nil, opts)
	}
	// want: 1 counted, 0 not, -1 either.
	for _, tc := range []struct {
		name              string
		w                 int
		opp, mirror       bool
		wantWalk, wantOpp int
	}{
		{"opponent's end step, seat only", winOppEnd, false, false, 1, 0},
		{"opponent's end step, opponent unmirrored", winOppEnd, true, false, 1, 0},
		{"opponent's end step, opponent mirrored", winOppEnd, true, true, 1, -1},
		{"own end step, opponent unmirrored", winOwnEnd, true, false, -1, 0},
		{"own end step, opponent mirrored", winOwnEnd, true, true, -1, 1},
	} {
		off, on := search(tc.w, false, tc.opp, tc.mirror), search(tc.w, true, tc.opp, tc.mirror)
		t.Logf("%s: visits %v -> %v, env steps %d -> %d, held in the walk %d, the opponent's %d", tc.name, off.Visits, on.Visits,
			off.Stats.EnvSteps, on.Stats.EnvSteps, on.Stats.HeldUpkeepWalk, on.Stats.HeldUpkeepOpp)
		if off.Stats.Searched != 1 || on.Stats.HeldUpkeep != 0 || on.Stats.HeldUpkeepBot != 0 || !reflect.DeepEqual(on.Keys, off.Keys) {
			t.Fatalf("%s: the root changed: searched %d, held %d, bot passed %d, keys %v, want %v", tc.name, off.Stats.Searched, on.Stats.HeldUpkeep, on.Stats.HeldUpkeepBot, on.Keys, off.Keys)
		}
		if off.Stats.HeldUpkeepWalk != 0 || off.Stats.HeldUpkeepOpp != 0 {
			t.Fatalf("%s: off counted %d %d", tc.name, off.Stats.HeldUpkeepWalk, off.Stats.HeldUpkeepOpp)
		}
		for _, c := range []struct {
			what       string
			want, have int
		}{{"the seat's", tc.wantWalk, on.Stats.HeldUpkeepWalk}, {"the opponent's", tc.wantOpp, on.Stats.HeldUpkeepOpp}} {
			if c.want >= 0 && (c.have > 0) != (c.want > 0) {
				t.Fatalf("%s: %s held in-walk decisions %d, want counted %v", tc.name, c.what, c.have, c.want > 0)
			}
		}
		if reflect.DeepEqual(off.Visits, on.Visits) && reflect.DeepEqual(off.Q, on.Q) && off.Stats.EnvSteps == on.Stats.EnvSteps {
			t.Fatalf("%s: the rule changed nothing in a 400-simulation search", tc.name)
		}
	}
}

// holdWalkPoint is one in-walk point: whose it was, its decision's sequence
// number, whether the rule's window held there, whether the cap cut its
// candidates, how many of them cast a spell, its keys and the keys of those
// that do not.
type holdWalkPoint struct {
	opp, window, cut bool
	seq              uint64
	casts            int
	keys, rest       []Key
}

// holdWalk walks one clairvoyant world from seat 0's opponent's-upkeep
// window, always playing candidate 0 (the bot's answer), and returns the
// points it reached. hold is the rule (nil off); opp puts the opponent in
// the tree mirroring the seat under mirror (OpponentMirror: auto-pay, and
// the rule when hold).
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
		out = append(out, holdWalkPoint{opp: pt.Opp, window: probe.on(env.e, env.cur), cut: pt.cut, seq: env.cur.Seq,
			casts: countCasts(env.cur, intentsOf(env.cands)), keys: pt.Keys, rest: withoutCasts(env.cur, env.cands)})
	}
	return out
}

// In the walk the seat's own upkeep and draw points cast nothing under the
// rule (and are points with casts without it): each is its unheld keys minus
// the casts (a cut list then refilled from the vocabulary), or not a point
// when fewer than two are left, and the seat's every other point has the
// same keys. With the opponent in the tree
// mirroring the seat, the opponent's own upkeep and draw are held the same
// way; without the mirror its points are what they were.
func TestHoldUpkeepInTheWalk(t *testing.T) {
	root := holdWindows(t)[winOppUpkeep]
	for _, tc := range []struct {
		name        string
		opp, mirror bool
	}{{"seat only", false, false}, {"opponent mirrored", true, true}, {"opponent unmirrored", true, false}} {
		off := holdWalk(t, root, nil, tc.opp, tc.mirror)
		on := holdWalk(t, root, newHoldUpkeep(""), tc.opp, tc.mirror)
		for _, side := range []bool{false, true} {
			if side && !tc.opp {
				continue
			}
			who := "seat"
			if side {
				who = "opponent"
			}
			held := !side || tc.mirror
			if !held {
				var offKeys, onKeys [][]Key
				for _, p := range off {
					if p.opp == side {
						offKeys = append(offKeys, p.keys)
					}
				}
				for _, p := range on {
					if p.opp == side {
						onKeys = append(onKeys, p.keys)
					}
				}
				t.Logf("%s, %s: %d points off, %d on", tc.name, who, len(offKeys), len(onKeys))
				if len(offKeys) == 0 || !reflect.DeepEqual(onKeys, offKeys) {
					t.Fatalf("%s, %s: the rule changed the %d points (%d with it)", tc.name, who, len(offKeys), len(onKeys))
				}
				continue
			}
			var offRest, onRest [][]Key
			onAt := map[uint64]holdWalkPoint{} // lookup only -- never ranged.
			offPts, offCasts, onPts, onCasts, kept := 0, 0, 0, 0, 0
			for _, p := range on {
				if p.opp != side {
					continue
				}
				onPts++
				if p.window {
					onAt[p.seq] = p
					if p.casts > 0 {
						onCasts++
					}
					continue
				}
				onRest = append(onRest, p.keys)
			}
			for _, p := range off {
				if p.opp != side {
					continue
				}
				offPts++
				if !p.window {
					offRest = append(offRest, p.keys)
					continue
				}
				if p.casts > 0 {
					offCasts++
				}
				q, ok := onAt[p.seq]
				switch {
				case ok:
					// The candidates without the casts, in order; where the
					// cap cut them, the freed slots go to the vocabulary's
					// next.
					kept++
					if !p.cut && !reflect.DeepEqual(q.keys, p.rest) || p.cut && (len(q.keys) < len(p.rest) || !reflect.DeepEqual(q.keys[:len(p.rest)], p.rest)) {
						t.Fatalf("%s, %s: held point %d keys %v, want %v (cut %v)", tc.name, who, p.seq, q.keys, p.rest, p.cut)
					}
				case len(p.rest) >= 2:
					t.Fatalf("%s, %s: held point %d (keys %v left) is no point", tc.name, who, p.seq, p.rest)
				}
			}
			t.Logf("%s, %s: points %d off (%d own upkeep/draw with a cast), %d on (%d; %d window points kept)",
				tc.name, who, offPts, offCasts, onPts, onCasts, kept)
			if !reflect.DeepEqual(onRest, offRest) || len(offRest) == 0 {
				t.Fatalf("%s, %s: the rule changed the %d points outside its window", tc.name, who, len(offRest))
			}
			if offCasts == 0 || onCasts != 0 || kept == 0 {
				t.Fatalf("%s, %s: own upkeep or draw points with a cast %d off, %d on; %d held points kept", tc.name, who, offCasts, onCasts, kept)
			}
		}
	}
}

// An opponent's point (OpponentNodes) at its own empty-stack upkeep holds
// its casts only under oppHold (OpponentMirror with HoldUpkeep), exactly:
// pass and the Beacon's ability are left. The rule on the walk's kinds alone
// leaves its candidates as they were, in either vocabulary.
func TestHoldUpkeepAtOpponentPointsOnlyMirrored(t *testing.T) {
	ws := holdWindows(t)
	for _, autoPay := range []bool{false, true} {
		e := ws[winOppOwnUpkeep].Clone()
		d := e.Pending()
		if !autoPay {
			// The manual vocabulary offers a cast once mana floats.
			if d = tapIsland(t, e); holdWindowOf(e, d) != winOppOwnUpkeep {
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
		if want := withoutCasts(d, offCands); on == nil || len(want) < 2 || !reflect.DeepEqual(keysOf(onCands), want) {
			t.Fatalf("autopay %v, oppHold on: point %v, keys %v, want %v", autoPay, on, keysOf(onCands), want)
		}
	}
}

// specialDeck is a mirror of Islands, Test Insight, and two instants with a
// special action that casts nothing, modelled on the corpus's Behold the
// Multiverse (Foretell) and Suspended Sentence (Suspend): Test Foresight
// ({1}{U}, Foretell {U}) and Test Stasis ({3}{U}, Suspend 2 -- {1}{U}).
func specialDeck(t *testing.T) []*cards.Card {
	t.Helper()
	insight := payWindowCard(t, "Name:Test Insight\nManaCost:U\nTypes:Instant\nA:SP$ Draw | SpellDescription$ Draw a card.\nOracle:Draw a card.\n")
	foresight := payWindowCard(t, "Name:Test Foresight\nManaCost:1 U\nTypes:Instant\nA:SP$ Draw | SpellDescription$ Draw a card.\nK:Foretell:U\nOracle:Draw a card.\n")
	stasis := payWindowCard(t, "Name:Test Stasis\nManaCost:3 U\nTypes:Instant\nK:Suspend:2:1 U\nA:SP$ Draw | SpellDescription$ Draw a card.\nOracle:Draw a card.\n")
	island := payWindowCard(t, "Name:Island\nTypes:Basic Land Island\nOracle:x\n")
	var deck []*cards.Card
	for i := 0; i < 17; i++ {
		deck = append(deck, island)
	}
	for i := 0; i < 8; i++ {
		deck = append(deck, insight, foresight, stasis)
	}
	return deck
}

// hasMode reports whether d offers a "cast" option of Mode mode.
func hasMode(d *decision.Decision, mode string) bool {
	for _, o := range d.Options {
		if o.Kind == "cast" && o.Mode == mode {
			return true
		}
	}
	return false
}

// specialWindow plays specialDeck's mirror (playHolding) to seat 0's own
// empty-stack upkeep with Foretell and Suspend offered once two Islands are
// tapped: the engine there, the mana floating.
func specialWindow(t *testing.T) *rules.Engine {
	t.Helper()
	var out *rules.Engine
	playHolding(t, specialDeck(t), func(e *rules.Engine, d *decision.Decision) bool {
		if holdWindowOf(e, d) != winOwnUpkeep || untappedIslands(e.G, 0) < 2 {
			return true
		}
		c := e.Clone()
		tapIsland(t, c)
		if nd := tapIsland(t, c); holdWindowOf(c, nd) == winOwnUpkeep && hasMode(nd, "foretell") && hasMode(nd, "suspend") {
			out = c
			return false
		}
		return true
	})
	if out == nil {
		t.Fatal("no own upkeep with Foretell and Suspend offered")
	}
	return out
}

// Foretell and Suspend ride the "cast" option Kind but are special actions
// that cast no spell: the rule keeps them, in either vocabulary and in a
// root macro, while it holds every cast beside them -- and, a turn after
// the foretelling, the foretold card's cast.
func TestHoldUpkeepKeepsSpecialActions(t *testing.T) {
	e := specialWindow(t)
	for _, autoPay := range []bool{false, true} {
		c := e.Clone()
		d := c.Pending()
		if autoPay {
			c.EnsurePaymentActions()
		}
		name := map[bool]string{false: "manual", true: "payment vocabulary"}[autoPay]
		checkHeldSpecial(t, name, c, d, autoPay, "foretell", "suspend")
	}
	// A root macro picking a special action is kept, one picking a cast
	// held.
	d := e.Pending()
	h := newHoldUpkeep("")
	for _, o := range d.Options {
		if o.Kind != "cast" {
			continue
		}
		m := &Macro{Key: MacroKeyPrefix + "pick", Steps: []MacroStep{{Player: 0, Kind: decision.KPriority, Picks: []rules.ScriptPick{{Kind: o.Kind, Obj: o.Obj, Label: o.Label}}}}}
		if got, want := h.holdsMacro(e, m), !testSpecialCast(o.Mode); got != want {
			t.Fatalf("a macro picking %q (mode %q): held %v, want %v", o.Label, o.Mode, got, want)
		}
	}
	// Foretell Test Foresight, play on to seat 0's next own upkeep, and cast
	// it from exile there with mana floating: held.
	c := e.Clone()
	cd := c.Pending()
	foretold := false
	for _, o := range cd.Options {
		if o.Kind == "cast" && o.Mode == "foretell" {
			if err := c.Submit(decision.Intent{Seq: cd.Seq, Player: cd.Player, Choices: []int{o.Index}}); err != nil {
				t.Fatal(err)
			}
			foretold = true
			break
		}
	}
	if !foretold {
		t.Fatal("no foretell option")
	}
	turn := c.G.Turn
	rngs := searchprobe.BotRandoms(9, 2)
	boards := [2]botpolicy.Board{botpolicy.NewBoard(2), botpolicy.NewBoard(2)}
	for p := range boards {
		boards[p].Cast = botpolicy.DefaultCastWeights
		boards[p].Cast.CastThreshold = math.MaxInt32
	}
	for steps := 0; steps < 3000 && !c.G.Over; steps++ {
		nd := c.Pending()
		if c.G.Turn > turn && holdWindowOf(c, nd) == winOwnUpkeep && untappedIslands(c.G, 0) >= 1 {
			fd := tapIsland(t, c)
			if !hasMode(fd, "foretell_cast") {
				t.Fatal("next own upkeep: the foretold card's cast is not offered")
			}
			for _, autoPay := range []bool{false, true} {
				cc := c.Clone()
				ccd := cc.Pending()
				if autoPay {
					cc.EnsurePaymentActions()
				}
				checkHeldSpecial(t, "foretold cast, "+map[bool]string{false: "manual", true: "payment vocabulary"}[autoPay], cc, ccd, autoPay, "foretell_cast")
			}
			return
		}
		in := botpolicy.Decide(botpolicy.BoardFromGameInto(c.G, c, nd.Player, &boards[nd.Player]), nd, rngs[nd.Player])
		if err := c.Submit(in); err != nil {
			t.Fatal(err)
		}
	}
	t.Fatal("seat 0's next own upkeep not reached")
}

// checkHeldSpecial enumerates d with and without the rule: the held
// candidates are exactly the unheld ones minus the casts, the special
// actions kept; each mode in modes is offered and is a candidate without
// the rule, and with it kept exactly when it is a special action.
func checkHeldSpecial(t *testing.T, name string, e *rules.Engine, d *decision.Decision, autoPay bool, modes ...string) {
	t.Helper()
	bot := passOf(t, d)
	all, _, _, ok, _ := enumerateCut(searchprobe.NewCollector(d.Player), e, d, bot, AllKinds(), BenchCandidateLimit, autoPay)
	if !ok || countCasts(d, intentsOf(all)) == 0 {
		t.Fatalf("%s: without the rule %v (ok %v), want a searched window with a cast", name, keysOf(all), ok)
	}
	k := heldKinds("")
	held, _, _, ok, _ := enumerateCut(searchprobe.NewCollector(d.Player), e, d, bot, k, BenchCandidateLimit, autoPay)
	want := withoutCasts(d, all)
	if len(want) < 2 {
		want = nil // skipped: SkipFewCandidates
	}
	got := keysOf(held)
	if len(got) == 0 {
		got = nil
	}
	if ok != (want != nil) || !reflect.DeepEqual(got, want) || k.hold.dropped == 0 {
		t.Fatalf("%s: with the rule %v (ok %v, dropped %d), want %v", name, got, ok, k.hold.dropped, want)
	}
	for _, mode := range modes {
		in, out := false, false
		for _, c := range all {
			if len(c.in.Choices) == 1 && c.in.Payment == nil && d.Options[c.in.Choices[0]].Mode == mode {
				in = true
				out = containsKey(keysOf(held), c.key)
			}
		}
		if !in || out != testSpecialCast(mode) {
			t.Fatalf("%s: mode %q a candidate %v, kept by the rule %v", name, mode, in, out)
		}
	}
}
