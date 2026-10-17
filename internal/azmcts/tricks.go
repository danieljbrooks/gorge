package azmcts

import (
	"strings"

	"github.com/adams-shaun/gorge/botpolicy"
	"github.com/adams-shaun/gorge/cards"
	"github.com/adams-shaun/gorge/decision"
	"github.com/adams-shaun/gorge/effects"
	"github.com/adams-shaun/gorge/rules"
	"github.com/adams-shaun/gorge/state"
)

// DefaultTricks is Options.Tricks' gated list when Options.TricksCards is
// empty: FDN's combat tricks (draft-zero-report docs/038, where none of 122
// was cast after attackers were declared). The pumps Giant Growth, Sure
// Strike, Bulk Up, Claws Out and Goblin Surprise (its pump mode only), the
// protection spells Snakeskin Veil, Divine Resilience, Fake Your Own Death
// and Celestial Armor, and the evasion spell Fleeting Flight. A card is
// matched by any of its faces' names (an adventurer by its adventure's).
const DefaultTricks = "Giant Growth|Sure Strike|Bulk Up|Claws Out|Goblin Surprise|Snakeskin Veil|Divine Resilience|" +
	"Fake Your Own Death|Celestial Armor|Fleeting Flight"

// trickRule is Options.Tricks inside one Search (DraftZero). It has two
// parts.
//
// The timing gate: a cast of a gated card (a card any of whose faces' names
// is listed) is a candidate only in a trick window: with an object an
// opponent controls on the stack (a spell or an ability; the caster's own
// never opens it, the spell being cast included), or in the
// declare-attackers, declare-blockers or combat-damage step once a creature
// is attacking. A card that grants an evasion keyword (Fleeting Flight's
// flying; grantsEvasion) also has its controller's own beginning of combat.
// A gated Charm with a mode that is not a trick (Goblin Surprise's tokens)
// is cast freely, and its trick modes (trickSA) are gated at its modes
// decision instead.
//
// The side checks: every gated card is beneficial to its target, so its cast
// is no candidate when no permanent of its caster's (or the caster) is a
// legal target -- for a card with no target of its own (a pump-all, an
// Equipment with flash), when the caster controls no creature -- and at its
// target decision the opponents' options are dropped when one of the
// caster's is offered. A counterspell (any card whose spell is SP$ Counter
// with TargetType$ Spell: Refute, Essence Scatter, An Offer You Can't
// Refuse, ...) is no candidate unless an opponent's spell is a legal target,
// and at its target decision the caster's own spells are dropped when an
// opponent's is offered. Counterspells are found by script, whatever the
// gated list.
//
// The candidates are filtered before they are capped, everywhere a
// candidate list is built: the priority vocabularies (payment casts, legacy
// and manual "cast" options), the full root's macros, and the target,
// multitarget and modes enumerations. A bot answer the rule drops is outside
// the candidates, so it is never searched. In its place (answer, Search,
// engineEnv.advance):
//
//   - at a priority root under AutoPayment (priority a searched kind),
//     outside HoldUpkeep's own upkeep and draw window, the root is searched
//     over what the rule leaves with no bot candidate (Root.NoBot, the
//     bot-tap reroute's full root), so the seat's other casts and abilities
//     are still weighed; with fewer than two candidates left the one left,
//     pass (the rule never drops it), is played;
//   - at a priority decision inside a simulation (the seat's, a mirrored
//     opponent's walk bot), outside HoldUpkeep's window, the walk bot is
//     asked again over what the rule leaves (keptPriority, reask);
//   - anywhere else, pass at a priority decision (a priority root without
//     AutoPayment or not searched, or inside HoldUpkeep's window) and the
//     first set of kept options at a target or modes decision (firstKept;
//     the bot is not asked again there).
//
// A seat's bot answer that taps mana by hand is no cast the rule drops, but
// it may be aimed at one: at a priority root where the rule drops a cast the
// seat asks its bot again over what the rule keeps, the dropped cards
// uncastable to it, and plays pass when that fails or the answer still taps
// with nothing left to cast (tapAnswer, Seat.DecideSearch). The walk's own
// taps are played as answered.
//
// Search hangs the rule on its Kinds (Kinds.tricks) as it does
// HoldUpkeep's; an opponent's in-walk points use it only under
// OpponentMirror (walkConfig.oppTricks). nil is off.
//
// The rule binds Search and the walk only. The seat's paths that never
// reach Search play the wrapped bot's answer unruled: DecideBoard (the
// driver's fallback once the observation feed has stopped),
// DecideRootRefused (an honest root that refused to build), and
// DecideSearch with Sims <= 0 or no engine. So does an unmirrored
// opponent's walk bot, and its points under OpponentNodes without
// OpponentMirror are enumerated with no rule.
type trickRule struct {
	// names is the gated list (Options.TricksCards, else DefaultTricks).
	// Lookup only -- never ranged.
	names map[string]bool
	// memo is each face's classification (classify). Lookup only.
	memo map[*cards.Face]trickFace
	// gatedN and sideN count the enumerations at which the timing gate, or
	// a side check, dropped a candidate; Search reads them after the root's
	// (Stats.TricksGated, TricksSide).
	gatedN, sideN int
	// changed counts the decisions the rule changed: an enumeration that
	// dropped a candidate, or a bot answer replaced (count). The walk reads
	// it around each of its decisions (Stats.TricksWalk, TricksOpp).
	changed int
}

