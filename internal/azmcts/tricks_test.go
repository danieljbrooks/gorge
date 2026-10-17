package azmcts

import (
	"context"
	"math/rand/v2"
	"reflect"
	"strings"
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
	"github.com/adams-shaun/gorge/view"
)

// The FDN scripts the tests play, verbatim from the card pool (the oracle
// text shortened), under their real names so DefaultTricks gates them, and
// three test cards: Test Prism (a land tapping for any colour, so no game is
// colour-screwed), Test Bear (a vanilla 2/2 for {1}) and Test Duelist (the
// same with first strike, so a combat with it gives priority in the
// combat-damage step, between the two damage passes).
const (
	scriptPrism         = "Name:Test Prism\nTypes:Land\nA:AB$ Mana | Cost$ T | Produced$ Any | Amount$ 1 | SpellDescription$ Add one mana of any color.\nOracle:{T}: Add one mana of any color.\n"
	scriptBear          = "Name:Test Bear\nManaCost:1\nTypes:Creature Bear\nPT:2/2\nOracle:\n"
	scriptDuelist       = "Name:Test Duelist\nManaCost:1\nTypes:Creature Human\nPT:2/2\nK:First Strike\nOracle:First strike\n"
	scriptGiantGrowth   = "Name:Giant Growth\nManaCost:G\nTypes:Instant\nA:SP$ Pump | ValidTgts$ Creature | NumAtt$ +3 | NumDef$ +3 | SpellDescription$ Target creature gets +3/+3 until end of turn.\nOracle:Target creature gets +3/+3 until end of turn.\n"
	scriptFlight        = "Name:Fleeting Flight\nManaCost:W\nTypes:Instant\nA:SP$ PutCounter | ValidTgts$ Creature | CounterType$ P1P1 | CounterNum$ 1 | SubAbility$ DBPump | SpellDescription$ Put a +1/+1 counter on target creature. It gains flying until end of turn. Prevent all combat damage that would be dealt to it this turn.\nSVar:DBPump:DB$ Pump | Defined$ Targeted | KW$ Flying & Prevent all combat damage that would be dealt to CARDNAME.\nOracle:Put a +1/+1 counter on target creature. It gains flying until end of turn.\n"
	scriptFakeDeath     = "Name:Fake Your Own Death\nManaCost:1 B\nTypes:Instant\nA:SP$ Pump | ValidTgts$ Creature | NumAtt$ +2 | SubAbility$ DBAnimate | SpellDescription$ Until end of turn, target creature gets +2/+0 and gains \"When this creature dies, return it to the battlefield tapped under its owner's control and you create a Treasure token.\"\nSVar:DBAnimate:DB$ Animate | Triggers$ TrigChangeZone | Defined$ ParentTarget\nSVar:TrigChangeZone:Mode$ ChangesZone | Origin$ Battlefield | Destination$ Graveyard | ValidCard$ Card.Self | Execute$ TrigChangeZone2 | TriggerController$ TriggeredCardController | TriggerDescription$ When this creature dies, return it to the battlefield tapped under its owner's control.\nSVar:TrigChangeZone2:DB$ ChangeZone | Origin$ Graveyard | Destination$ Battlefield | Tapped$ True | Defined$ TriggeredNewCardLKICopy\nSVar:PlayMain1:TRUE\nOracle:Until end of turn, target creature gets +2/+0.\n"
	scriptGoblinSurp    = "Name:Goblin Surprise\nManaCost:2 R\nTypes:Instant\nA:SP$ Charm | Choices$ DBPumpAll,DBToken\nSVar:DBPumpAll:DB$ PumpAll | ValidCards$ Creature.YouCtrl | NumAtt$ +2 | SpellDescription$ Creatures you control get +2/+0 until end of turn.\nSVar:DBToken:DB$ Token | TokenAmount$ 2 | TokenScript$ r_1_1_goblin | SpellDescription$ Create two 1/1 red Goblin creature tokens.\nOracle:Choose one.\n"
	scriptRefute        = "Name:Refute\nManaCost:1 U U\nTypes:Instant\nA:SP$ Counter | TargetType$ Spell | TgtPrompt$ Select target spell | ValidTgts$ Card | SubAbility$ DBDraw | SpellDescription$ Counter target spell. Draw a card, then discard a card.\nSVar:DBDraw:DB$ Draw | SubAbility$ DBDiscard\nSVar:DBDiscard:DB$ Discard | Defined$ You | Mode$ TgtChoose | NumCards$ 1\nOracle:Counter target spell. Draw a card, then discard a card.\n"
	scriptSnakeskinVeil = "Name:Snakeskin Veil\nManaCost:G\nTypes:Instant\nA:SP$ PutCounter | ValidTgts$ Creature.YouCtrl | CounterType$ P1P1 | CounterNum$ 1 | SubAbility$ DBPump | SpellDescription$ Put a +1/+1 counter on target creature you control. It gains hexproof until end of turn.\nSVar:DBPump:DB$ Pump | Defined$ Targeted | KW$ Hexproof\nOracle:Put a +1/+1 counter on target creature you control. It gains hexproof until end of turn.\n"
)

// The tricks the tests' deck holds (gated: all but Refute, which is a
// counterspell; Goblin Surprise is cast freely, its pump mode gated).
const (
	nGiantGrowth = "Giant Growth"
	nFlight      = "Fleeting Flight"
	nFakeDeath   = "Fake Your Own Death"
	nGoblinSurp  = "Goblin Surprise"
	nRefute      = "Refute"
	nSnakeskin   = "Snakeskin Veil"
	nBear        = "Test Bear"
	nDuelist     = "Test Duelist"
)

// trickDeck is the mirror the windows are played in: 17 Test Prisms, 9 Test
// Bears, 3 Test Duelists, and 11 spells -- Giant Growth three, Fleeting
// Flight, Goblin Surprise and Refute two each, Fake Your Own Death and
// Snakeskin Veil one each.
func trickDeck(t *testing.T) []*cards.Card {
	t.Helper()
	prism, bear, duelist := payWindowCard(t, scriptPrism), payWindowCard(t, scriptBear), payWindowCard(t, scriptDuelist)
	gg, ff, gs, ref := payWindowCard(t, scriptGiantGrowth), payWindowCard(t, scriptFlight), payWindowCard(t, scriptGoblinSurp), payWindowCard(t, scriptRefute)
	fake, veil := payWindowCard(t, scriptFakeDeath), payWindowCard(t, scriptSnakeskinVeil)
	var deck []*cards.Card
	for i := 0; i < 17; i++ {
		deck = append(deck, prism)
	}
	for i := 0; i < 9; i++ {
		deck = append(deck, bear)
	}
	for i := 0; i < 3; i++ {
		deck = append(deck, duelist, gg)
	}
	for i := 0; i < 2; i++ {
		deck = append(deck, ff, gs, ref)
	}
	return append(deck, fake, veil)
}

// trickName reports whether name is one of the deck's spells (never cast by
// playTricks' driver).
func trickName(name string) bool {
	switch name {
	case nGiantGrowth, nFlight, nFakeDeath, nGoblinSurp, nRefute, nSnakeskin:
		return true
	}
	return false
}

