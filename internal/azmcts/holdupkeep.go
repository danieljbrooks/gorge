package azmcts

import (
	"strings"

	"github.com/adams-shaun/gorge/decision"
	"github.com/adams-shaun/gorge/rules"
	"github.com/adams-shaun/gorge/state"
)

// holdUpkeep is Options.HoldUpkeep inside one Search (DraftZero): at a
// priority decision of the active player in its own upkeep or draw step with
// an empty stack (on), every candidate that casts a spell is dropped before
// the candidates are capped -- a payment cast, a legacy "cast" option (mana
// floating; an alternative-cost, flashback or other non-hand cast included)
// and a root macro with a cast step. No sorcery-speed cast is legal there, so
// each is an instant-speed spell's (an instant, a flash card, a card cast as
// though it had flash). Pass, land plays, activated abilities, the "cast"-kind
// special actions that cast nothing (specialCast: Foretell, Suspend, Plot) and
// everything else stay. A bot answer the rule holds is outside the held
// candidates, and pass is played in its place (heldAnswer). Search hangs the
// rule on its Kinds (Kinds.hold), so the root and the seat's in-walk points
// enumerate under it; an opponent's in-walk points do only under
// OpponentMirror (walkConfig.oppHold), each seat at its own upkeep and draw.
// nil is off.
type holdUpkeep struct {
	// exempt is Options.HoldUpkeepExempt: card names whose casts are never
	// held (any face's name matches). Lookup only -- never ranged.
	exempt map[string]bool
	// dropped counts the decisions at which the rule changed something: an
	// enumeration that dropped at least one candidate, or a bot answer
	// replaced by pass in the walk. Search reads it after the root's
	// enumeration (Stats.HeldUpkeep), the walk around each of its own
	// (Stats.HeldUpkeepWalk, HeldUpkeepOpp).
	dropped int
}

// newHoldUpkeep is the rule with the given exempt card names, '|'-separated
// (Options.HoldUpkeepExempt; each trimmed of spaces, empty names ignored).
func newHoldUpkeep(exempt string) *holdUpkeep {
	h := &holdUpkeep{}
	for _, n := range strings.Split(exempt, "|") {
		if n = strings.TrimSpace(n); n == "" {
			continue
		}
		if h.exempt == nil {
			h.exempt = map[string]bool{}
		}
		h.exempt[n] = true
	}
	return h
}

// on reports whether the rule applies at decision d of engine e: a priority
// decision of the active player, in its upkeep or draw step, with nothing on
// the stack. False for a nil rule, engine or decision.
func (h *holdUpkeep) on(e *rules.Engine, d *decision.Decision) bool {
	if h == nil || e == nil || d == nil || d.Kind != decision.KPriority {
		return false
	}
	g := e.G
	return d.Player == g.Active && len(g.Stack) == 0 && (g.Step == state.StepUpkeep || g.Step == state.StepDraw)
}

// holds reports whether a cast of object id is held: always, unless the
// object's card is exempt (any of its faces' names is listed).
func (h *holdUpkeep) holds(e *rules.Engine, id state.ObjID) bool {
	if len(h.exempt) == 0 {
		return true
	}
	o := e.G.Obj(id)
	if o == nil || o.Card == nil {
		return true
	}
	for _, f := range o.Card.Faces {
		if f != nil && h.exempt[f.Name] {
			return false
		}
	}
	return true
}

// specialCasts are the special actions (CR 116.2) the rules engine offers
// under the "cast" option Kind although they cast no spell (rules/legal.go's
// split-second filter keeps them for that reason): Foretell (pay {2}, exile
// the card face down; any step of its owner's turn, so an upkeep too),
// Suspend (pay, exile it with time counters; an instant's at instant speed)
// and Plot (sorcery speed only, so never in the rule's window). The engine
// labels each "<Action> <card name>" (rules/legal_walk_hand.go,
// legal_plotzone_walk.go), where a spell cast's label starts "Cast ". The
// later casts of such a card (Modes foretell_cast, suspend_cast, plot_cast)
// are spells and are held.
var specialCasts = [...]struct{ mode, label string }{
	{"foretell", "Foretell "}, {"suspend", "Suspend "}, {"plot", "Plot "},
}

// specialCast reports whether a "cast" option of Mode mode is a special
// action, not a spell (specialCasts).
func specialCast(mode string) bool {
	for _, s := range specialCasts {
		if mode == s.mode {
			return true
		}
	}
	return false
}

// specialCastPick reports whether a macro step's "cast" pick names a special
// action: a rules.ScriptPick carries no Mode, so its label is read
// (specialCasts).
func specialCastPick(label string) bool {
	for _, s := range specialCasts {
		if strings.HasPrefix(label, s.label) {
			return true
		}
	}
	return false
}

// holdsOption reports whether option o of a decision the rule is on at is a
// held cast: a "cast" option that casts a spell (not a special action,
// specialCast) of a card not exempt.
func (h *holdUpkeep) holdsOption(e *rules.Engine, o decision.Option) bool {
	return o.Kind == "cast" && !specialCast(o.Mode) && h.holds(e, o.Obj)
}

// holdsMacro reports whether root macro m casts a held spell: a step that
// casts through a payment action, or that picks a "cast" option other than a
// special action (specialCastPick).
func (h *holdUpkeep) holdsMacro(e *rules.Engine, m *Macro) bool {
	for _, st := range m.Steps {
		if st.PayCast != 0 && h.holds(e, st.PayCast) {
			return true
		}
		for _, pk := range st.Picks {
			if pk.Kind == "cast" && !specialCastPick(pk.Label) && h.holds(e, pk.Obj) {
				return true
			}
		}
	}
	return false
}

// castsHeld reports whether answer in casts a held spell at decision d: a
// payment selection (decision.PaymentAction always casts) of an object not
// exempt -- an action d does not list counts as held -- or a choice of a
// held "cast" option (holdsOption).
func (h *holdUpkeep) castsHeld(e *rules.Engine, d *decision.Decision, in decision.Intent) bool {
	if in.Payment != nil {
		for i := range d.PaymentActions {
			if a := &d.PaymentActions[i]; a.ID == in.Payment.ActionID {
				return h.holds(e, a.Cast.Object)
			}
		}
		return true
	}
	for _, c := range in.Choices {
		if c >= 0 && c < len(d.Options) && h.holdsOption(e, d.Options[c]) {
			return true
		}
	}
	return false
}

// heldAnswer is the answer the rule plays in place of the bot's answer in at
// decision d of engine e: where the rule is on and in casts a held spell
// (castsHeld), in is outside the held candidates, so the decision is not
// searched and, instead of that cast, d's pass is played (ok true). Anywhere
// else, or with no pass offered, ok is false and in stands.
func (h *holdUpkeep) heldAnswer(e *rules.Engine, d *decision.Decision, in decision.Intent) (decision.Intent, bool) {
	if !h.on(e, d) || !h.castsHeld(e, d, in) {
		return in, false
	}
	for _, o := range d.Options {
		if o.Kind == "pass" {
			return decision.Intent{Seq: d.Seq, Player: d.Player, Choices: []int{o.Index}}, true
		}
	}
	return in, false
}

// count records that the rule changed a decision (holdUpkeep.dropped).
func (h *holdUpkeep) count() { h.dropped++ }

// drops is the count so far, 0 for a nil rule.
func (h *holdUpkeep) drops() int {
	if h == nil {
		return 0
	}
	return h.dropped
}
