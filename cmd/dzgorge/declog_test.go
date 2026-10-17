package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"github.com/adams-shaun/gorge/internal/policynet"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// -declog only reads the game: the same game with and without it is the same game, record for record, and the log
// holds well-formed lines of its four kinds plus each game's priority counts for both seats.
func TestDeclogReadsOnly(t *testing.T) {
	const spec = "az:sims=6:autopay"
	rec0, member0 := decidedGame(t, spec)

	path := filepath.Join(t.TempDir(), "declog.jsonl")
	w, err := newAuditWriter(path)
	if err != nil {
		t.Fatal(err)
	}
	declogW, declogCards = w, sync.Map{}
	defer func() { declogW, declogCards = nil, sync.Map{} }()
	rec1, member1 := decidedGame(t, spec)
	w.close()

	if rec1.Turns != rec0.Turns || rec1.Intents != rec0.Intents || rec1.Result != rec0.Result ||
		rec1.Winner != rec0.Winner || !bytes.Equal(member0, member1) {
		t.Fatalf("-declog changed the game: %+v vs %+v", rec1, rec0)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	kinds := map[string]int{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var line map[string]any
		if err := json.Unmarshal(sc.Bytes(), &line); err != nil {
			t.Fatalf("bad line %q: %v", sc.Text(), err)
		}
		k, _ := line["t"].(string)
		kinds[k]++
		switch k {
		case "prio":
			// rerooted is written only when true, and only beside the search record it reads (no_bot).
			if r, ok := line["rerooted"]; ok && (r != true || line["search"] == nil) {
				t.Fatalf("prio line with rerooted %v and search %v: %q", r, line["search"], sc.Text())
			}
		case "target", "stack", "card":
		case "game":
			counts, _ := line["counts"].([]any)
			if len(counts) != 2 {
				t.Fatalf("game line without both seats' counts: %q", sc.Text())
			}
		default:
			t.Fatalf("unknown line kind %q", k)
		}
	}
	if kinds["game"] != 1 || kinds["stack"] == 0 || kinds["card"] == 0 {
		t.Fatalf("line kinds %v: want one game line, stack objects and cards", kinds)
	}
	t.Logf("%s after %d turns; log lines %v", rec1.Result, rec1.Turns, kinds)
}

// A prio line takes the decision's own search record, and marks it rerooted exactly when the record says the search
// had no bot candidate (the bot-tap reroute); a stale record (another decision's) attaches nothing.
func TestDeclogAttachSearchMarksReroutes(t *testing.T) {
	rec := func(seq uint64, noBot bool) *policynet.VisitRecord {
		return &policynet.VisitRecord{Sequence: seq, Sims: 8, Cands: [][]int{{0}, {1}}, Visits: []int{5, 3},
			Q: []float64{0.5, 0.4}, Prior: []float64{0.6, 0.4}, NoBot: noBot}
	}
	for _, c := range []struct {
		name               string
		sr                 *policynet.VisitRecord
		wantSearch, reroot bool
	}{
		{"rerouted", rec(7, true), true, true},
		{"normal", rec(7, false), true, false},
		{"stale", rec(6, true), false, false},
		{"none", nil, false, false},
	} {
		var r dlPrio
		attachSearch(&r, c.sr, 7)
		if (r.Search != nil) != c.wantSearch || r.Rerooted != c.reroot {
			t.Errorf("%s: search %v rerooted %v, want %v %v", c.name, r.Search != nil, r.Rerooted, c.wantSearch, c.reroot)
		}
		line, _ := json.Marshal(r)
		if has := strings.Contains(string(line), `"rerooted"`); has != c.reroot {
			t.Errorf("%s: line %s: rerooted field present %v, want %v", c.name, line, has, c.reroot)
		}
	}
}