// trickFace is what the rule knows about one card face.
type trickFace struct {
	gated    bool // a listed name: any of the card's faces' names
	modal    bool // a gated Charm with a mode that is no trick: its cast is free, its trick modes are gated
	counter  bool // its spell is SP$ Counter with TargetType$ Spell
	evasion  bool // its spell grants an evasion keyword: its controller's own beginning of combat too
	targeted bool // its spell targets (else the side check asks for a creature of the caster's)
}

// newTrickRule is the rule with the gated card names, '|'-separated
// (Options.TricksCards; each trimmed of spaces, empty names ignored), or
// DefaultTricks when names is empty.
func newTrickRule(names string) *trickRule {
	if strings.TrimSpace(names) == "" {
		names = DefaultTricks
	}
	r := &trickRule{names: map[string]bool{}, memo: map[*cards.Face]trickFace{}}
	for _, n := range strings.Split(names, "|") {
		if n = strings.TrimSpace(n); n != "" {
			r.names[n] = true
		}
	}
	return r
}

// trickAPIs are the effects of a trick mode (trickSA): pumps, +1/+1
// counters and protection on the caster's creatures.
var trickAPIs = [...]string{"Pump", "PumpAll", "PutCounter", "PutCounterAll", "Protection", "ProtectionAll"}

// trickAPI reports whether api names a trick effect (trickAPIs).
func trickAPI(api string) bool {
	for _, a := range trickAPIs {
		if api == a {
			return true
		}
	}
	return false
}

// trickSA reports whether sa is a trick effect (trickAPIs).
func trickSA(sa *cards.SA) bool { return sa != nil && trickAPI(sa.API) }

// evasionKeywords are the keywords grantsEvasion looks for.
var evasionKeywords = [...]string{"Flying", "Menace", "Shadow", "Horsemanship", "Fear", "Intimidate", "Skulk"}

// grantsEvasion reports whether sa or one of its sub-abilities grants an
// evasion keyword (KW$, '&'-separated), or "can't be blocked".
func grantsEvasion(sa *cards.SA) bool {
	for s, n := sa, 0; s != nil && n < 16; s, n = s.Sub, n+1 {
		for _, kw := range strings.Split(s.ParamStr(cards.PKKW), "&") {
			kw = strings.TrimSpace(kw)
			if strings.Contains(kw, "Unblockable") || strings.Contains(kw, "can't be blocked") {
				return true
			}
			for _, e := range evasionKeywords {
				if kw == e {
					return true
				}
			}
		}
	}
	return false
}

