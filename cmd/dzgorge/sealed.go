package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"sort"

	"github.com/adams-shaun/gorge/draftlab"
	"github.com/adams-shaun/gorge/limited"
	"github.com/adams-shaun/gorge/limited/setdata"
)

// saltSealedPools tags the sealed-pools seed streams: pool i of a run with base
// seed S opens its packs from rand.NewPCG(S, saltSealedPools<<32|i) — the labdemo
// step-item pattern (cmd/labdemo stepItem), in a word no draftlab or labdemo
// salt uses.
const saltSealedPools = 0xD25E01

// best23Ref is the baseline builder sealed-pools -best23 runs: gorge's Best23
// (limited/pickbot), frozen at version 1 so a later behaviour change refuses to
// resolve instead of silently moving the baseline.
var best23Ref = draftlab.AgentRef{Name: "best23", Version: "1"}

// sealedPool is one line of sealed-pools -out.
type sealedPool struct {
	PoolID    string         `json:"pool_id"`
	Index     int            `json:"index"`
	Set       string         `json:"set"`
	Collation string         `json:"collation"`
	Packs     int            `json:"packs"`
	BaseSeed  uint64         `json:"base_seed"`
	Seed      uint64         `json:"seed"` // the SealedPool seed: the pool is a pure function of (set, collation, seed, packs)
	Cards     map[string]int `json:"cards"`
	Best23    *builtDeck     `json:"best23,omitempty"`
}

// builtDeck is a baseline builder's deck: its agent ref and its 40 cards by
// name, basics included (the shape of a gorge deck file's "cards").
type builtDeck struct {
	Builder string      `json:"builder"`
	Cards   []deckEntry `json:"cards"`
}

type deckEntry struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// poolSeed is pool i's SealedPool seed under base seed: rand.NewPCG(base,
// saltSealedPools<<32|i).Uint64().
func poolSeed(base uint64, i int) uint64 {
	return rand.New(rand.NewPCG(base, saltSealedPools<<32|uint64(i))).Uint64()
}

// parseCollation maps -collation's short names to setdata's: arena (the MTG
// Arena Play Booster, setdata's default) and play (the paper Play Booster);
// setdata's own spellings pass through.
func parseCollation(s string) (setdata.Collation, error) {
	switch s {
	case "", "arena", string(setdata.Arena):
		return setdata.Arena, nil
	case string(setdata.Play): // "play"
		return setdata.Play, nil
	}
	return "", fmt.Errorf("collation %q: want arena or play", s)
}

// countByName folds CardRefs (or deck entries) into name counts.
func countByName(refs []limited.CardRef) map[string]int {
	out := make(map[string]int, len(refs))
	for _, r := range refs {
		out[r.Name]++
	}
	return out
}

// deckEntries folds a deck file's cards (one entry per Main card, one per basic
// type) into sorted {name, count} entries.
func deckEntries(d draftlab.Deck) []deckEntry {
	n := map[string]int{}
	for _, e := range d.File.Cards {
		n[e.Name] += e.Count
	}
	names := make([]string, 0, len(n))
	for k := range n {
		names = append(names, k)
	}
	sort.Strings(names)
	out := make([]deckEntry, 0, len(names))
	for _, k := range names {
		out = append(out, deckEntry{Name: k, Count: n[k]})
	}
	return out
}

// makeSealedPools opens n sealed pools, indices first..first+n-1 under base seed
// seed, with the Best23 deck of each when best23. Every pool is a pure function
// of (set, collation, poolSeed(seed, i), packs); so is its Best23 deck.
func makeSealedPools(lab *draftlab.Lab, set string, c setdata.Collation, seed uint64, first, n, packs int,
	best23 bool) ([]sealedPool, error) {
	var nb draftlab.NamedBuilder
	if best23 {
		var err error
		if nb, err = lab.Builder(best23Ref); err != nil {
			return nil, err
		}
	}
	out := make([]sealedPool, 0, n)
	for i := first; i < first+n; i++ {
		ps := poolSeed(seed, i)
		pool, err := lab.SealedPool(set, c, ps, packs)
		if err != nil {
			return nil, fmt.Errorf("pool %d: %w", i, err)
		}
		sp := sealedPool{PoolID: fmt.Sprintf("%s-%d-%06d", set, seed, i), Index: i, Set: set, Collation: string(c),
			Packs: packs, BaseSeed: seed, Seed: ps, Cards: countByName(pool)}
		if best23 {
			d, err := lab.Build(pool, nb)
			if err != nil {
				return nil, fmt.Errorf("pool %d: %w", i, err)
			}
			sp.Best23 = &builtDeck{Builder: nb.Ref.String(), Cards: deckEntries(d)}
		}
		out = append(out, sp)
	}
	return out, nil
}

