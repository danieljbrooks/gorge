package main

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"encoding/json"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/adams-shaun/gorge/cards"
	"github.com/adams-shaun/gorge/internal/policynet"
	"github.com/adams-shaun/gorge/internal/testutil"
)

// :decided[=H], :decidedn=, :decidedsims= and :decidedw= parse into the az seat's decided-game
// settings with their defaults (0.9, 3, 40, 0.1), decided=0 is off, and every bad range or
// combination is refused at parse time.
func TestParseDecided(t *testing.T) {
	for _, tc := range []struct {
		spec       string
		hi         float64
		n, sims    int
		w          float64
		configured bool
	}{
		{"az:sims=8", 0, 0, 0, 0, false},
		{"az:sims=8:decided=0", 0, 0, 0, 0, false},
		{"az:sims=8:decided", 0.9, 3, 40, 0.1, true},
		{"az:sims=8:decided=0.95", 0.95, 3, 40, 0.1, true},
		{"az:sims=8:explore:decided=0.8:decidedn=2:decidedsims=10:decidedw=0", 0.8, 2, 10, 0, true},
		{"az:sims=8:decided=0.9:decidedw=1:pcr=0.5", 0.9, 3, 40, 1, true},
	} {
		p, err := parsePolicy(tc.spec)
		if err != nil {
			t.Fatalf("%s: %v", tc.spec, err)
		}
		if p.Decided != tc.hi || p.DecidedN != tc.n || p.DecSims != tc.sims || p.DecW != tc.w {
			t.Fatalf("%s: decided %g n %d sims %d w %g, want %g %d %d %g", tc.spec, p.Decided, p.DecidedN, p.DecSims, p.DecW, tc.hi, tc.n, tc.sims, tc.w)
		}
		cfg := p.azConfig()
		if cfg.DecidedHigh != tc.hi || cfg.DecidedN != tc.n || cfg.DecidedSims != tc.sims || cfg.DecidedWeight != tc.w {
			t.Fatalf("%s: seat config %g %d %d %g", tc.spec, cfg.DecidedHigh, cfg.DecidedN, cfg.DecidedSims, cfg.DecidedWeight)
		}
		if _, err := p.seat(1); err != nil {
			t.Fatalf("%s: %v", tc.spec, err)
		}
	}
	for _, tc := range []struct{ spec, want string }{
		{"az:decided=0.5", "threshold in (0.5,1)"},
		{"az:decided=1", "threshold in (0.5,1)"},
		{"az:decided=-0.9", "threshold in (0.5,1)"},
		{"az:decided=NaN", "threshold in (0.5,1)"},
		{"az:decided=x", "invalid syntax"},
		{"az:decidedn=2", "need decided=H"},
		{"az:decided=0:decidedsims=10", "need decided=H"},
		{"az:decidedw=0.5", "need decided=H"},
		{"az:decided:decidedn=0", "want >= 1 root values"},
		{"az:decided:decidedn=x", "invalid syntax"},
		{"az:decided:decidedsims=0", "want >= 1 simulations"},
		{"az:decided:decidedw=1.5", "record weight in [0,1]"},
		{"az:decided:decidedw=-0.1", "record weight in [0,1]"},
		{"bot:decided", "are az keys"},
		{"sbtactical:decidedn=3", "are az keys"},
	} {
		if _, err := parsePolicy(tc.spec); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: error %v, want one mentioning %q", tc.spec, err, tc.want)
		}
	}
}

// decidedGame plays one recorded game of az (spec) against the bot and returns the game record
// and its corpus member.
func decidedGame(t *testing.T, spec string) (gameRec, []byte) {
	t.Helper()
	reg := testutil.CorpusRegistry(t)
	dc := &deckCache{reg: reg, m: map[string][]*cards.Card{
		"red":   testutil.RepoDeck(t, reg, "mono-red-goblins"),
		"green": testutil.RepoDeck(t, reg, "mono-green-stompy"),
	}}
	specs := make([]*policySpec, 2)
	for i, s := range []string{spec, "bot"} {
		p, err := parsePolicy(s)
		if err != nil {
			t.Fatal(err)
		}
		specs[i] = p
	}
	jb := job{game: 0, seed: 42, decks: [2]string{"red", "green"}, sides: [2]int{0, 1}}
	rec, member := playOne(jb, specs, dc, reg, 40, 5000, 0, true)
	if rec.Result == "error" {
		t.Fatalf("%s: error %s (stall %q)", spec, rec.Err, rec.Stall)
	}
	return rec, member
}