// classify is object o's face's trickFace, memoized per face.
func (r *trickRule) classify(o *state.Object) trickFace {
	f := o.Face()
	if f == nil {
		return trickFace{}
	}
	if tf, ok := r.memo[f]; ok {
		return tf
	}
	var tf trickFace
	if o.Card != nil {
		for _, x := range o.Card.Faces {
			if x != nil && r.names[x.Name] {
				tf.gated = true
			}
		}
	}
	if sa := f.SpellAbility(); sa != nil {
		tf.counter = sa.APIKind() == cards.APICounter && strings.TrimSpace(sa.ParamStr(cards.PKTargetType)) == "Spell"
		tf.evasion = grantsEvasion(sa)
		if sa.APIKind() == cards.APICharm {
			for _, m := range effects.CharmOf(sa).Modes {
				if !trickSA(cards.ResolveSVar(f.SVars, strings.TrimSpace(m))) {
					tf.modal = true
				}
			}
		} else {
			tf.targeted = effects.TargetsOf(sa).Targeted()
		}
	}
	r.memo[f] = tf
	return tf
}

// trickGate is the rule at one decision d of engine e: its window, read on
// first need, and what an enumeration there dropped. The zero value (a nil
// rule) drops nothing.
type trickGate struct {
	r            *trickRule
	e            *rules.Engine
	d            *decision.Decision
	read         bool
	win, flight  bool
	gated, sided bool
}

// gate is the rule at decision d of engine e (the zero gate for a nil rule).
func (r *trickRule) gate(e *rules.Engine, d *decision.Decision) trickGate {
	if r == nil || e == nil || d == nil {
		return trickGate{}
	}
	return trickGate{r: r, e: e, d: d}
}

// window reports whether the trick window is open at the gate's decision:
// an object on the stack that an opponent of the decider controls (a spell
// or an ability), or an attacking creature in the declare-attackers,
// declare-blockers or combat-damage step; with evasion also the decider's
// own beginning of combat. The decider's own stack objects never open it:
// its spells and abilities, and at a modes or target decision the spell it
// is casting (on the stack already, CR 601.2a, under its control).
func (g *trickGate) window(evasion bool) bool {
	if !g.read {
		g.read = true
		gm := g.e.G
		for _, id := range gm.Stack {
			if o := gm.Obj(id); o != nil && o.Controller != g.d.Player {
				g.win = true
				break
			}
		}
		if !g.win {
			switch gm.Step {
			case state.StepDeclareAttackers, state.StepDeclareBlockers, state.StepCombatDamage:
				g.win = anyAttacking(gm)
			case state.StepBeginCombat:
				g.flight = gm.Active == g.d.Player
			}
		}
	}
	return g.win || evasion && g.flight
}

// anyAttacking reports whether a creature on the battlefield is attacking.
func anyAttacking(gm *state.Game) bool {
	for p := range gm.Players {
		for _, id := range gm.Zone(state.ZBattlefield, state.PlayerID(p)) {
			if o := gm.Obj(id); o != nil && o.IsAttacking {
				return true
			}
		}
	}
	return false
}

// controlsCreature reports whether p controls a creature.
func controlsCreature(e *rules.Engine, p state.PlayerID) bool {
	for _, id := range e.G.Zone(state.ZBattlefield, p) {
		if o := e.G.Obj(id); o != nil && o.Controller == p && e.IsCreature(id) {
			return true
		}
	}
	return false
}

// ownTarget reports whether spell or mode sa of source, cast by p, has a
// legal target on p's side (a permanent p controls, or p); untargeted, it
// reports whether p controls a creature.
func ownTarget(e *rules.Engine, p state.PlayerID, source state.ObjID, sa *cards.SA, targeted bool) bool {
	if !targeted {
		return controlsCreature(e, p)
	}
	for _, t := range e.LegalTargets(p, source, sa) {
		if t.IsPlayer {
			if t.Player == p {
				return true
			}
			continue
		}
		if o := e.G.Obj(t.Obj); o != nil && o.Controller == p {
			return true
		}
	}
	return false
}

