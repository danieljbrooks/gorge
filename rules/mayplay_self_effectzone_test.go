package rules

import (
	"testing"

	"github.com/adams-shaun/gorge/state"
)

// DraftZero patch 0009: Omniscience's static has no EffectZone$, so it works only on the battlefield. In hand it
// used to offer its own free cast (a deck builder trained on self-play found it: drawn, it won ~80% of games).
func TestOmniscienceInHandGrantsNothing(t *testing.T) {
	e := handEngine(t, corpusAlternativeCard(t, "Omniscience"), corpusAlternativeCard(t, "Craw Wurm"))
	omni, wurm := e.G.Zone(state.ZHand, 0)[0], e.G.Zone(state.ZHand, 0)[1]
	_, selfFree := freeHandCastOption(e, 0, omni)
	_, wurmFree := freeHandCastOption(e, 0, wurm)
	if selfFree || wurmFree {
		t.Errorf("Omniscience in hand: free cast of itself offered=%v, of Craw Wurm offered=%v; want neither", selfFree, wurmFree)
	}
}
