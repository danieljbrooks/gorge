package main

import (
	"testing"

	"github.com/adams-shaun/gorge/internal/azmcts"
)

// play's summary sums the az seats' root Diags (azAgg.watch) and reports each counter under its own key: the
// bot-tap backstop's (az_bot_tap_rerooted, az_land_first). A Diag of no searched kind is not counted.
func TestPlaySummaryCounters(t *testing.T) {
	var a azAgg
	a.watch(azmcts.Diag{Stats: azmcts.Stats{LandFirst: 100, BotTapRerooted: 100}}) // Kind "": ignored
	a.watch(azmcts.Diag{Kind: "priority", Stats: azmcts.Stats{BotTapRerooted: 7, LandFirst: 8}})
	a.watch(azmcts.Diag{Kind: "priority", Searched: true, Stats: azmcts.Stats{BotTapRerooted: 70, LandFirst: 80}})
	sum := map[string]any{}
	a.tapSummary(sum)
	for k, want := range map[string]int64{"az_bot_tap_rerooted": 77, "az_land_first": 88} {
		if got, ok := sum[k].(int64); !ok || got != want {
			t.Errorf("%s = %v, want %d", k, sum[k], want)
		}
	}
	if len(sum) != 2 {
		t.Errorf("summary keys %v, want the 2 above", sum)
	}
	if a.asked.Load() != 2 || a.searched.Load() != 1 {
		t.Errorf("asked %d searched %d, want 2 and 1", a.asked.Load(), a.searched.Load())
	}
}