// oppSpellTarget reports whether counterspell source, cast by p, has an
// opponent's spell among its legal targets.
func oppSpellTarget(e *rules.Engine, p state.PlayerID, source state.ObjID, sa *cards.SA) bool {
	for _, t := range e.LegalTargets(p, source, sa) {
		if o := e.G.Obj(t.Obj); !t.IsPlayer && o != nil && o.Zone == state.ZStack && o.Controller != p {
			return true
		}
	}
	return false
}

// dropsCast reports whether the rule drops a cast of object id at the gate's
// (priority) decision, and marks why (gated: outside the window; sided: a
// side check).
func (g *trickGate) dropsCast(id state.ObjID) bool {
	if g.r == nil {
		return false
	}
	o := g.e.G.Obj(id)
	if o == nil || o.Face() == nil {
		return false
	}
	tf := g.r.classify(o)
	gated := tf.gated && !tf.modal
	if !gated && !tf.counter {
		return false
	}
	if gated && !g.window(tf.evasion) {
		g.gated = true
		return true
	}
	p := g.d.Player
	sa := o.Face().SpellAbility()
	switch {
	case tf.counter:
		if !oppSpellTarget(g.e, p, id, sa) {
			g.sided = true
			return true
		}
	case !ownTarget(g.e, p, id, sa, tf.targeted):
		g.sided = true
		return true
	}
	return false
}

// dropsOption reports whether the rule drops priority option o: a "cast"
// option that casts a spell (not a special action, specialCast) the gate
// drops.
func (g *trickGate) dropsOption(o decision.Option) bool {
	return g.r != nil && o.Kind == "cast" && !specialCast(o.Mode) && g.dropsCast(o.Obj)
}

// dropsMacro reports whether root macro m has a step that casts a spell the
// gate drops at the root decision: through a payment action, or by picking
// a "cast" option other than a special action (specialCastPick).
func (g *trickGate) dropsMacro(m *Macro) bool {
	if g.r == nil {
		return false
	}
	for _, st := range m.Steps {
		if st.PayCast != 0 && g.dropsCast(st.PayCast) {
			return true
		}
		for _, pk := range st.Picks {
			if pk.Kind == "cast" && !specialCastPick(pk.Label) && g.dropsCast(pk.Obj) {
				return true
			}
		}
	}
	return false
}

// optionDrops is, at a target or modes decision of a gated card's or a
// counterspell's spell (d.Source, the spell on the stack), which of its
// options the rule drops (by position; nil when none is, or anywhere else).
// It marks why. An ability's decisions are not the spell's (its Source is
// the ability object): a gated permanent's own abilities, Celestial Armor's
// attach trigger and equip, target the caster's creatures only.
//
//   - Target: the caster deciding for its own spell. A gated
//     card's (a Charm's only under a trick effect, d.TargetEffect) drops the
//     options on an opponent's side when one on the caster's is offered; a
//     counterspell's drops the caster's own spells when an opponent's is.
//   - Modes: a gated Charm's trick modes (trickSA), outside the window or
//     with no target on the caster's side.
func (g *trickGate) optionDrops() []bool {
	if g.r == nil || (g.d.Kind != decision.KTarget && g.d.Kind != decision.KModes) || g.d.Source == 0 {
		return nil
	}
	d, e := g.d, g.e
	src := e.G.Obj(d.Source)
	if src == nil || src.Face() == nil || src.Controller != d.Player {
		return nil
	}
	tf := g.r.classify(src)
	var drop []bool
	mark := func(i int) {
		if drop == nil {
			drop = make([]bool, len(d.Options))
		}
		drop[i] = true
	}
	switch d.Kind {
	case decision.KTarget:
		charm := false
		if sa := src.Face().SpellAbility(); sa != nil && sa.APIKind() == cards.APICharm {
			charm = true
		}
		beneficial := tf.gated && (!charm || d.TargetEffect != nil && trickAPI(d.TargetEffect.API))
		if !beneficial && !tf.counter {
			return nil
		}
		own := make([]bool, len(d.Options))
		anyOwn, anyOpp := false, false
		for i, o := range d.Options {
			if o.Kind == "player" {
				own[i] = o.Player == d.Player
			} else if x := e.G.Obj(o.Obj); x != nil {
				own[i] = x.Controller == d.Player
			}
			anyOwn = anyOwn || own[i]
			anyOpp = anyOpp || !own[i]
		}
		for i := range d.Options {
			switch {
			case tf.counter && anyOpp && own[i]:
				mark(i)
			case !tf.counter && beneficial && anyOwn && !own[i]:
				mark(i)
			}
		}
		if drop != nil {
			g.sided = true
		}
	case decision.KModes:
		f := src.Face()
		sa := f.SpellAbility()
		if !tf.gated || sa == nil || sa.APIKind() != cards.APICharm {
			return nil
		}
		names := d.ResumeModes
		if len(names) != len(d.Options) {
			names = effects.CharmOf(sa).Modes
		}
		for i, o := range d.Options {
			if o.Index < 0 || o.Index >= len(names) {
				continue
			}
			sub := cards.ResolveSVar(f.SVars, strings.TrimSpace(names[o.Index]))
			if !trickSA(sub) {
				continue
			}
			if !g.window(grantsEvasion(sub)) {
				g.gated = true
				mark(i)
			} else if !ownTarget(e, d.Player, d.Source, sub, effects.TargetsOf(sub).Targeted()) {
				g.sided = true
				mark(i)
			}
		}
	}
	return drop
}

