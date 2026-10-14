package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/adams-shaun/gorge/draftlab"
	"github.com/adams-shaun/gorge/internal/testutil"
	"github.com/adams-shaun/gorge/limited/build"
	"github.com/adams-shaun/gorge/limited/setdata"
)

// A pool's seed depends only on the base seed and its index, and differs across both.
func TestPoolSeed(t *testing.T) {
	if poolSeed(7, 3) != poolSeed(7, 3) {
		t.Fatal("poolSeed is not a function of (seed, i)")
	}
	seen := map[uint64]bool{}
	for _, s := range []uint64{1, 2, 7} {
		for i := 0; i < 50; i++ {
			v := poolSeed(s, i)
			if seen[v] {
				t.Fatalf("poolSeed(%d, %d) repeats an earlier seed", s, i)
			}
			seen[v] = true
		}
	}
}

func TestParseCollation(t *testing.T) {
	for in, want := range map[string]setdata.Collation{"": setdata.Arena, "arena": setdata.Arena,
		"play-arena": setdata.Arena, "play": setdata.Play} {
		got, err := parseCollation(in)
		if err != nil || got != want {
			t.Errorf("parseCollation(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := parseCollation("draft"); err == nil {
		t.Error("parseCollation(draft) did not fail")
	}
}

func sealedLab(t *testing.T) *draftlab.Lab {
	t.Helper()
	l, err := draftlab.Open(testutil.CorpusDirOrSkip(t), draftlab.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// Pools are a pure function of (set, collation, seed, index): a rerun, or a run that
// starts at -first, writes the same bytes; six FDN Play Boosters hold 84 cards; the
// Best23 deck is 40 cards drawn from the pool plus basics.
func TestSealedPoolsDeterministic(t *testing.T) {
	lab := sealedLab(t)
	a, err := makeSealedPools(lab, "FDN", setdata.Arena, 7, 0, 6, 6, true)
	if err != nil {
		t.Fatal(err)
	}
	b, err := makeSealedPools(lab, "FDN", setdata.Arena, 7, 0, 6, 6, true)
	if err != nil {
		t.Fatal(err)
	}
	tail, err := makeSealedPools(lab, "FDN", setdata.Arena, 7, 4, 2, 6, true)
	if err != nil {
		t.Fatal(err)
	}
	var ba, bb, bt bytes.Buffer
	for _, x := range []struct {
		p []sealedPool
		w *bytes.Buffer
	}{{a, &ba}, {b, &bb}, {a[4:], &bt}} {
		if err := writeSealedPools(x.w, x.p); err != nil {
			t.Fatal(err)
		}
	}
	if !bytes.Equal(ba.Bytes(), bb.Bytes()) {
		t.Fatal("two runs with one seed wrote different pools")
	}
	var bt2 bytes.Buffer
	if err := writeSealedPools(&bt2, tail); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(bt.Bytes(), bt2.Bytes()) {
		t.Fatal("-first 4 -n 2 differs from pools 4 and 5 of -n 6")
	}
	other, err := makeSealedPools(lab, "FDN", setdata.Arena, 8, 0, 1, 6, false)
	if err != nil {
		t.Fatal(err)
	}
	if other[0].Seed == a[0].Seed || other[0].Best23 != nil {
		t.Fatal("another base seed gave the same pool seed, or a Best23 deck without -best23")
	}
	basics := map[string]bool{}
	for _, n := range build.BasicNames {
		basics[n] = true
	}
	for _, p := range a {
		total := 0
		for _, n := range p.Cards {
			total += n
		}
		if total != 84 {
			t.Errorf("%s: %d cards, want 84 (6 packs of 14)", p.PoolID, total)
		}
		if p.Seed != poolSeed(7, p.Index) || p.Set != "FDN" || p.Collation != "play-arena" || p.Packs != 6 {
			t.Errorf("%s: header %+v", p.PoolID, p)
		}
		deckN := 0
		for _, e := range p.Best23.Cards {
			deckN += e.Count
			if !basics[e.Name] && e.Count > p.Cards[e.Name] {
				t.Errorf("%s: best23 plays %d %s, the pool has %d", p.PoolID, e.Count, e.Name, p.Cards[e.Name])
			}
		}
		if deckN != 40 || p.Best23.Builder != "best23@1" {
			t.Errorf("%s: best23 deck %d cards by %q, want 40 by best23@1", p.PoolID, deckN, p.Best23.Builder)
		}
	}
}

// The command line writes one JSON object a line.
func TestRunSealedPools(t *testing.T) {
	dir := testutil.CorpusDirOrSkip(t)
	out := filepath.Join(t.TempDir(), "pools.jsonl")
	if rc := runSealedPools([]string{"-cards", dir, "-set", "FDN", "-n", "3", "-seed", "5", "-collation", "play",
		"-best23", "-out", out}); rc != 0 {
		t.Fatalf("sealed-pools exited %d", rc)
	}
	f, err := os.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	n := 0
	for sc.Scan() {
		var p sealedPool
		if err := json.Unmarshal(sc.Bytes(), &p); err != nil {
			t.Fatal(err)
		}
		if p.Index != n || p.Collation != "play" || p.Best23 == nil || len(p.Cards) == 0 {
			t.Errorf("line %d: %+v", n, p)
		}
		n++
	}
	if n != 3 {
		t.Fatalf("%d lines, want 3", n)
	}
	if rc := runSealedPools([]string{"-cards", dir, "-n", "1", "-collation", "draft", "-out", out}); rc != 2 {
		t.Fatalf("a bad collation exited %d, want 2", rc)
	}
}
