package azmcts

import (
	"strings"

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
// and Celestial Armor, and the evasion spell Fleeting Flight.
const DefaultTricks = "Giant Growth|Sure Strike|Bulk Up|Claws Out|Goblin Surprise|Snakeskin Veil|Divine Resilience|" +
	"Fake Your Own Death|Celestial Armor|Fleeting Flight"

// trickRule is Options.Tricks inside one Search (DraftZero). It has two
// parts.
//
// The timing gate: a cast of a gated card (a listed name, any face) is a
// candidate only in a trick window: with something on the stack (the spell
// being cast aside), or in the declare-attackers, declare-blockers or
// combat-damage step once a creature is attacking. A card that grants an
// evasion keyword (Fleeting Flight's flying; grantsEvasion) also has its
// controller's own beginning of combat. A gated Charm with a mode that is
// not a trick (Goblin Surprise's tokens) is cast freely, and its trick modes
// (trickSA) are gated at its modes decision instead.
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
// the candidates, so the decision is not searched and answer plays in its
// place pass (priority) or the first set of kept options (a target or modes
// decision). Search hangs the rule on its Kinds (Kinds.tricks) as it does
// HoldUpkeep's; an opponent's in-walk points use it only under
// OpponentMirror (walkConfig.oppTricks). nil is off.
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
	gated    bool // a listed name (any of the card's faces)
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
	r *trickRule
	e *rules.Engine
	d *decision.Decision
	// exclude is the spell being cast at a modes or target decision (on
	// the stack already, CR 601.2a), which does not open the window.
	exclude      state.ObjID
	read         bool
	win, flight  bool
	gated, sided bool
}

// gate is the rule at decision d of engine e (the zero gate for a nil rule).
func (r *trickRule) gate(e *rules.Engine, d *decision.Decision) trickGate {
	if r == nil || e == nil || d == nil {
		return trickGate{}
	}
	g := trickGate{r: r, e: e, d: d}
	if d.Kind != decision.KPriority {
		g.exclude = d.Source
	}
	return g
}

// window reports whether the trick window is open at the gate's decision:
// something on the stack (the excluded spell aside), or an attacking
// creature in the declare-attackers, declare-blockers or combat-damage
// step; with evasion also the decider's own beginning of combat.
func (g *trickGate) window(evasion bool) bool {
	if !g.read {
		g.read = true
		gm := g.e.G
		for _, id := range gm.Stack {
			if id != g.exclude {
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

// answer is the answer the rule plays in place of the bot's answer in at
// decision d of engine e: where in is a cast, a target or a mode the rule
// drops, in is outside the candidates, so the decision is not searched and
// pass (a priority decision) or the first kept options (firstKept: a target
// or modes decision) are played instead (ok true). Anywhere else, or with no
// such answer, ok is false and in stands. It records nothing (count does).
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