// finish records what the gate's enumeration dropped (trickRule.gatedN,
// sideN, changed).
func (g *trickGate) finish() {
	if g.r == nil || !g.gated && !g.sided {
		return
	}
	if g.gated {
		g.r.gatedN++
	}
	if g.sided {
		g.r.sideN++
	}
	g.r.changed++
}

// picksDropped reports whether answer in picks an option drop marks.
func picksDropped(in decision.Intent, drop []bool) bool {
	for _, c := range in.Choices {
		if c >= 0 && c < len(drop) && drop[c] {
			return true
		}
	}
	return false
}

// keptOnly is ins without the answers that pick a dropped option, capped at
// limit.
func keptOnly(ins []decision.Intent, drop []bool, limit int) []decision.Intent {
	out := ins[:0:0]
	for _, in := range ins {
		if len(out) >= limit {
			break
		}
		if !picksDropped(in, drop) {
			out = append(out, in)
		}
	}
	return out
}

// firstKept is the answer the rule plays at d in place of answer in, which
// picks a dropped option: in without its dropped options when d accepts
// that, else the first non-empty set of kept options d accepts, smallest
// first and lexicographic in option position (at most subsetTries
// examined), else the empty answer when d accepts it; ok false when there
// is none.
func firstKept(d *decision.Decision, in decision.Intent, drop []bool) (decision.Intent, bool) {
	var rest []int
	for _, c := range in.Choices {
		if c < 0 || c >= len(drop) || !drop[c] {
			rest = append(rest, c)
		}
	}
	try := decision.Intent{Seq: d.Seq, Player: d.Player, Choices: rest}
	if len(rest) > 0 && d.Validate(try) == nil {
		return try, true
	}
	var kept []int
	for i, o := range d.Options {
		if !drop[i] {
			kept = append(kept, o.Index)
		}
	}
	n := len(kept)
	lo, hi := max(d.Min, 0), min(d.Max, n)
	if d.AllowNone {
		lo = 0
	}
	comb := make([]int, 0, max(hi, 0))
	tries := 0
	for k := max(lo, 1); k <= hi && tries < subsetTries; k++ {
		comb = comb[:0]
		for i := 0; i < k; i++ {
			comb = append(comb, i)
		}
		for tries < subsetTries {
			tries++
			choices := make([]int, k)
			for i, p := range comb {
				choices[i] = kept[p]
			}
			if c := (decision.Intent{Seq: d.Seq, Player: d.Player, Choices: choices}); d.Validate(c) == nil {
				return c, true
			}
			i := k - 1
			for i >= 0 && comb[i] == n-k+i {
				i--
			}
			if i < 0 {
				break
			}
			comb[i]++
			for j := i + 1; j < k; j++ {
				comb[j] = comb[j-1] + 1
			}
		}
	}
	if none := (decision.Intent{Seq: d.Seq, Player: d.Player}); lo == 0 && d.Validate(none) == nil {
		return none, true
	}
	return in, false
}