func writeSealedPools(w io.Writer, pools []sealedPool) error {
	bw := bufio.NewWriter(w)
	enc := json.NewEncoder(bw)
	for i := range pools {
		if err := enc.Encode(&pools[i]); err != nil { // a map encodes with sorted keys: the bytes are deterministic
			return err
		}
	}
	return bw.Flush()
}

// runSealedPools writes sealed pools from gorge's draftlab (SealedPool) as JSON
// lines, for DraftZero's deck builder: tabula-rasa training pools without
// 17lands data. -best23 adds gorge's Best23 deck of each pool as a baseline
// (it ranks cards by 17lands GIH ratings: an evaluation opponent, not training
// data).
func runSealedPools(args []string) int {
	fs := flag.NewFlagSet("sealed-pools", flag.ExitOnError)
	cardsDir := fs.String("cards", ".cards", "gorge corpus directory")
	set := fs.String("set", "FDN", "set code (draftlab: FDN or BLB)")
	n := fs.Int("n", 100, "pools to write")
	first := fs.Int("first", 0, "index of the first pool (pool i's seed depends only on -seed and i)")
	seed := fs.Uint64("seed", 1, "base seed; pool i opens its packs from rand.NewPCG(seed, 0xD25E01<<32|i)")
	packs := fs.Int("packs", 6, "packs per pool (draftlab: 1-12)")
	coll := fs.String("collation", "arena", "booster collation: arena (MTG Arena Play Booster) or play (paper)")
	best23 := fs.Bool("best23", false, "also write each pool's best23@1 deck (40 cards with basics)")
	out := fs.String("out", "", "pools JSONL (required; - for stdout)")
	fs.Parse(args)
	if *out == "" {
		fmt.Fprintln(os.Stderr, "sealed-pools: -out is required")
		return 2
	}
	if *n < 1 || *first < 0 || *packs < 1 || *packs > 12 {
		fmt.Fprintf(os.Stderr, "sealed-pools: want -n >= 1, -first >= 0 and 1 <= -packs <= 12 (got %d, %d, %d)\n",
			*n, *first, *packs)
		return 2
	}
	c, err := parseCollation(*coll)
	if err != nil {
		fmt.Fprintln(os.Stderr, "sealed-pools:", err)
		return 2
	}
	lab, err := draftlab.Open(*cardsDir, draftlab.Options{})
	if err != nil {
		fmt.Fprintln(os.Stderr, "sealed-pools:", err)
		return 1
	}
	pools, err := makeSealedPools(lab, *set, c, *seed, *first, *n, *packs, *best23)
	if err != nil {
		fmt.Fprintln(os.Stderr, "sealed-pools:", err)
		return 1
	}
	var w io.Writer = os.Stdout
	if *out != "-" {
		tmp := *out + ".tmp"
		f, err := os.Create(tmp)
		if err != nil {
			fmt.Fprintln(os.Stderr, "sealed-pools:", err)
			return 1
		}
		if err := writeSealedPools(f, pools); err != nil {
			f.Close()
			fmt.Fprintln(os.Stderr, "sealed-pools:", err)
			return 1
		}
		if err := f.Close(); err != nil {
			fmt.Fprintln(os.Stderr, "sealed-pools:", err)
			return 1
		}
		if err := os.Rename(tmp, *out); err != nil {
			fmt.Fprintln(os.Stderr, "sealed-pools:", err)
			return 1
		}
		fmt.Fprintf(os.Stderr, "sealed-pools: %d %s pools (%s, %d packs, seed %d, first %d) -> %s\n", len(pools), *set, c,
			*packs, *seed, *first, *out)
		return 0
	}
	if err := writeSealedPools(w, pools); err != nil {
		fmt.Fprintln(os.Stderr, "sealed-pools:", err)
		return 1
	}
	return 0
}
