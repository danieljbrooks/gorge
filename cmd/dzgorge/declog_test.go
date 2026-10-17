package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
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
		case "prio", "target", "stack", "card":
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