// answer is the rule's answer in place of the bot's answer in at decision d
// of engine e: where in is a cast, a target or a mode the rule drops, in is
// outside the candidates, so it is never searched, and ok is true with pass
// (a priority decision) or the first kept options (firstKept: a target or
// modes decision). Anywhere else, or with no such answer (no pass offered),
// ok is false and in stands. It records nothing (count does). At a priority
// decision pass is the fallback: outside HoldUpkeep's window Search reroots
// the root over the vocabulary the rule leaves, and the walk asks its bot
// again (reask).
func (r *trickRule) answer(e *rules.Engine, d *decision.Decision, in decision.Intent) (decision.Intent, bool) {
	if r == nil || e == nil || d == nil {
		return in, false
	}
	g := r.gate(e, d)
	switch d.Kind {
	case decision.KPriority:
		dropped := false
		if in.Payment != nil {
			for i := range d.PaymentActions {
				if a := &d.PaymentActions[i]; a.ID == in.Payment.ActionID {
					dropped = g.dropsCast(a.Cast.Object)
					break
				}
			}
		}
		for _, c := range in.Choices {
			if c >= 0 && c < len(d.Options) && g.dropsOption(d.Options[c]) {
				dropped = true
			}
		}
		if !dropped {
			return in, false
		}
		if pass, ok := passAnswer(d); ok {
			return pass, true
		}
	case decision.KTarget, decision.KModes:
		if drop := g.optionDrops(); drop != nil && picksDropped(in, drop) {
			return firstKept(d, in, drop)
		}
	}
	return in, false
}

// passAnswer is d's pass, false when d offers none.
func passAnswer(d *decision.Decision) (decision.Intent, bool) {
	for _, o := range d.Options {
		if o.Kind == "pass" {
			return decision.Intent{Seq: d.Seq, Player: d.Player, Choices: []int{o.Index}}, true
		}
	}
	return decision.Intent{}, false
}

// keptPriority is priority decision d of engine e as the rule leaves it, to
// ask a walk bot again over (engineEnv.reask): a copy of d without the
// "cast" options dropsOption drops -- the others renumbered in order, back
// mapping each new index to d's own (the bot answers by index) -- and
// without the payment actions whose casts dropsCast drops. dropped lists the
// objects whose casts went (once per option or action). ok is false, and d
// should be answered as before, when the rule drops nothing at d or d is no
// priority decision.
func (r *trickRule) keptPriority(e *rules.Engine, d *decision.Decision) (kept decision.Decision, back []int, dropped []state.ObjID, ok bool) {
	g := r.gate(e, d)
	if g.r == nil || d.Kind != decision.KPriority {
		return decision.Decision{}, nil, nil, false
	}
	kept = *d
	kept.Options = make([]decision.Option, 0, len(d.Options))
	kept.PaymentActions = nil
	for _, o := range d.Options {
		if g.dropsOption(o) {
			dropped = append(dropped, o.Obj)
			continue
		}
		back = append(back, o.Index)
		o.Index = len(kept.Options)
		kept.Options = append(kept.Options, o)
	}
	for _, a := range d.PaymentActions {
		if g.dropsCast(a.Cast.Object) {
			dropped = append(dropped, a.Cast.Object)
			continue
		}
		kept.PaymentActions = append(kept.PaymentActions, a)
	}
	return kept, back, dropped, len(dropped) > 0
}

// uncastable marks the cards ids uncastable on board b (Card.Castable), so a
// bot asked over b neither casts them nor taps mana toward them (the tap
// gate prices castable cards only). b's tables are the caller's, refilled
// before its next bot answer, so the marks die with that answer.
func uncastable(b botpolicy.Board, ids []state.ObjID) {
	for _, id := range ids {
		if c := b.Cards.Ref(id); c != nil {
			c.Castable = false
		}
	}
}

