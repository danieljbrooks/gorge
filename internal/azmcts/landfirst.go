package azmcts

import "github.com/adams-shaun/gorge/decision"

// The land drop before a reroot (DraftZero).
//
// The auto-payment vocabulary never offers a land play (paymentPriorityCands:
// a bot answer that plays a land is outside it, so the root is skipped and
// the bot's land drop played), and neither does a full root (rootCands):
// land drops are the bot's, everywhere. But the auto-pay bot's main-phase
// policy runs its tap gate before its land drop (botpolicy's decide: the tap
// gate, then chooseLand, then chooseCast), so where it taps by hand -- a card
// castable by zone that no payment plan pays, such as an instant with no
// legal target -- its answer is the tap, with the land drop still to come.
// The bot-tap backstop (Seat.DecideSearch, Root.NoBot) then searched a
// vocabulary with no land play, and wherever it chose pass the seat's main
// phase ended without its land drop (the verifier's pair 509: seat 1 passed
// with Forests in hand in its own main phases of turns 10 and 12).
//
// So a root rerooted for want of a bot candidate asks the bot again with its
// mana activations removed -- the private decision the auto-pay bot itself
// ranks its plan casts over (seat.Bot's paymentIntent) -- and plays that
// answer when it is a land play, as the bot would have before its taps. The
// reroot then comes at the next decision, the land drop made.

// landFirst replaces a reroot by the bot's land drop: where r is rerooted
// for want of a bot candidate (NoBot alone, no Macros or BotKey) at a
// priority root under auto-payment with priority searched, and the bot
// (BotAsk) has a land drop there (botLandDrop), Bot becomes that land play
// and NoBot is cleared, so the root is skipped and the land played. It
// reports whether it did.
func (r *Root) landFirst(autoPayment bool, kinds Kinds) bool {
	if !r.NoBot || r.BotAsk == nil || len(r.Macros) > 0 || r.BotKey != "" || !autoPayment || !kinds.Priority ||
		r.Decision == nil || r.Decision.Kind != decision.KPriority {
		return false
	}
	land, ok := botLandDrop(r.Decision, r.BotAsk)
	if !ok {
		return false
	}
	r.Bot, r.NoBot = land, false
	return true
}

// botLandDrop is the land drop of bot ask at priority decision d: ask's
// answer over d without its mana activations (withoutActivations), mapped
// back onto d's own option indices, when it plays a land d offers. A d that
// offers no land play is not asked; an error, a payment answer, an index the
// decision without activations does not have, or any answer but a land play
// is no land drop.
func botLandDrop(d *decision.Decision, ask func(decision.Decision) (decision.Intent, error)) (decision.Intent, bool) {
	land := false
	for _, o := range d.Options {
		if o.Kind == "play_land" {
			land = true
			break
		}
	}
	if !land {
		return decision.Intent{}, false
	}
	without, back := withoutActivations(*d)
	in, err := ask(without)
	if err != nil || in.Payment != nil || len(in.Choices) != 1 || in.Choices[0] < 0 || in.Choices[0] >= len(back) {
		return decision.Intent{}, false
	}
	in.Choices = []int{back[in.Choices[0]]}
	if priorityBase(d, in) != BasePlayLand {
		return decision.Intent{}, false
	}
	return in, true
}

// withoutActivations is decision d without its "activate" options (the
// engine's mana abilities, legalActions), the rest renumbered in order, and
// back, each new index's option index in d. The payment actions are kept.
func withoutActivations(d decision.Decision) (decision.Decision, []int) {
	opts := make([]decision.Option, 0, len(d.Options))
	back := make([]int, 0, len(d.Options))
	for _, o := range d.Options {
		if o.Kind == "activate" {
			continue
		}
		back = append(back, o.Index)
		o.Index = len(opts)
		opts = append(opts, o)
	}
	d.Options = opts
	return d, back
}