// corpusRecords decodes a corpus member's visit records.
func corpusRecords(t *testing.T, member []byte) []policynet.VisitRecord {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(member))
	if err != nil {
		t.Fatal(err)
	}
	var out []policynet.VisitRecord
	dec := json.NewDecoder(zr)
	for {
		var r policynet.VisitRecord
		if err := dec.Decode(&r); err == io.EOF {
			return out
		} else if err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
}

// readNpyF32 reads a 1-D little-endian float32 .npy file (writeNpy's).
func readNpyF32(t *testing.T, path string) []float32 {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) < 10 || string(raw[1:6]) != "NUMPY" || !strings.Contains(string(raw[10:]), "'<f4'") {
		t.Fatalf("%s: not a float32 .npy", path)
	}
	body := raw[10+int(binary.LittleEndian.Uint16(raw[8:10])):]
	out := make([]float32, len(body)/4)
	for i := range out {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(body[4*i:]))
	}
	return out
}

// A decided= game searches some decisions at decidedsims= and records them with the decidedw=
// weight; the game record counts them (summary only: the JSONL line is unchanged), and pack
// writes the weights as weight.npy -- a corpus without weighted records packs no weight.npy, so
// its shards are exactly the old ones. decidedw=1 plays the same game, recording every decided
// search unweighted.
func TestDecidedPlaysRecordsAndPacksWeights(t *testing.T) {
	const spec = "az:sims=6:explore:decided=0.7:decidedn=2:decidedsims=2"
	rec, member := decidedGame(t, spec+":decidedw=0.25")
	n, win, agree := rec.decided[0][0], rec.decided[0][1], rec.decided[0][2]
	if n == 0 || win > n || agree > n || rec.decided[1] != [3]int{} {
		t.Fatalf("decided counts %v: want decided searches on seat 0 only", rec.decided)
	}
	recs := corpusRecords(t, member)
	weighted := 0
	for _, r := range recs {
		switch {
		case r.Weight == 0.25 && r.Sims == 2:
			weighted++
		case r.Weight != 0 || r.Sims != 6:
			t.Fatalf("record seq %d: weight %g sims %d", r.Sequence, r.Weight, r.Sims)
		}
	}
	if weighted == 0 || weighted != rec.weighted || len(recs) != rec.Visits || weighted > n {
		t.Fatalf("%d weighted of %d records; game record %d weighted of %d, %d decided", weighted, len(recs), rec.weighted, rec.Visits, n)
	}
	line, _ := json.Marshal(rec)
	var keys map[string]any
	if err := json.Unmarshal(line, &keys); err != nil {
		t.Fatal(err)
	}
	for k := range keys {
		if strings.Contains(k, "decided") || strings.Contains(k, "weight") {
			t.Fatalf("the game line carries the summary count %q: %s", k, line)
		}
	}

	dir := t.TempDir()
	pack := func(name string, member []byte) string {
		t.Helper()
		in, out := filepath.Join(dir, name+".visits.jsonl.gz"), filepath.Join(dir, name)
		if err := os.WriteFile(in, member, 0o644); err != nil {
			t.Fatal(err)
		}
		if code := runPack([]string{"-in", in, "-out", out}); code != 0 {
			t.Fatalf("pack %s: exit %d", name, code)
		}
		return filepath.Join(out, "00000")
	}
	wts := readNpyF32(t, filepath.Join(pack("weighted", member), "weight.npy"))
	if len(wts) != len(recs) {
		t.Fatalf("weight.npy has %d values for %d records", len(wts), len(recs))
	}
	for i, r := range recs {
		want := float32(1)
		if r.Weight > 0 {
			want = float32(r.Weight)
		}
		if wts[i] != want {
			t.Fatalf("weight.npy[%d] = %g, record weight %g", i, wts[i], r.Weight)
		}
	}

	rec1, member1 := decidedGame(t, spec+":decidedw=1")
	if rec1.Turns != rec.Turns || rec1.Intents != rec.Intents || rec1.Result != rec.Result || rec1.decided != rec.decided {
		t.Fatalf("decidedw=1 played another game: %+v vs %+v", rec1, rec)
	}
	if rec1.weighted != 0 || rec1.Visits != rec.Visits {
		t.Fatalf("decidedw=1: %d weighted of %d records, want 0 of %d", rec1.weighted, rec1.Visits, rec.Visits)
	}
	if _, err := os.Stat(filepath.Join(pack("unweighted", member1), "weight.npy")); !os.IsNotExist(err) {
		t.Fatalf("a corpus without weighted records packed a weight.npy (%v)", err)
	}
	t.Logf("%s after %d turns: %d decided searches (%d winning, %d agreeing), %d of %d records weighted",
		rec.Result, rec.Turns, n, win, agree, weighted, len(recs))
}
