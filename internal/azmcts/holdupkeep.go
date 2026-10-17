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
// though it had flash). Pass, land plays, activated abilities and everything
// else stay. Search hangs it on its Kinds (Kinds.hold), so the root and the
// seat's in-walk points enumerate under it; an opponent's in-walk points do
// only under OpponentMirror (walkConfig.oppHold), each seat at its own
// upkeep and draw. nil is off.
type holdUpkeep struct {
	// exempt is Options.HoldUpkeepExempt: card names whose casts are never
	// held (any face's name matches). Lookup only -- never ranged.
	exempt map[string]bool
	// dropped counts the enumerations at which the rule dropped at least one
	// candidate; Search reads it after the root's (Stats.HeldUpkeep).
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

// holdsOption reports whether option o of a decision the rule is on at is a
// held cast.
func (h *holdUpkeep) holdsOption(e *rules.Engine, o decision.Option) bool {
	return o.Kind == "cast" && h.holds(e, o.Obj)
}

// holdsMacro reports whether root macro m casts a held spell: a step that
// casts through a payment action, or that picks a "cast" option.
func (h *holdUpkeep) holdsMacro(e *rules.Engine, m *Macro) bool {
	for _, st := range m.Steps {
		if st.PayCast != 0 && h.holds(e, st.PayCast) {
			return true
		}
		for _, pk := range st.Picks {
			if pk.Kind == "cast" && h.holds(e, pk.Obj) {
				return true
			}
		}
	}
	return false
}

// count records that an enumeration dropped a candidate.
func (h *holdUpkeep) count() { h.dropped++ }