// playTricks plays trickDeck's mirror, seed after seed, with a scripted
// driver that keeps the tricks in hand and mana open: in its own main phase
// with an empty stack a seat plays a land, else casts one creature a turn
// (seat 0 only from turn bearFrom on), and passes everywhere else; it
// attacks with every creature it can; every other decision is the plain
// bot's. visit sees every pending decision before it is answered and
// returns false to stop.
func playTricks(t *testing.T, bearFrom int32, visit func(e *rules.Engine, d *decision.Decision) bool) {
	t.Helper()
	deck := trickDeck(t)
	for seed := uint64(1); seed < 80; seed++ {
		e := rules.New(rules.Config{Seed: seed, Names: []string{"a", "b"}, Decks: [][]*cards.Card{deck, deck}})
		e.Advance()
		rngs := searchprobe.BotRandoms(seed, 2)
		cast := [2]int32{-1, -1}
		for steps := 0; steps < 4000 && !e.G.Over; steps++ {
			d := e.Pending()
			if d.Kind == decision.KPriority {
				e.EnsurePaymentActions()
			}
			if !visit(e, d) {
				return
			}
			if err := e.Submit(trickDriver(e, d, rngs, &cast, bearFrom)); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// trickDriver is playTricks' answer at d.
func trickDriver(e *rules.Engine, d *decision.Decision, rngs []*rand.Rand, cast *[2]int32, bearFrom int32) decision.Intent {
	g := e.G
	p := d.Player
	switch d.Kind {
	case decision.KPriority:
		if g.Active == p && g.Step.IsMain() && len(g.Stack) == 0 {
			for _, o := range d.Options {
				if o.Kind == "play_land" {
					return decision.Intent{Seq: d.Seq, Player: p, Choices: []int{o.Index}}
				}
			}
			if cast[p] != g.Turn && (p != 0 || g.Turn >= bearFrom) {
				for _, a := range d.PaymentActions {
					if n := cardName(e, a.Cast.Object); len(a.Plans) > 0 && (n == nBear || n == nDuelist) {
						cast[p] = g.Turn
						return decision.Intent{Seq: d.Seq, Player: p, Payment: &decision.PaymentSelection{ActionID: a.ID, Plan: decision.ClonePaymentPlan(a.Plans[0])}}
					}
				}
			}
		}
		if in, ok := passAt(d); ok {
			return in
		}
	case decision.KAttackers:
		all := decision.Intent{Seq: d.Seq, Player: p}
		for _, o := range d.Options {
			all.Choices = append(all.Choices, o.Index)
		}
		if d.Validate(all) == nil {
			return all
		}
	}
	return botpolicy.Decide(botpolicy.BoardFromGame(g, e, p), d, rngs[p])
}

// The windows trickWindows finds: priority decisions of seat 0 but
// twOppBotCast, each with an empty stack but the three spell windows.
const (
	twMain       = iota // seat 0's own first main phase, both seats controlling a creature: closed
	twUpkeep            // seat 0's own upkeep, both controlling a creature: closed (and :holdupkeep's window)
	twBegin             // seat 0's own beginning of combat: closed, but for Fleeting Flight (its controller's own)
	twOppBegin          // seat 1's beginning of combat: closed, Fleeting Flight's too (not its controller's own)
	twNoAttack          // seat 0's own declare-attackers step with no creature attacking: closed
	twEndCombat         // seat 0's own end of combat step: closed
	twAttack            // seat 0's own declare-attackers step, its creatures (two or more) attacking: open
	twBlock             // seat 1's declare-blockers step, seat 1 attacking: open
	twDamage            // a combat-damage step between its two damage passes (a first striker in combat): open
	twOppSpell          // seat 1's creature spell on the stack, seat 0 controlling a creature: open, Refute castable
	twOwnSpell          // seat 0's own creature spell on the stack: open, Refute castable on its own spell only
	twNoOwn             // seat 1's creature spell on the stack, seat 0 controlling no creature: open, no own target
	twBotCast           // a closed window where seat 0's auto-pay bot casts a gated trick
	twOppBotCast        // a closed window where seat 1's auto-pay bot casts a gated trick (seat 1 to act)
	numTrickWindows
)

var trickWindowNames = [numTrickWindows]string{"own main", "own upkeep", "own beginning of combat", "opponent's beginning of combat",
	"own declare attackers, no attacker", "own end of combat", "own declare attackers", "opponent's declare blockers", "combat damage",
	"opponent's spell on the stack", "own spell on the stack", "opponent's spell, no own creature", "seat 0's bot casts a trick",
	"seat 1's bot casts a trick"}

// The gated tricks of the deck (Goblin Surprise is cast freely).
var gatedTricks = map[string]bool{nGiantGrowth: true, nFlight: true, nFakeDeath: true, nSnakeskin: true}

// castableNames is the names of the cards seat d.Player can cast at
// priority decision d through a payment action.
func castableNames(e *rules.Engine, d *decision.Decision) map[string]bool {
	out := map[string]bool{}
	for _, a := range e.EnsurePaymentActions() {
		if len(a.Plans) > 0 {
			out[cardName(e, a.Cast.Object)] = true
		}
	}
	return out
}

// castName is the name of the card candidate in casts at d (a payment cast
// or a "cast" option that is no special action), "" for any other answer.
func castName(e *rules.Engine, d *decision.Decision, in decision.Intent) string {
	if in.Payment != nil {
		for _, a := range d.PaymentActions {
			if a.ID == in.Payment.ActionID {
				return cardName(e, a.Cast.Object)
			}
		}
		return ""
	}
	if len(in.Choices) == 1 && in.Choices[0] >= 0 && in.Choices[0] < len(d.Options) {
		if o := d.Options[in.Choices[0]]; o.Kind == "cast" && !testSpecialCast(o.Mode) {
			return cardName(e, o.Obj)
		}
	}
	return ""
}

// closedWindow reports whether no trick window is open at seat p's decision
// (the rule's window, read independently): an empty stack and no combat
// step with an attacker, and not p's own beginning of combat.
func closedWindow(e *rules.Engine, p state.PlayerID) bool {
	g := e.G
	if len(g.Stack) > 0 || g.Step == state.StepBeginCombat && g.Active == p {
		return false
	}
	switch g.Step {
	case state.StepDeclareAttackers, state.StepDeclareBlockers, state.StepCombatDamage:
		return !anyAttacking(g)
	}
	return true
}

// trickWindowOf is the window d is, -1 for none. bot is the seat's auto-pay
// bot answer at d, asked only for the bot windows.
func trickWindowOf(t *testing.T, e *rules.Engine, d *decision.Decision, want func(int) bool) int {
	g := e.G
	if d == nil || d.Kind != decision.KPriority {
		return -1
	}
	c := castableNames(e, d)
	if d.Player == 1 {
		if want(twOppBotCast) && closedWindow(e, 1) && controlsCreature(e, 1) && (c[nGiantGrowth] || c[nFlight] || c[nFakeDeath]) {
			if gatedTricks[castName(e, d, autoPayBot(t, e, d))] {
				return twOppBotCast
			}
		}
		return -1
	}
	both := controlsCreature(e, 0) && controlsCreature(e, 1)
	if len(g.Stack) == 1 {
		top := g.Obj(g.Stack[0])
		if top == nil || !creatureCard(e, g.Stack[0]) || !c[nGiantGrowth] || !c[nRefute] {
			return -1
		}
		switch {
		case top.Controller == 1 && !controlsCreature(e, 0) && controlsCreature(e, 1) && c[nGoblinSurp]:
			return twNoOwn
		case top.Controller == 1 && both && c[nGoblinSurp] && untappedPrisms(e, 0) >= 4:
			return twOppSpell
		case top.Controller == 0 && both:
			return twOwnSpell
		}
		return -1
	}
	if len(g.Stack) != 0 || !both || !c[nGiantGrowth] {
		return -1
	}
	switch {
	case g.Active == 0 && g.Step == state.StepMain1 && c[nFlight] && c[nGoblinSurp] && c[nFakeDeath]:
		return twMain
	case g.Active == 0 && g.Step == state.StepUpkeep:
		return twUpkeep
	case g.Active == 0 && g.Step == state.StepBeginCombat && c[nFlight]:
		return twBegin
	case g.Active == 1 && g.Step == state.StepBeginCombat && c[nFlight]:
		return twOppBegin
	case g.Active == 0 && g.Step == state.StepDeclareAttackers && !anyAttacking(g):
		return twNoAttack
	case g.Active == 0 && g.Step == state.StepEndCombat:
		return twEndCombat
	case g.Active == 0 && g.Step == state.StepDeclareAttackers && anyAttacking(g) && c[nFlight] && c[nGoblinSurp] && creatures(e, 0) >= 2:
		return twAttack
	case g.Active == 1 && g.Step == state.StepDeclareBlockers && anyAttacking(g):
		return twBlock
	case g.Step == state.StepCombatDamage && anyAttacking(g):
		return twDamage
	}
	if want(twBotCast) && closedWindow(e, 0) && gatedTricks[castName(e, d, autoPayBot(t, e, d))] {
		return twBotCast
	}
	return -1
}

var trickWindowCache struct {
	once sync.Once
	ws   [numTrickWindows]*rules.Engine
}

// trickWindows is a clone of an engine at each window (the payment plans
// built), found once per test binary: twNoOwn in games where seat 0 casts no
// creature before turn 9, the others in games where both seats cast them.
func trickWindows(t *testing.T) [numTrickWindows]*rules.Engine {
	t.Helper()
	trickWindowCache.once.Do(func() {
		ws := &trickWindowCache.ws
		found := 0
		for _, from := range []int32{0, 9} {
			want := func(w int) bool { return ws[w] == nil && (w == twNoOwn) == (from > 0) }
			playTricks(t, from, func(e *rules.Engine, d *decision.Decision) bool {
				if w := trickWindowOf(t, e, d, want); w >= 0 && want(w) {
					c := e.Clone()
					c.EnsurePaymentActions()
					ws[w] = c
					found++
				}
				if from == 0 {
					return found < numTrickWindows-1
				}
				return ws[twNoOwn] == nil
			})
		}
	})
	var out [numTrickWindows]*rules.Engine
	for w, e := range trickWindowCache.ws {
		if e == nil {
			t.Fatalf("no %s window reached", trickWindowNames[w])
		}
		out[w] = e.Clone()
		out[w].EnsurePaymentActions()
	}
	return out
}

// creatureCard reports whether object id's card is a creature card.
func creatureCard(e *rules.Engine, id state.ObjID) bool {
	o := e.G.Obj(id)
	return o != nil && o.Face() != nil && o.Face().IsCreature()
}

// autoPayBot is the auto-pay bot's answer at e's pending decision d (the
// searching seat's own bot under AutoPayment).
func autoPayBot(t *testing.T, e *rules.Engine, d *decision.Decision) decision.Intent {
	t.Helper()
	in, err := seat.NewBot(7).EnableAutoPayMana().Decide(context.Background(), view.Project(e.G, e, d.Player, d), *d)
	if err != nil {
		t.Fatal(err)
	}
	return in
}

// trickKinds is kinds under the Tricks rule (names '|'-separated, "" the
// default list).
func trickKinds(kinds Kinds, names string) Kinds {
	kinds.tricks = newTrickRule(names)
	return kinds
}

// floatMana taps one of seat d.Player's Test Prisms for mana of colour sym
// (W, U, B, R, G) at e's pending priority decision; the pending decision
// after it.
func floatMana(t *testing.T, e *rules.Engine, sym string) *decision.Decision {
	t.Helper()
	d := e.Pending()
	for _, o := range d.Options {
		if o.Kind != "activate" || cardName(e, o.Obj) != "Test Prism" {
			continue
		}
		if err := e.Submit(decision.Intent{Seq: d.Seq, Player: d.Player, Choices: []int{o.Index}}); err != nil {
			t.Fatal(err)
		}
		c := e.Pending()
		for _, m := range c.Options {
			if m.ManaSymbol == sym {
				if err := e.Submit(decision.Intent{Seq: c.Seq, Player: c.Player, Choices: []int{m.Index}}); err != nil {
					t.Fatal(err)
				}
				return e.Pending()
			}
		}
		t.Fatalf("no %s in the Prism's colour choice", sym)
	}
	t.Fatal("no untapped Test Prism")
	return nil
}

// castNamed casts the card named name at e's pending priority decision
// through its payment action; the pending decision after it.
func castNamed(t *testing.T, e *rules.Engine, name string) *decision.Decision {
	t.Helper()
	d := e.Pending()
	for _, a := range e.EnsurePaymentActions() {
		if len(a.Plans) > 0 && cardName(e, a.Cast.Object) == name {
			in := decision.Intent{Seq: d.Seq, Player: d.Player, Payment: &decision.PaymentSelection{ActionID: a.ID, Plan: decision.ClonePaymentPlan(a.Plans[0])}}
			if err := e.Submit(in); err != nil {
				t.Fatal(err)
			}
			return e.Pending()
		}
	}
	t.Fatalf("%s is not castable", name)
	return nil
}

// payCastOf is the payment cast of the card named name at e's pending
// decision d.
func payCastOf(t *testing.T, e *rules.Engine, d *decision.Decision, name string) decision.Intent {
	t.Helper()
	for _, a := range e.EnsurePaymentActions() {
		if len(a.Plans) > 0 && cardName(e, a.Cast.Object) == name {
			return decision.Intent{Seq: d.Seq, Player: d.Player, Payment: &decision.PaymentSelection{ActionID: a.ID, Plan: decision.ClonePaymentPlan(a.Plans[0])}}
		}
	}
	t.Fatalf("%s is not castable", name)
	return decision.Intent{}
}

// trickExpect is what the rule drops at a window: the gated cards' casts
// (gated), the casts a side check drops (side), and the cards whose casts
// must be candidates without the rule there (offered; the window's
// guarantee), split by what the rule does with them.
type trickExpect struct {
	gated, side map[string]bool
	offered     []string
}

func nameSet(ns ...string) map[string]bool {
	m := map[string]bool{}
	for _, n := range ns {
		m[n] = true
	}
	return m
}

// trickExpects is each window's expectation.
var trickExpects = [numTrickWindows]trickExpect{
	twMain:       {gated: nameSet(nGiantGrowth, nFlight, nFakeDeath, nSnakeskin), offered: []string{nGiantGrowth, nFlight, nFakeDeath, nGoblinSurp}},
	twUpkeep:     {gated: nameSet(nGiantGrowth, nFlight, nFakeDeath, nSnakeskin), offered: []string{nGiantGrowth}},
	twBegin:      {gated: nameSet(nGiantGrowth, nFakeDeath, nSnakeskin), offered: []string{nGiantGrowth, nFlight}},
	twOppBegin:   {gated: nameSet(nGiantGrowth, nFlight, nFakeDeath, nSnakeskin), offered: []string{nGiantGrowth, nFlight}},
	twNoAttack:   {gated: nameSet(nGiantGrowth, nFlight, nFakeDeath, nSnakeskin), offered: []string{nGiantGrowth}},
	twEndCombat:  {gated: nameSet(nGiantGrowth, nFlight, nFakeDeath, nSnakeskin), offered: []string{nGiantGrowth}},
	twAttack:     {offered: []string{nGiantGrowth, nFlight, nGoblinSurp}},
	twBlock:      {offered: []string{nGiantGrowth}},
	twDamage:     {offered: []string{nGiantGrowth}},
	twOppSpell:   {offered: []string{nGiantGrowth, nRefute}},
	twOwnSpell:   {side: nameSet(nRefute), offered: []string{nGiantGrowth, nRefute}},
	twNoOwn:      {side: nameSet(nGiantGrowth, nFlight, nFakeDeath), offered: []string{nGiantGrowth, nRefute}},
	twBotCast:    {gated: nameSet(nGiantGrowth, nFlight, nFakeDeath, nSnakeskin)},
	twOppBotCast: {gated: nameSet(nGiantGrowth, nFlight, nFakeDeath, nSnakeskin)},
}

// checkTrickEnum enumerates d with and without the rule (autoPay: the
// payment vocabulary, else the manual one; names the gated list) and wants
// exactly the candidates without the rule minus the casts x drops, the
// offered ones among the candidates without it, and the rule's counters to
// say why.
func checkTrickEnum(t *testing.T, name string, e *rules.Engine, d *decision.Decision, x trickExpect, autoPay bool, tricksNames string, offered []string) {
	t.Helper()
	bot := passOf(t, d)
	all, _, _, ok, _ := enumerateCut(searchprobe.NewCollector(d.Player), e, d, bot, AllKinds(), BenchCandidateLimit, autoPay)
	if !ok {
		t.Fatalf("%s: not searched without the rule", name)
	}
	seen := map[string]bool{}
	var want []Key
	gated, side := false, false
	for _, c := range all {
		n := castName(e, d, c.in)
		seen[n] = true
		switch {
		case x.gated[n]:
			gated = true
		case x.side[n]:
			side = true
		default:
			want = append(want, c.key)
		}
	}
	for _, n := range offered {
		if !seen[n] {
			t.Fatalf("%s: %s is no candidate without the rule: %v", name, n, keysOf(all))
		}
	}
	k := trickKinds(AllKinds(), tricksNames)
	on, _, _, ok, _ := enumerateCut(searchprobe.NewCollector(d.Player), e, d, bot, k, BenchCandidateLimit, autoPay)
	got := keysOf(on)
	if !ok {
		got = nil
	}
	if len(want) < 2 {
		want = nil
	}
	t.Logf("%s: %d candidates without the rule, %d with it (gated %v, side %v)", name, len(all), len(got), gated, side)
	if !reflect.DeepEqual(got, want) || (k.tricks.gatedN > 0) != gated || (k.tricks.sideN > 0) != side || (k.tricks.changed > 0) != (gated || side) {
		t.Fatalf("%s: with the rule %v (gated %d, side %d, changed %d), want %v (gated %v, side %v)", name, got, k.tricks.gatedN, k.tricks.sideN, k.tricks.changed, want, gated, side)
	}
}

// Every window, in the payment vocabulary: a gated card's cast is dropped
// outside the trick windows (Fleeting Flight kept at its controller's own
// beginning of combat), kept inside them; Goblin Surprise's cast is always
// kept; a gated card with no target on its caster's side is dropped inside
// a window too; Refute is dropped with only its caster's own spell on the
// stack and kept over an opponent's. With mana floating, the manual
// vocabulary's legacy casts are dropped the same way.
func TestTricksGatesCastsOutsideTheWindows(t *testing.T) {
	ws := trickWindows(t)
	for w, e := range ws {
		checkTrickEnum(t, trickWindowNames[w], e, e.Pending(), trickExpects[w], true, "", trickExpects[w].offered)
	}
	// Mana floating (G and W: Giant Growth, Fleeting Flight, Snakeskin
	// Veil), the manual vocabulary.
	for _, w := range []int{twMain, twBegin, twOppBegin, twEndCombat, twAttack, twBlock, twNoOwn} {
		e := ws[w].Clone()
		if n := untappedPrisms(e, 0); n < 2 {
			t.Logf("%s: %d untapped Prisms", trickWindowNames[w], n)
			continue
		}
		floatMana(t, e, "G")
		d := floatMana(t, e, "W")
		offered := []string{nGiantGrowth}
		if castableNames(ws[w], ws[w].Pending())[nFlight] {
			offered = append(offered, nFlight)
		}
		checkTrickEnum(t, trickWindowNames[w]+", mana floating", e, d, trickExpects[w], false, "", offered)
		e.EnsurePaymentActions()
		checkTrickEnum(t, trickWindowNames[w]+", mana floating, payment vocabulary", e, d, trickExpects[w], true, "", offered)
	}
	// TricksCards replaces the list: only Fleeting Flight gated, the side
	// checks of Refute (a counterspell, by script) as before.
	only := func(x trickExpect, keep ...string) trickExpect {
		y := trickExpect{gated: map[string]bool{}, side: map[string]bool{}}
		for n := range x.gated {
			for _, k := range keep {
				if n == k {
					y.gated[n] = true
				}
			}
		}
		for n := range x.side {
			for _, k := range keep {
				if n == k {
					y.side[n] = true
				}
			}
		}
		return y
	}
	for _, w := range []int{twMain, twOppBegin, twOwnSpell, twNoOwn} {
		e := ws[w]
		checkTrickEnum(t, trickWindowNames[w]+", Fleeting Flight alone", e, e.Pending(), only(trickExpects[w], nFlight, nRefute), true, "Fleeting Flight", nil)
	}
}

// untappedPrisms counts seat p's untapped Test Prisms.
func untappedPrisms(e *rules.Engine, p state.PlayerID) int {
	n := 0
	for _, id := range e.G.Zone(state.ZBattlefield, p) {
		if o := e.G.Obj(id); o != nil && !o.Tapped && cardName(e, id) == "Test Prism" {
			n++
		}
	}
	return n
}

// trickSearch is Search at a clone of e (its pending decision, the bot's
// answer bot, pass when zero) with Sims 4, auto-payment and Options.Tricks
// as given.
func trickSearch(t *testing.T, e *rules.Engine, bot decision.Intent, tricks bool, cards string, root func(*Root), mods ...func(*Options)) Result {
	t.Helper()
	c := e.Clone()
	d := c.Pending()
	if bot.Choices == nil && bot.Payment == nil {
		bot = passOf(t, d)
	}
	obs := searchprobe.NewCollector(d.Player)
	src, err := newTestClairvoyant(c, obs)
	if err != nil {
		t.Fatal(err)
	}
	opts := DefaultOptions()
	opts.Sims, opts.AutoPayment, opts.Tricks, opts.TricksCards = 4, true, tricks, cards
	for _, m := range mods {
		m(&opts)
	}
	r := Root{Engine: c, Decision: d, Bot: bot, Observer: obs}
	if root != nil {
		root(&r)
	}
	res, err := Search(context.Background(), r, src, nil, opts)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// Search with Options.Tricks: at a closed window the root is the unruled
// vocabulary minus the gated casts (capped: a dropped cast frees its slot),
// Stats.TricksGated 1; a side-check root counts TricksSide; an open window
// searches exactly as without the rule. A bot answer the rule drops is
// played as pass, unsearched (TricksBot), and a full root's macro casting a
// gated card is left out. It composes with HoldUpkeep: in the own upkeep
// HoldUpkeep holds every cast first, elsewhere Tricks gates alone.
func TestTricksAtTheSearchRoot(t *testing.T) {
	ws := trickWindows(t)
	limit := DefaultOptions().Limit
	for w, e := range ws {
		name := trickWindowNames[w]
		d := e.Pending()
		x := trickExpects[w]
		off := trickSearch(t, e, decision.Intent{}, false, "", nil)
		on := trickSearch(t, e, decision.Intent{}, true, "", nil)
		all, _, _, _, _ := enumerateCut(searchprobe.NewCollector(d.Player), e.Clone(), d, passOf(t, d), AllKinds(), BenchCandidateLimit, true)
		var want []Key
		gated, side := 0, 0
		for _, c := range all {
			switch n := castName(e, d, c.in); {
			case x.gated[n]:
				gated = 1
			case x.side[n]:
				side = 1
			default:
				want = append(want, c.key)
			}
		}
		if len(want) > limit {
			want = want[:limit]
		}
		if len(want) < 2 {
			want = nil // skipped: SkipFewCandidates, the bot's pass played
		}
		if len(on.Keys) == 0 {
			on.Keys = nil
		}
		if off.Stats.Searched != 1 || off.Stats.TricksGated+off.Stats.TricksSide+off.Stats.TricksBot != 0 {
			t.Fatalf("%s, off: searched %d, tricks counters %d %d %d", name, off.Stats.Searched, off.Stats.TricksGated, off.Stats.TricksSide, off.Stats.TricksBot)
		}
		if on.Stats.TricksGated != gated || on.Stats.TricksSide != side || on.Stats.TricksBot != 0 || !reflect.DeepEqual(on.Keys, want) {
			t.Fatalf("%s, on: gated %d side %d bot %d, keys %v; want gated %d side %d, keys %v", name, on.Stats.TricksGated, on.Stats.TricksSide, on.Stats.TricksBot, on.Keys, gated, side, want)
		}
		if gated+side == 0 && !reflect.DeepEqual(on.Visits, off.Visits) {
			t.Fatalf("%s: the rule changed an open window's search: visits %v, want %v", name, on.Visits, off.Visits)
		}
	}
	// The bot casts a gated trick outside the windows: pass, unsearched.
	for _, w := range []int{twMain, twBotCast} {
		e := ws[w]
		d := e.Pending()
		bot := payCastOf(t, e, d, nGiantGrowth)
		off := trickSearch(t, e, bot, false, "", nil)
		on := trickSearch(t, e, bot, true, "", nil)
		if off.Stats.Searched != 1 || castName(e, d, off.Candidates[0]) != nGiantGrowth {
			t.Fatalf("%s, a bot cast, off: searched %d, candidates %v", trickWindowNames[w], off.Stats.Searched, off.Labels)
		}
		if on.Stats.TricksBot != 1 || on.Stats.TricksGated != 1 || on.Stats.Skipped != 1 || !reflect.DeepEqual(on.Intent, passOf(t, d)) {
			t.Fatalf("%s, a bot cast, on: bot %d gated %d skipped %d, played %+v", trickWindowNames[w], on.Stats.TricksBot, on.Stats.TricksGated, on.Stats.Skipped, on.Intent)
		}
	}
	// The manual vocabulary with mana floating: the bot's legacy cast is
	// played as pass too.
	{
		e := ws[twMain].Clone()
		floatMana(t, e, "G")
		d := e.Pending()
		var cast decision.Intent
		for _, o := range d.Options {
			if o.Kind == "cast" && cardName(e, o.Obj) == nGiantGrowth {
				cast = decision.Intent{Seq: d.Seq, Player: d.Player, Choices: []int{o.Index}}
			}
		}
		if cast.Choices == nil {
			t.Fatal("own main, G floating: no legacy Giant Growth cast")
		}
		manual := func(o *Options) { o.AutoPayment = false }
		off, on := trickSearch(t, e, cast, false, "", nil, manual), trickSearch(t, e, cast, true, "", nil, manual)
		if off.Stats.Searched != 1 || on.Stats.TricksBot != 1 || on.Stats.Skipped != 1 || !reflect.DeepEqual(on.Intent, passOf(t, d)) {
			t.Fatalf("own main, a legacy bot cast: off searched %d; on bot %d skipped %d, played %+v", off.Stats.Searched, on.Stats.TricksBot, on.Stats.Skipped, on.Intent)
		}
	}
	// Options.TricksCards replaces the list: Fleeting Flight alone, Giant
	// Growth's cast is kept and the bot's is searched.
	{
		e := ws[twMain]
		d := e.Pending()
		bot := payCastOf(t, e, d, nGiantGrowth)
		alone := trickSearch(t, e, bot, true, nFlight, nil)
		names := map[string]bool{}
		for _, in := range alone.Candidates {
			names[castName(e, d, in)] = true
		}
		if alone.Stats.TricksBot != 0 || alone.Stats.TricksGated != 1 || alone.Stats.Searched != 1 || !names[nGiantGrowth] || names[nFlight] {
			t.Fatalf("own main, Fleeting Flight alone: bot %d gated %d searched %d, candidates %v", alone.Stats.TricksBot, alone.Stats.TricksGated, alone.Stats.Searched, alone.Labels)
		}
	}
	// A bot cast inside a window stands: candidate 0, searched.
	{
		e := ws[twAttack]
		d := e.Pending()
		bot := payCastOf(t, e, d, nGiantGrowth)
		off, on := trickSearch(t, e, bot, false, "", nil), trickSearch(t, e, bot, true, "", nil)
		if on.Stats.TricksBot != 0 || on.Stats.Searched != 1 || !reflect.DeepEqual(on.Keys, off.Keys) || !reflect.DeepEqual(on.Visits, off.Visits) {
			t.Fatalf("own declare attackers, a bot cast: bot %d, searched %d, keys %v, want %v", on.Stats.TricksBot, on.Stats.Searched, on.Keys, off.Keys)
		}
	}
	// The full root: no bot candidate, and a macro casting Giant Growth.
	for _, w := range []int{twMain, twAttack} {
		e := ws[w]
		var obj state.ObjID
		for _, a := range e.EnsurePaymentActions() {
			if len(a.Plans) > 0 && cardName(e, a.Cast.Object) == nGiantGrowth {
				obj = a.Cast.Object
			}
		}
		full := func(r *Root) {
			r.NoBot = true
			r.Macros = []Macro{{Key: MacroKeyPrefix + "growth", Label: "macro growth", Steps: []MacroStep{{Player: 0, Kind: decision.KPriority, PayCast: obj}}}}
		}
		wide := func(o *Options) { o.Limit = BenchCandidateLimit }
		off, on := trickSearch(t, e, decision.Intent{}, false, "", full, wide), trickSearch(t, e, decision.Intent{}, true, "", full, wide)
		held := w == twMain
		if !containsKey(off.Keys, MacroKeyPrefix+"growth") || containsKey(on.Keys, MacroKeyPrefix+"growth") != !held || (on.Stats.TricksGated == 1) != held {
			t.Fatalf("%s, full root: keys %v -> %v, gated %d", trickWindowNames[w], off.Keys, on.Keys, on.Stats.TricksGated)
		}
	}
	// With HoldUpkeep: in the own upkeep the held casts go first, so Tricks
	// gates none of them; in the own main phase HoldUpkeep is off and
	// Tricks gates alone.
	both := func(o *Options) { o.HoldUpkeep = true }
	for _, w := range []int{twUpkeep, twMain} {
		e := ws[w]
		hold := trickSearch(t, e, decision.Intent{}, false, "", nil, both)
		tricks := trickSearch(t, e, decision.Intent{}, true, "", nil)
		comp := trickSearch(t, e, decision.Intent{}, true, "", nil, both)
		want := tricks
		if w == twUpkeep {
			want = hold
		}
		if !reflect.DeepEqual(comp.Keys, want.Keys) || comp.Stats.HeldUpkeep != hold.Stats.HeldUpkeep || (comp.Stats.TricksGated == 1) != (w == twMain) || (comp.Stats.HeldUpkeep == 1) != (w == twUpkeep) {
			t.Fatalf("%s, with HoldUpkeep: keys %v (held %d, gated %d), want %v", trickWindowNames[w], comp.Keys, comp.Stats.HeldUpkeep, comp.Stats.TricksGated, want.Keys)
		}
	}
	bad := DefaultOptions()
	bad.TricksCards = "Giant Growth"
	if err := bad.Validate(nil); err == nil {
		t.Fatal("a tricks list without Tricks validated")
	}
}

// Goblin Surprise is cast freely; at its modes decision the pump mode is
// gated (outside the windows: the spell itself on the stack opens none) and
// side-checked (no creature of the caster's), the token mode never. A bot
// answer picking the dropped pump mode is replaced by the token mode, with
// the modes searched or not.
func TestTricksGoblinSurpriseModes(t *testing.T) {
	ws := trickWindows(t)
	for _, tc := range []struct {
		w          int
		drop, side bool
	}{{twMain, true, false}, {twAttack, false, false}, {twOppSpell, false, false}, {twNoOwn, true, true}} {
		name := trickWindowNames[tc.w]
		e := ws[tc.w].Clone()
		d := castNamed(t, e, nGoblinSurp)
		if d.Kind != decision.KModes || len(d.Options) != 2 || cardName(e, d.Source) != nGoblinSurp {
			t.Fatalf("%s: after casting Goblin Surprise a %s decision for %s with %d options", name, d.Kind, cardName(e, d.Source), len(d.Options))
		}
		pump, token := -1, -1
		for i, o := range d.Options {
			if strings.Contains(o.Label, "+2/+0") {
				pump = i
			} else if strings.Contains(o.Label, "Goblin") {
				token = i
			}
		}
		if pump < 0 || token < 0 {
			t.Fatalf("%s: modes %+v", name, d.Options)
		}
		if alone := newTrickRule(nFlight).gate(e, d); alone.optionDrops() != nil {
			t.Fatalf("%s: Goblin Surprise not listed, yet a mode dropped", name)
		}
		r := newTrickRule("")
		g := r.gate(e, d)
		drop := g.optionDrops()
		g.finish()
		if dropped := drop != nil && drop[pump]; dropped != tc.drop || drop != nil && drop[token] || tc.drop && (r.sideN > 0) != tc.side || tc.drop && (r.gatedN > 0) == tc.side {
			t.Fatalf("%s: drops %v (gated %d, side %d), want the pump mode dropped %v (side %v)", name, drop, r.gatedN, r.sideN, tc.drop, tc.side)
		}
		pick := func(i int) decision.Intent { return decision.Intent{Seq: d.Seq, Player: d.Player, Choices: []int{i}} }
		// The modes searched (FullKinds): the token mode alone is left, so
		// the decision is skipped.
		all, _, _, ok, _ := enumerateCut(searchprobe.NewCollector(d.Player), e, d, pick(token), FullKinds(), BenchCandidateLimit, true)
		on, _, _, okOn, _ := enumerateCut(searchprobe.NewCollector(d.Player), e, d, pick(token), trickKinds(FullKinds(), ""), BenchCandidateLimit, true)
		if !ok || len(all) != 2 || okOn == tc.drop || !tc.drop && !reflect.DeepEqual(keysOf(on), keysOf(all)) {
			t.Fatalf("%s: modes %v (ok %v) -> %v (ok %v)", name, keysOf(all), ok, keysOf(on), okOn)
		}
		for _, kinds := range []Kinds{FullKinds(), AllKinds()} {
			k := func(o *Options) { o.Sims, o.Kinds = 0, kinds }
			off := trickSearch(t, e, pick(pump), false, "", nil, k)
			got := trickSearch(t, e, pick(pump), true, "", nil, k)
			want, bot := pick(pump), 0
			if tc.drop {
				want, bot = pick(token), 1
			}
			if !reflect.DeepEqual(off.Intent, pick(pump)) || !reflect.DeepEqual(got.Intent, want) || got.Stats.TricksBot != bot {
				t.Fatalf("%s, modes searched %v: the bot's pump mode played %+v off, %+v on (bot %d)", name, kinds.Modes, off.Intent, got.Intent, got.Stats.TricksBot)
			}
		}
	}
}

// sideOf is whether each of target decision d's options is on seat p's side.
func sideOf(e *rules.Engine, d *decision.Decision, p state.PlayerID) []bool {
	own := make([]bool, len(d.Options))
	for i, o := range d.Options {
		if o.Kind == "player" {
			own[i] = o.Player == p
		} else if x := e.G.Obj(o.Obj); x != nil {
			own[i] = x.Controller == p
		}
	}
	return own
}

// checkTargetDrops wants the rule at target decision d to drop exactly the
// options wantDrop marks (none: nil), and a bot answer picking a dropped one
// replaced by the first kept option (TricksBot), the target searched
// (AllKinds) or not.
func checkTargetDrops(t *testing.T, name string, e *rules.Engine, d *decision.Decision, wantDrop []bool) {
	t.Helper()
	r := newTrickRule("")
	g := r.gate(e, d)
	drop := g.optionDrops()
	g.finish()
	if !reflect.DeepEqual(drop, wantDrop) || (r.sideN > 0) != (wantDrop != nil) || r.gatedN != 0 {
		t.Fatalf("%s: drops %v (side %d, gated %d), want %v", name, drop, r.sideN, r.gatedN, wantDrop)
	}
	pick := func(i int) decision.Intent {
		return decision.Intent{Seq: d.Seq, Player: d.Player, Choices: []int{d.Options[i].Index}}
	}
	firstKeptAt, firstDropAt := -1, -1
	for i := range d.Options {
		if wantDrop == nil || !wantDrop[i] {
			if firstKeptAt < 0 {
				firstKeptAt = i
			}
		} else if firstDropAt < 0 {
			firstDropAt = i
		}
	}
	// Enumerated with the bot on a kept option: the dropped ones are gone.
	all, _, _, ok, _ := enumerateCut(searchprobe.NewCollector(d.Player), e, d, pick(firstKeptAt), AllKinds(), BenchCandidateLimit, true)
	on, _, _, okOn, _ := enumerateCut(searchprobe.NewCollector(d.Player), e, d, pick(firstKeptAt), trickKinds(AllKinds(), ""), BenchCandidateLimit, true)
	var want []Key
	for _, c := range all {
		if wantDrop == nil || !wantDrop[c.in.Choices[0]] {
			want = append(want, c.key)
		}
	}
	if len(want) < 2 {
		want = nil
	}
	got := keysOf(on)
	if !okOn {
		got = nil
	}
	if !ok {
		want = nil // a single legal target: not searched, with or without the rule
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s: targets %v (ok %v) -> %v, want %v", name, keysOf(all), ok, got, want)
	}
	if firstDropAt < 0 {
		return
	}
	// The bot's answer a dropped target: outside the candidates, so the
	// decision is not searched (skipped where targets are a searched kind)
	// and the first kept target is played.
	for _, kinds := range []Kinds{AllKinds(), {Priority: true}} {
		k := func(o *Options) { o.Kinds = kinds }
		off := trickSearch(t, e, pick(firstDropAt), false, "", nil, func(o *Options) { o.Sims, o.Kinds = 0, kinds })
		res := trickSearch(t, e, pick(firstDropAt), true, "", nil, k)
		if !reflect.DeepEqual(off.Intent, pick(firstDropAt)) || !reflect.DeepEqual(res.Intent, pick(firstKeptAt)) || res.Stats.TricksBot != 1 ||
			res.Stats.TricksSide != btoi(kinds.Target) || res.Stats.Searched != 0 || res.Stats.Skipped != btoi(kinds.Target) {
			t.Fatalf("%s, targets searched %v: the bot's dropped target played %+v off, %+v on (bot %d, side %d, searched %d, skipped %d), want %+v",
				name, kinds.Target, off.Intent, res.Intent, res.Stats.TricksBot, res.Stats.TricksSide, res.Stats.Searched, res.Stats.Skipped, pick(firstKeptAt))
		}
	}
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

// At a gated card's target decision the opponent's creatures are dropped
// when one of the caster's is offered, and kept when none is (a cast the
// rule would not offer, made by hand). At a counterspell's the caster's own
// spells are dropped when an opponent's is offered, and kept when none is.
func TestTricksTargets(t *testing.T) {
	ws := trickWindows(t)
	for _, n := range []string{nGiantGrowth, nFlight, nFakeDeath} {
		e := ws[twAttack].Clone()
		if !castableNames(e, e.Pending())[n] {
			continue
		}
		d := castNamed(t, e, n)
		if d.Kind != decision.KTarget || cardName(e, d.Source) != n {
			t.Fatalf("%s: after the cast a %s decision for %s", n, d.Kind, cardName(e, d.Source))
		}
		own := sideOf(e, d, 0)
		drop := make([]bool, len(own))
		anyOwn, anyOpp := false, false
		for i := range own {
			drop[i] = !own[i]
			anyOwn, anyOpp = anyOwn || own[i], anyOpp || !own[i]
		}
		if !anyOwn || !anyOpp {
			t.Fatalf("%s at %s: sides %v, want both", n, trickWindowNames[twAttack], own)
		}
		checkTargetDrops(t, n+" at "+trickWindowNames[twAttack], e, d, drop)
	}
	// Forced: only the opponent's creatures are legal.
	{
		e := ws[twNoOwn].Clone()
		d := castNamed(t, e, nGiantGrowth)
		for _, o := range sideOf(e, d, 0) {
			if o {
				t.Fatal("no own creature, yet an own target")
			}
		}
		checkTargetDrops(t, "Giant Growth with no own creature", e, d, nil)
	}
	// Refute over an opponent's creature spell and the caster's own Giant
	// Growth (cast in response on its own creature).
	{
		e := ws[twOppSpell].Clone()
		d := castNamed(t, e, nGiantGrowth)
		own := sideOf(e, d, 0)
		at := -1
		for i := range own {
			if own[i] {
				at = i
				break
			}
		}
		if d.Kind != decision.KTarget || at < 0 {
			t.Fatalf("Giant Growth's target decision: %s, sides %v", d.Kind, own)
		}
		if err := e.Submit(decision.Intent{Seq: d.Seq, Player: d.Player, Choices: []int{d.Options[at].Index}}); err != nil {
			t.Fatal(err)
		}
		pd := e.Pending()
		if pd.Kind != decision.KPriority || pd.Player != 0 || len(e.G.Stack) != 2 || !castableNames(e, pd)[nRefute] {
			t.Fatalf("after Giant Growth: %s for %d, stack %v, castable %v", pd.Kind, pd.Player, e.G.Stack, castableNames(e, pd))
		}
		checkTrickEnum(t, "Refute over both seats' spells", e, pd, trickExpect{}, true, "", []string{nRefute})
		rd := castNamed(t, e, nRefute)
		side := sideOf(e, rd, 0)
		if rd.Kind != decision.KTarget || len(side) != 2 || side[0] == side[1] {
			t.Fatalf("Refute's target decision: %s, sides %v", rd.Kind, side)
		}
		checkTargetDrops(t, "Refute over both seats' spells", e, rd, side)
	}
	// Refute forced onto the caster's own spell, the only one.
	{
		e := ws[twOwnSpell].Clone()
		rd := castNamed(t, e, nRefute)
		if side := sideOf(e, rd, 0); rd.Kind != decision.KTarget || len(side) == 0 || !side[0] {
			t.Fatalf("Refute on its own spell: %s, sides %v", rd.Kind, side)
		}
		checkTargetDrops(t, "Refute on the caster's own spell", e, rd, nil)
	}
}

// Search from a root the rule leaves alone (the own declare-attackers step)
// deep enough to reach closed windows in the walk: the seat's are gated
// (Stats.TricksWalk), a mirrored opponent's too (TricksOpp), an unmirrored
// opponent's never. The tree differs with the rule on.
func TestTricksSearchGatesTheWalk(t *testing.T) {
	ws := trickWindows(t)
	for _, tc := range []struct {
		name        string
		opp, mirror bool
	}{{"seat only", false, false}, {"opponent unmirrored", true, false}, {"opponent mirrored", true, true}} {
		mods := func(o *Options) { o.Sims, o.OpponentNodes, o.OpponentMirror = 400, tc.opp, tc.mirror }
		off := trickSearch(t, ws[twAttack], decision.Intent{}, false, "", nil, mods)
		on := trickSearch(t, ws[twAttack], decision.Intent{}, true, "", nil, mods)
		t.Logf("%s: visits %v -> %v, env steps %d -> %d, walk %d, opponent %d", tc.name, off.Visits, on.Visits, off.Stats.EnvSteps, on.Stats.EnvSteps, on.Stats.TricksWalk, on.Stats.TricksOpp)
		if off.Stats.Searched != 1 || on.Stats.TricksGated+on.Stats.TricksSide+on.Stats.TricksBot != 0 || !reflect.DeepEqual(on.Keys, off.Keys) {
			t.Fatalf("%s: the root changed: keys %v, want %v", tc.name, on.Keys, off.Keys)
		}
		if off.Stats.TricksWalk+off.Stats.TricksOpp != 0 || on.Stats.TricksWalk == 0 || (on.Stats.TricksOpp > 0) != tc.mirror {
			t.Fatalf("%s: walk %d, opponent %d (off %d %d), want the seat's gated and the opponent's only mirrored", tc.name, on.Stats.TricksWalk, on.Stats.TricksOpp, off.Stats.TricksWalk, off.Stats.TricksOpp)
		}
		if reflect.DeepEqual(off.Visits, on.Visits) && reflect.DeepEqual(off.Q, on.Q) && off.Stats.EnvSteps == on.Stats.EnvSteps {
			t.Fatalf("%s: the rule changed nothing in a 400-simulation search", tc.name)
		}
	}
}

// trickEnv is a walk env on a clone of engine e at its pending decision,
// seat 0 searching with its auto-pay bot, over kinds with the rule tricks
// (nil off) hung on them; seat 1 in the tree with its auto-pay bot
// (oppAutoPay), gated under oppTricks; the walk's step cap maxSteps.
func trickEnv(t *testing.T, e *rules.Engine, kinds Kinds, tricks *trickRule, oppTricks bool, maxSteps int) (*engineEnv, *Stats) {
	t.Helper()
	c := e.Clone()
	kinds.tricks = tricks
	st := &Stats{}
	cfg := &walkConfig{kinds: kinds, limit: DefaultOptions().Limit, maxSteps: maxSteps, envSeed: 0x747269636b73, actor: 0, rootDec: c.Pending(), stats: st,
		autoPayment: true, oppNodes: true, oppForks: []*searchprobe.Forker{nil, searchprobe.NewCollector(1).Forker()}, oppAutoPay: true, oppTricks: oppTricks}
	env, err := newEngineEnv(World{Engine: c, Observer: searchprobe.NewCollector(0)}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return env, st
}

// A walk bot's gated cast at an in-walk decision that is no point (neither
// priority nor a target a searched kind) in a closed window: the searching
// seat's and the
// mirrored opponent's are played as pass and counted (TricksWalk,
// TricksOpp); an unmirrored opponent's stands, uncounted -- its walk-bot
// answer reads oppTricksRule, never the seat's rule. The walk is cut after
// the first answer.
func TestTricksWalkBotCastAtANonPoint(t *testing.T) {
	ws := trickWindows(t)
	kinds := Kinds{Attackers: true, Blockers: true}
	for _, tc := range []struct {
		name      string
		w         int
		oppTricks bool
		passed    bool
	}{
		{"seat", twBotCast, false, true},
		{"opponent, unmirrored", twOppBotCast, false, false},
		{"opponent, mirrored", twOppBotCast, true, true},
	} {
		for _, on := range []bool{false, true} {
			e := ws[tc.w]
			p := e.Pending().Player
			var rule *trickRule
			if on {
				rule = newTrickRule("")
			}
			env, st := trickEnv(t, e, kinds, rule, tc.oppTricks, 1)
			var tricksInHand []state.ObjID
			for _, id := range env.e.G.Zone(state.ZHand, p) {
				if gatedTricks[cardName(env.e, id)] {
					tricksInHand = append(tricksInHand, id)
				}
			}
			if pt, err := env.advance(); err != nil || pt != nil || !env.capped {
				t.Fatalf("%s, on %v: point %v, err %v, capped %v; want the walk cut after one answer", tc.name, on, pt, err, env.capped)
			}
			kept := true
			for _, id := range tricksInHand {
				if o := env.e.G.Obj(id); o == nil || o.Zone != state.ZHand {
					kept = false
				}
			}
			want := on && tc.passed
			counted := st.TricksWalk + st.TricksOpp
			if kept != want || (counted > 0) != want || st.TricksWalk > 0 && p != 0 || st.TricksOpp > 0 && p == 0 {
				t.Fatalf("%s, on %v: tricks kept in hand %v, counted walk %d opponent %d; want passed and counted %v", tc.name, on, kept, st.TricksWalk, st.TricksOpp, want)
			}
		}
	}
}

// A dropped payment cast still introduces its card to the observer, as it
// does without the rule, so every later key is the one it would be.
func TestTricksObservesDroppedCasts(t *testing.T) {
	ws := trickWindows(t)
	for _, w := range []int{twMain, twNoOwn, twOwnSpell} {
		e := ws[w]
		d := e.Pending()
		offObs, onObs := searchprobe.NewCollector(0), searchprobe.NewCollector(0)
		enumerateCut(offObs, e, d, passOf(t, d), AllKinds(), BenchCandidateLimit, true)
		k := trickKinds(AllKinds(), "")
		enumerateCut(onObs, e, d, passOf(t, d), k, BenchCandidateLimit, true)
		if k.tricks.changed == 0 || onObs.Introduced() != offObs.Introduced() {
			t.Fatalf("%s: introduced %d with the rule (changed %d), %d without", trickWindowNames[w], onObs.Introduced(), k.tricks.changed, offObs.Introduced())
		}
	}
}

// firstKept: the answer without its dropped options when the decision
// accepts it; else the first non-empty set of kept options; else the empty
// answer when accepted; else none.
func TestTricksFirstKept(t *testing.T) {
	opts := func(n int) []decision.Option {
		var out []decision.Option
		for i := 0; i < n; i++ {
			out = append(out, decision.Option{Index: i, Kind: "permanent", Obj: state.ObjID(10 + i)})
		}
		return out
	}
	in := func(c ...int) decision.Intent { return decision.Intent{Seq: 1, Choices: c} }
	for _, tc := range []struct {
		name     string
		min, max int
		drop     []bool
		bot      decision.Intent
		want     []int
		ok       bool
	}{
		{"two targets, one dropped", 1, 2, []bool{false, true, false}, in(2, 1), []int{2}, true},
		{"one target, dropped", 1, 1, []bool{true, false, false}, in(0), []int{1}, true},
		{"up to two, both dropped", 0, 2, []bool{true, true, false}, in(0, 1), []int{2}, true},
		{"exactly two, one kept", 2, 2, []bool{true, false, false}, in(0, 1), []int{1, 2}, true},
		{"up to one, all dropped", 0, 1, []bool{true, true, true}, in(1), nil, true},
		{"one target, all dropped", 1, 1, []bool{true, true, true}, in(1), []int{1}, false},
	} {
		d := &decision.Decision{Seq: 1, Kind: decision.KTarget, Min: tc.min, Max: tc.max, Options: opts(len(tc.drop))}
		got, ok := firstKept(d, tc.bot, tc.drop)
		if ok != tc.ok || !reflect.DeepEqual(got.Choices, tc.want) && (len(got.Choices) != 0 || len(tc.want) != 0) {
			t.Fatalf("%s: %v %v, want %v %v", tc.name, got.Choices, ok, tc.want, tc.ok)
		}
	}
}

// The prior-only seat plays Search's answer: pass in place of its bot's
// gated cast.
func TestTricksPriorSeat(t *testing.T) {
	ws := trickWindows(t)
	for _, on := range []bool{false, true} {
		e := ws[twBotCast].Clone()
		d := e.Pending()
		cast := payCastOf(t, e, d, nGiantGrowth)
		sc := DefaultSeatConfig()
		sc.PriorOnly, sc.Source = true, testSeatSource
		sc.Search.AutoPayment, sc.Search.Tricks = true, on
		az, err := NewSeat(5, nil, sc)
		if err != nil {
			t.Fatal(err)
		}
		got, err := az.decidePrior(context.Background(), searchseat.Env{Engine: e}, *d, cast)
		if err != nil {
			t.Fatal(err)
		}
		want := cast
		if on {
			want = passOf(t, d)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("prior-only seat, tricks %v: played %+v, want %+v", on, got, want)
		}
	}
}

// creatures counts the creatures seat p controls.
func creatures(e *rules.Engine, p state.PlayerID) int {
	n := 0
	for _, id := range e.G.Zone(state.ZBattlefield, p) {
		if o := e.G.Obj(id); o != nil && o.Controller == p && e.IsCreature(id) {
			n++
		}
	}
	return n
}

// What the rule reads off a card's script (classify), for FDN's tricks and
// counterspells and a few test cards: listed (any face's name), a Charm cast
// freely, a counterspell (SP$ Counter, TargetType$ Spell exactly), an
// evasion grant, a targeted spell.
func TestTricksClassify(t *testing.T) {
	const (
		scatter  = "Name:Essence Scatter\nManaCost:1 U\nTypes:Instant\nA:SP$ Counter | TargetType$ Spell | TgtPrompt$ Select target creature spell | ValidTgts$ Creature | SpellDescription$ Counter target creature spell.\nOracle:Counter target creature spell.\n"
		offer    = "Name:An Offer You Can't Refuse\nManaCost:U\nTypes:Instant\nA:SP$ Counter | TargetType$ Spell | ValidTgts$ Card.nonCreature | TgtPrompt$ Select target noncreature spell | SubAbility$ DBToken | SpellDescription$ Counter target noncreature spell.\nSVar:DBToken:DB$ Token | TokenAmount$ 2 | TokenScript$ c_a_treasure_sac | TokenOwner$ TargetedController\nOracle:x\n"
		stifle   = "Name:Test Stifle\nManaCost:U\nTypes:Instant\nA:SP$ Counter | TargetType$ Activated,Triggered | TgtPrompt$ Select target ability | ValidTgts$ Card | SpellDescription$ Counter target activated or triggered ability.\nOracle:x\n"
		sure     = "Name:Sure Strike\nManaCost:1 R\nTypes:Instant\nA:SP$ Pump | ValidTgts$ Creature | NumAtt$ +3 | KW$ First Strike | SpellDescription$ Target creature gets +3/+0 and gains first strike until end of turn.\nOracle:x\n"
		bulk     = "Name:Bulk Up\nManaCost:1 R\nTypes:Instant\nA:SP$ Pump | ValidTgts$ Creature | NumAtt$ Double | SpellDescription$ Double target creature's power until end of turn.\nK:Flashback:4 R R\nOracle:x\n"
		claws    = "Name:Claws Out\nManaCost:3 W W\nTypes:Instant\nK:Affinity:Cat\nA:SP$ PumpAll | ValidCards$ Creature.YouCtrl | NumAtt$ +2 | NumDef$ +2 | SpellDescription$ Creatures you control get +2/+2 until end of turn.\nOracle:x\n"
		resil    = "Name:Divine Resilience\nManaCost:W\nTypes:Instant\nK:Kicker:2 W\nA:SP$ Pump | ValidTgts$ Creature.YouCtrl | TargetMin$ X | TargetMax$ Y | KW$ Indestructible | SpellDescription$ Target creature you control gains indestructible until end of turn.\nSVar:X:Count$Kicked.0.1\nSVar:Y:Count$Kicked.Z.1\nSVar:Z:Count$Valid Creature.YouCtrl\nOracle:x\n"
		armor    = "Name:Celestial Armor\nManaCost:2 W\nTypes:Artifact Equipment\nK:Flash\nT:Mode$ ChangesZone | Origin$ Any | Destination$ Battlefield | ValidCard$ Card.Self | Execute$ TrigAttach | TriggerDescription$ When this Equipment enters, attach it to target creature you control.\nSVar:TrigAttach:DB$ Attach | ValidTgts$ Creature.YouCtrl | TgtPrompt$ Select target creature you control | SubAbility$ DBPump\nSVar:DBPump:DB$ Pump | Defined$ Targeted | KW$ Hexproof & Indestructible\nS:Mode$ Continuous | AffectedDefined$ Equipped | Affected$ Creature | AddPower$ 2 | AddKeyword$ Flying | Description$ Equipped creature gets +2/+0 and has flying.\nK:Equip:3 W\nOracle:x\n"
		sneak    = "Name:Test Sneak\nManaCost:U\nTypes:Instant\nA:SP$ Pump | ValidTgts$ Creature | KW$ HIDDEN Unblockable | SpellDescription$ Target creature can't be blocked this turn.\nOracle:x\n"
		menace   = "Name:Test Menace\nManaCost:R\nTypes:Instant\nA:SP$ Pump | ValidTgts$ Creature | NumAtt$ +1 | KW$ Menace | SpellDescription$ Target creature gets +1/+0 and gains menace.\nOracle:x\n"
		twoFaces = "Name:Test Squire\nManaCost:1 W\nTypes:Creature Human\nPT:2/2\nAlternateMode:Adventure\nOracle:x\n\nALTERNATE\n\nName:Test Errand\nManaCost:W\nTypes:Instant Adventure\nA:SP$ Pump | ValidTgts$ Creature | NumAtt$ +1 | NumDef$ +1 | SpellDescription$ Target creature gets +1/+1.\nOracle:x\n"
	)
	r := newTrickRule(DefaultTricks + "|Test Sneak|Test Menace|Test Errand")
	for _, tc := range []struct {
		script string
		want   trickFace
	}{
		{scriptGiantGrowth, trickFace{gated: true, targeted: true}},
		{sure, trickFace{gated: true, targeted: true}},
		{bulk, trickFace{gated: true, targeted: true}},
		{claws, trickFace{gated: true}},
		{scriptGoblinSurp, trickFace{gated: true, modal: true}},
		{scriptSnakeskinVeil, trickFace{gated: true, targeted: true}},
		{resil, trickFace{gated: true, targeted: true}},
		{scriptFakeDeath, trickFace{gated: true, targeted: true}},
		{armor, trickFace{gated: true}},
		{scriptFlight, trickFace{gated: true, targeted: true, evasion: true}},
		{scriptRefute, trickFace{counter: true, targeted: true}},
		{scatter, trickFace{counter: true, targeted: true}},
		{offer, trickFace{counter: true, targeted: true}},
		{stifle, trickFace{targeted: true}},
		{scriptBear, trickFace{}},
		{sneak, trickFace{gated: true, targeted: true, evasion: true}},
		{menace, trickFace{gated: true, targeted: true, evasion: true}},
		{twoFaces, trickFace{gated: true}},
	} {
		c := payWindowCard(t, tc.script)
		got := r.classify(&state.Object{Card: c})
		if got != tc.want {
			t.Fatalf("%s: %+v, want %+v", c.Faces[0].Name, got, tc.want)
		}
	}
}

// At a priority decision the gate drops a "cast" option of a gated card
// outside the windows and no other option -- a special action (Foretell) of
// the same card stays -- and a root macro whose step casts one, by payment
// or by a pick that is no special action; inside a window it drops none.
func TestTricksDropsOptionsAndMacros(t *testing.T) {
	ws := trickWindows(t)
	for _, w := range []int{twMain, twAttack} {
		e := ws[w]
		d := e.Pending()
		var gg, bear state.ObjID
		for _, id := range e.G.Zone(state.ZHand, 0) {
			if cardName(e, id) == nGiantGrowth {
				gg = id
			}
		}
		for _, id := range e.G.Zone(state.ZBattlefield, 0) {
			if n := cardName(e, id); n == nBear || n == nDuelist {
				bear = id
			}
		}
		if gg == 0 || bear == 0 {
			t.Fatalf("%s: Giant Growth %d in hand, a creature %d", trickWindowNames[w], gg, bear)
		}
		closed := w == twMain
		r := newTrickRule("")
		g := r.gate(e, d)
		for _, tc := range []struct {
			o    decision.Option
			drop bool
		}{
			{decision.Option{Kind: "cast", Obj: gg}, closed},
			{decision.Option{Kind: "cast", Obj: gg, Mode: "flashback"}, closed},
			{decision.Option{Kind: "cast", Obj: gg, Mode: "foretell"}, false},
			{decision.Option{Kind: "ability", Obj: gg}, false},
			{decision.Option{Kind: "cast", Obj: bear}, false},
		} {
			if got := g.dropsOption(tc.o); got != tc.drop {
				t.Fatalf("%s: option %+v dropped %v, want %v", trickWindowNames[w], tc.o, got, tc.drop)
			}
		}
		step := func(pk rules.ScriptPick) *Macro {
			return &Macro{Key: MacroKeyPrefix + "m", Steps: []MacroStep{{Player: 0, Kind: decision.KPriority, Picks: []rules.ScriptPick{pk}}}}
		}
		for _, tc := range []struct {
			m    *Macro
			drop bool
		}{
			{step(rules.ScriptPick{Kind: "cast", Obj: gg, Label: "Cast Giant Growth"}), closed},
			{step(rules.ScriptPick{Kind: "cast", Obj: gg, Label: "Foretell Giant Growth"}), false},
			{step(rules.ScriptPick{Kind: "cast", Obj: bear, Label: "Cast Test Bear"}), false},
			{&Macro{Key: MacroKeyPrefix + "p", Steps: []MacroStep{{Player: 0, Kind: decision.KPriority, PayCast: gg}}}, closed},
			{&Macro{Key: MacroKeyPrefix + "p", Steps: []MacroStep{{Player: 0, Kind: decision.KPriority, PayCast: bear}}}, false},
		} {
			if got := g.dropsMacro(tc.m); got != tc.drop {
				t.Fatalf("%s: macro %+v dropped %v, want %v", trickWindowNames[w], tc.m.Steps, got, tc.drop)
			}
		}
	}
}

// A target decision for a gated Charm drops the opponent's targets only
// under a trick effect (d.TargetEffect: a pump yes, damage no); any target
// decision drops nothing when its spell is not the deciding seat's (an
// opponent choosing) or not a gated card's. Synthetic decisions over a real
// board: Goblin Surprise (in hand) as the source, every creature a target.
func TestTricksCharmAndChooserTargets(t *testing.T) {
	e := trickWindows(t)[twAttack]
	var gs, bear state.ObjID
	for _, id := range e.G.Zone(state.ZHand, 0) {
		switch cardName(e, id) {
		case nGoblinSurp:
			gs = id
		case nBear, nDuelist:
			bear = id
		}
	}
	if gs == 0 || e.G.Obj(gs).Controller != 0 {
		t.Fatal("no Goblin Surprise of seat 0's in hand")
	}
	target := func(src state.ObjID, player state.PlayerID, api string) *decision.Decision {
		d := &decision.Decision{Seq: 1, Player: player, Kind: decision.KTarget, Min: 1, Max: 1, Source: src, TargetEffect: &decision.TargetEffect{API: api}}
		for p := range e.G.Players {
			for _, id := range e.G.Zone(state.ZBattlefield, state.PlayerID(p)) {
				if e.IsCreature(id) {
					d.Options = append(d.Options, decision.Option{Index: len(d.Options), Kind: "permanent", Obj: id, Player: state.PlayerID(p)})
				}
			}
		}
		return d
	}
	pump := target(gs, 0, "Pump")
	want := sideOf(e, pump, 0)
	for i := range want {
		want[i] = !want[i]
	}
	// Giant Growth's (a gated card's, no Charm) over the two players: the
	// opponent is dropped.
	var gg state.ObjID
	for _, id := range e.G.Zone(state.ZHand, 0) {
		if cardName(e, id) == nGiantGrowth {
			gg = id
		}
	}
	players := &decision.Decision{Seq: 1, Player: 0, Kind: decision.KTarget, Min: 1, Max: 1, Source: gg, TargetEffect: &decision.TargetEffect{API: "Pump"},
		Options: []decision.Option{{Index: 0, Kind: "player", Player: 0}, {Index: 1, Kind: "player", Player: 1}}}
	for _, tc := range []struct {
		name string
		d    *decision.Decision
		want []bool
	}{
		{"a pump mode", pump, want},
		{"the players", players, []bool{false, true}},
		{"a damage mode", target(gs, 0, "DealDamage"), nil},
		{"the opponent choosing", target(gs, 1, "Pump"), nil},
		{"a creature spell", target(bear, 0, "Pump"), nil},
	} {
		g := newTrickRule("").gate(e, tc.d)
		if got := g.optionDrops(); !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%s: drops %v, want %v", tc.name, got, tc.want)
		}
	}
}

// An opponent's point (OpponentNodes) in a closed window gates its casts
// only under oppTricks (OpponentMirror with Tricks): exactly its unruled
// candidates minus the gated casts. The rule on the walk's kinds alone
// leaves them as they were.
func TestTricksAtOpponentPointsOnlyMirrored(t *testing.T) {
	e := trickWindows(t)[twOppBotCast]
	d := e.Pending()
	point := func(oppTricks bool) []cand {
		var st Stats
		kinds := trickKinds(AllKinds(), "")
		cfg := &walkConfig{kinds: kinds, limit: BenchCandidateLimit, actor: 0, stats: &st, oppNodes: true,
			oppForks: []*searchprobe.Forker{nil, searchprobe.NewCollector(1).Forker()}, oppAutoPay: true, oppTricks: oppTricks}
		env := &engineEnv{e: e, cfg: cfg}
		if pt := env.oppPoint(d, passOf(t, d)); pt == nil {
			return nil
		}
		return env.cands
	}
	off, on := point(false), point(true)
	var want []Key
	gated := false
	for _, c := range off {
		if gatedTricks[castName(e, d, c.in)] {
			gated = true
			continue
		}
		want = append(want, c.key)
	}
	if len(want) < 2 {
		want = nil // no point left: the opponent's walk bot answers
	}
	got := keysOf(on)
	if len(got) == 0 {
		got = nil
	}
	if !gated || off == nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("seat 1's closed window: %v unmirrored, %v mirrored, want %v (gated cast offered %v)", keysOf(off), keysOf(on), want, gated)
	}
}

// The cast-time side check (ownTarget) counts a player target on the
// caster's side, the caster, as an own target, and an untargeted card needs
// a creature of the caster's. Seat 0 controls no creature at twNoOwn and
// several at twAttack.
func TestTricksOwnTargetPlayers(t *testing.T) {
	ws := trickWindows(t)
	boon := payWindowCard(t, "Name:Test Boon\nManaCost:W\nTypes:Instant\nA:SP$ GainLife | ValidTgts$ Player | LifeAmount$ 2 | SpellDescription$ Target player gains 2 life.\nOracle:x\n")
	bane := payWindowCard(t, "Name:Test Bane\nManaCost:W\nTypes:Instant\nA:SP$ GainLife | ValidTgts$ Opponent | LifeAmount$ 2 | SpellDescription$ Target opponent gains 2 life.\nOracle:x\n")
	for _, tc := range []struct {
		w                  int
		boon, bane, untgtd bool
	}{{twNoOwn, true, false, false}, {twAttack, true, false, true}} {
		e := ws[tc.w]
		var src state.ObjID
		for _, id := range e.G.Zone(state.ZHand, 0) {
			if cardName(e, id) == nGiantGrowth {
				src = id
			}
		}
		if src == 0 {
			t.Fatalf("%s: no Giant Growth in hand", trickWindowNames[tc.w])
		}
		if got := ownTarget(e, 0, src, boon.Faces[0].SpellAbility(), true); got != tc.boon {
			t.Fatalf("%s: target player, own target %v", trickWindowNames[tc.w], got)
		}
		if got := ownTarget(e, 0, src, bane.Faces[0].SpellAbility(), true); got != tc.bane {
			t.Fatalf("%s: target opponent, own target %v", trickWindowNames[tc.w], got)
		}
		if got := ownTarget(e, 0, src, nil, false); got != tc.untgtd {
			t.Fatalf("%s: untargeted, own target %v", trickWindowNames[tc.w], got)
		}
	}
}