// mapBack is answer in, given over keptPriority's decision, mapped back onto
// the decision's own option indices (back); ok is false when it picks an
// index the kept decision does not have. A payment answer names one of the
// decision's own payment actions already (the kept ones keep their IDs).
func mapBack(in decision.Intent, back []int) (decision.Intent, bool) {
	if len(in.Choices) == 0 {
		return in, true
	}
	choices := make([]int, len(in.Choices))
	for i, c := range in.Choices {
		if c < 0 || c >= len(back) {
			return in, false
		}
		choices[i] = back[c]
	}
	in.Choices = choices
	return in, true
}

// castLeft reports whether kept, keptPriority's decision at priority decision
// d of engine e, may still let the seat cast a spell its mana could pay: a
// payment action (planned or not) or a "cast" option that casts a spell (not
// a special action) is kept, or d's payment census may be incomplete. Built
// from an empty pool, d's payment actions are every spell the seat can cast
// now, legal targets included; with mana floating the builder may decline
// to build them, and an unbuilt list (a seat that builds no plans) has none,
// so either answers true.
func castLeft(e *rules.Engine, d *decision.Decision, kept *decision.Decision) bool {
	if !d.PaymentActionsBuilt || len(kept.PaymentActions) > 0 || e.G.Players[d.Player].Pool.Total() > 0 {
		return true
	}
	for _, o := range kept.Options {
		if o.Kind == "cast" && !specialCast(o.Mode) {
			return true
		}
	}
	return false
}

// tapAnswer is the rule at a priority root d of engine e whose bot answered
// in, a mana activation (priorityBase BaseActivate: the auto-pay bot's
// manual tap gate, which taps only in a main phase, so never inside
// HoldUpkeep's window): where the rule drops a cast at d (keptPriority), the
// tap may be aimed at that cast, which is never played, so its mana would go
// to nothing and the seat's lands would be tapped through its windows. The
// bot the tap came from (ask) is asked again, as the walk asks its walk bot
// (engineEnv.reask): over the decision the rule keeps, on a board built from
// e with the dropped cards uncastable, its answer mapped back onto d. Pass is
// played in its place when that fails (ask's error, an index the kept
// decision does not have), or when the answer is still a mana activation and
// no cast is left (castLeft): its mana could then pay only a cast the rule
// drops, or none (the tap gate also taps toward a card the engine offers no
// cast of, one with no legal target). ok is false, and in stands, when in is
// no mana activation, d no priority decision, the rule drops nothing at d, or
// d offers no pass. The answer may be in itself (a tap aimed at a kept
// spell), and it records nothing.
func (r *trickRule) tapAnswer(e *rules.Engine, d *decision.Decision, in decision.Intent,
	ask func(botpolicy.Board, decision.Decision) (decision.Intent, error)) (decision.Intent, bool) {
	if r == nil || e == nil || d == nil || d.Kind != decision.KPriority || priorityBase(d, in) != BaseActivate {
		return in, false
	}
	kept, back, dropped, ok := r.keptPriority(e, d)
	if !ok {
		return in, false
	}
	b := botpolicy.BoardFromGame(e.G, e, d.Player)
	uncastable(b, dropped)
	again, err := ask(b, kept)
	if err == nil {
		again, ok = mapBack(again, back)
	}
	if err != nil || !ok || priorityBase(d, again) == BaseActivate && !castLeft(e, d, &kept) {
		if pass, ok := passAnswer(d); ok {
			return pass, true
		}
		return in, false
	}
	return again, true
}

// count records that the rule changed a decision (trickRule.changed).
func (r *trickRule) count() {
	if r != nil {
		r.changed++
	}
}

// drops is the count of changed decisions so far, 0 for a nil rule.
func (r *trickRule) drops() int {
	if r == nil {
		return 0
	}
	return r.changed
}
