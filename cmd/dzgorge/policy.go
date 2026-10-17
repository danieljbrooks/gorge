package main

import (
	"fmt"
	"github.com/adams-shaun/gorge/decision"
	"math"
	"strconv"
	"strings"

	"github.com/adams-shaun/gorge/botpolicy"
	"github.com/adams-shaun/gorge/bots/sbtactical"
	"github.com/adams-shaun/gorge/cards"
	"github.com/adams-shaun/gorge/internal/azmcts"
	"github.com/adams-shaun/gorge/internal/gonet"
	"github.com/adams-shaun/gorge/internal/policynet"
	"github.com/adams-shaun/gorge/internal/searchprobe"
	"github.com/adams-shaun/gorge/internal/spellbench/builtins"
	"github.com/adams-shaun/gorge/seat"
)

// policySpec is one side's policy, parsed from "name[:key=value...]":
//
//	random                       uniform over the legal options, mana paid automatically (sb-uniform)
//	bot                          gorge's production heuristic, no search
//	sbtactical                   sb-tactical (bots/sbtactical, internal/spellbench/builtins.NewTactical):
//	                             the SpellBench scored heuristic, gorge's strongest no-search bot. Mana is
//	                             always auto-paid (builtins.AutoPay, the mode botbench's sb-tactical arm
//	                             and the hosted seat use), and the game runner hands it the engine as its
//	                             potential-play payment planner, as botbench does. Its keep/mulligan and
//	                             bottoming asks go to gorge's default policy, so :mull, :keep7 and
//	                             -mull-heuristic act on it exactly as on bot; other keys are ignored
//	az:sims=100                  AlphaZero-style PUCT search over the seat's own decisions
//	                             (internal/azmcts), honest worlds: each simulation re-deals the
//	                             cards the seat cannot see. Generation 0 without net=: a uniform
//	                             prior and gorge's heuristic leaf
//	az:sims=100:net=gen1.gpol    the same, the network's prior and value head
//	   :leaf=heuristic           keep the heuristic leaf (the net is the prior only)
//	   :prior=uniform            keep the uniform prior (the net is the leaf only)
//	   :worlds=K                 K deals per decision instead of one per simulation
//	   :explore[:nonoise]        generation: sample moves by visits on turns 1-4 (+ root noise)
//	   :cpuct=1.5 :fpu=0.1 :cands=N
//	   :autopay                  mana paid automatically: every cast (and pass) at a priority stop is a
//	                             candidate, so the search decides what to cast and when (the seat's own
//	                             bot is gorge's auto-pay bot; azmcts Options.AutoPayment). bot:autopay is
//	                             that bot alone. The az and prior seats consume the engine's payment
//	                             plans (gorge 9afeacb2e): their bot casts through a plan instead of
//	                             tapping lands by hand. On an az seat only, a decision whose bot answer
//	                             still taps by hand is rerouted: searched over the payment vocabulary
//	                             with no bot candidate (azmcts Root.NoBot; play's summary counts them as
//	                             az_bot_tap_rerooted, -declog marks their prio lines rerooted) -- unless
//	                             its bot, asked again without its mana taps, plays a land: the bot's tap
//	                             gate runs before its land drop, and the vocabulary has no land play, so
//	                             the land is played first, unsearched (az_land_first). A prior
//	                             seat has no such reroute: it gains the payment plans but still plays
//	                             such a bot tap unsearched
//	   :manualtaps               with :autopay: the seat as before gorge 9afeacb2e, to replay older games
//	                             -- no payment plans are built for it, so its auto-pay bot taps lands by
//	                             hand, and a decision whose bot answer is such a tap is skipped (the tap
//	                             played unsearched) rather than searched; the search then decides with
//	                             mana floating (azmcts SeatConfig.ManualTaps). An az and prior key;
//	                             without :autopay it changes nothing. An exact replay of an old game
//	                             needs play -workers 1: with more workers the search's floats and walk
//	                             counters differ from run to run (gonet's caches are shared across the
//	                             workers' games), as the old binary's own multi-worker runs do
//	   :holdupkeep[=NAMES]       no instant-speed cast in the seat's own upkeep or draw step: at a priority
//	                             decision of the active player there with an empty stack, every candidate
//	                             that casts a spell (an instant, a flash card, a flashback or other
//	                             alternative-cost cast; a payment cast and a cast with mana floating
//	                             alike) is dropped, at the seat's root and at its points inside a
//	                             simulation; pass, abilities, Foretell and Suspend (special actions, no
//	                             spell cast) and the rest stay. The same cast in the first main phase has
//	                             the same lands untapped, and has seen the draw and the land drop.
//	                             A root left with fewer than two candidates plays the bot's answer (there
//	                             a pass) unsearched, and a bot answer that is itself a held cast is played
//	                             as pass, at the root and inside a simulation; play's summary counts the
//	                             roots changed as az_holdupkeep_roots, those passed as
//	                             az_holdupkeep_bot_passes, and the decisions changed inside simulations
//	                             as az_holdupkeep_walk and az_holdupkeep_opp. With :oppnodes:oppfull the
//	                             opponent's points hold its casts in its own upkeep and draw too. =NAMES
//	                             exempts cards by name, '|'-separated (=Opt|Brainstorm; a card matches by
//	                             any face's name; a name may not hold a colon). Any step with something
//	                             on the stack, and every other turn's, is unaffected. An az and prior key
//	                             (azmcts Options.HoldUpkeep, HoldUpkeepExempt)
//	   :tricks[=NAMES]           combat tricks only where they do something, and never on the wrong side. A
//	                             gated card's cast (by default FDN's Giant Growth, Sure Strike, Bulk Up, Claws
//	                             Out, Goblin Surprise, Snakeskin Veil, Divine Resilience, Fake Your Own
//	                             Death, Celestial Armor and Fleeting Flight; =NAMES replaces the list,
//	                             '|'-separated, a card matching by any face's name, a name holding no colon)
//	                             is a candidate only with something on the stack, or from declare attackers
//	                             through combat damage once a creature is attacking; a card granting evasion
//	                             (Fleeting Flight's flying) also at its controller's own beginning of combat.
//	                             Goblin Surprise, a modal spell whose token mode is no trick, is cast freely
//	                             and only its pump mode is gated, at its modes decision. Side checks: a gated
//	                             card is no candidate when no permanent of its caster's is a legal target (a
//	                             pump-all or Celestial Armor: when the caster controls no creature), and at
//	                             its target decision the opponent's options are dropped when one of the
//	                             caster's is offered; a counterspell (Refute, Essence Scatter, An Offer You
//	                             Can't Refuse, any SP$ Counter whose only target is a spell; whatever the
//	                             list) is no candidate unless an opponent's spell is a legal target, and at
//	                             its target decision the caster's own spells are dropped when an
//	                             opponent's is offered. At the seat's root and its points inside a
//	                             simulation, and with :oppnodes:oppfull at the opponent's too; a bot answer
//	                             the rule drops is played as pass, or as the first kept targets or modes.
//	                             Composes with :holdupkeep. play's summary counts the roots changed as
//	                             az_tricks_gated_roots and az_tricks_side_roots, the bot answers replaced as
//	                             az_tricks_bot_passes, and the decisions changed inside simulations as
//	                             az_tricks_walk and az_tricks_opp. An az and prior key (azmcts
//	                             Options.Tricks, TricksCards)
//	   :topk=K                   the network chooses the candidates: every searched decision (the root
//	                             and every one inside a simulation) is enumerated in full and the bot's
//	                             answer is kept with the K-1 others the network's prior ranks highest,
//	                             instead of the first cands= in enumeration order (under autopay the
//	                             prior then also scores every cast). Needs net= or remote= and the
//	                             net's prior (inert under prior=uniform or with no network);
//	                             azmcts Options.PriorTopK
//	   :oppnodes                 the opponent in the tree: its searched decisions inside a simulation
//	                             (priority, attackers, blockers, single target, >= 2 candidates) are tree
//	                             points where it picks what is worst for the seat (PUCT on 1 - Q), its
//	                             prior the net's on the opponent's own view, instead of gorge's bot
//	                             answering them (azmcts Options.OpponentNodes)
//	   :oppfull                  with :oppnodes (which it needs): the opponent's points mirror the seat's
//	                             settings -- under :autopay its bot pays automatically and its casts are
//	                             searched candidates, under :topk=K the net's prior ranks and cuts its
//	                             candidates too (azmcts Options.OpponentMirror; gorge/patches/0007).
//	                             Without it the opponent's points ignore :autopay and :topk
//	   :skippass                 simulation shortcut: rollout engines auto-pass every priority window
//	                             outside a main phase with an empty stack (azmcts Options.SkipPass); the
//	                             real game and the root decision are untouched
//	   :mull                     the London mulligan by land count (botpolicy/mulligan.go): keep 2-5 lands
//	                             of 7 (also after one mulligan), after two keep unless 0 or 7, and
//	                             bottom toward ceil(K/2) lands, highest mana value spells first. Any
//	                             bot, sbtactical, az or prior seat; play -mull-heuristic sets it on every seat.
//	                             Without it the seat mulligans on gorge's 1/3 coin
//	   :keep7                    never mulligan (the control for :mull in a game with -mulligans N)
//	   :morekinds                the fuller search (gorge/patches/0007): besides priority, attackers,
//	                             blockers and single targets, a decision's modes, a may trigger's yes/no,
//	                             a choice among at most cands= options (X, a number, a name, cost cards,
//	                             dig/search picks; never a mana-payment window) and multi-target picks
//	                             are searched, each legal option set a candidate (the bot's first). With
//	                             :oppnodes the opponent's are tree points too, and with :oppfull:autopay
//	                             as well the opponent pays automatically and its casts are searched
//	                             candidates
//	   :kinds=LIST               the searched kinds by name, comma-separated: priority, attackers,
//	                             blockers, target, modes, optional, choose, multitarget, or all (=
//	                             :morekinds); the default is the first four
//	   :closedlist               the opponent's deck list is closed: the seat's own list stays known,
//	                             but every world deals the opponent's unknown hand and library from a
//	                             list drawn from the belief corpus (play -belief-decks/-belief-pool,
//	                             default the TRAIN split of -decks/-pool), conditioned on the colours
//	                             of the opponent's seen cards, never from its real list or its real
//	                             hidden cards (azmcts.WorldRedealClosed)
//	   :ksims=priority.200,blockers.40
//	                             per-kind simulations at the seat's root (decision kinds; azmcts
//	                             SeatConfig.SimsByKind); other kinds use sims=
//	   :pcr=P                    playout-cap randomization (KataGo; azmcts SeatConfig.FullProb): each
//	                             searched root decision is a FULL search with probability P (sims=,
//	                             ksims=, :explore's noise and sampling; recorded to -corpus) or else a
//	                             CHEAP one (cheap= simulations, no root noise, the most-visited
//	                             candidate, never recorded). A seeded coin per decision (seat seed and
//	                             decision sequence), so games never depend on -workers. P in [0,1];
//	                             0 (the default) is off, every searched decision full as before
//	   :cheap=N                  with pcr=: a cheap search's simulations (default 40)
//	   :pcrk=priority.0.65,attackers.0.5
//	                             with pcr=: the full-search probability by searched kind (the :kinds=
//	                             names: priority, attackers, blockers, target, modes, optional, choose,
//	                             multitarget; kind=P is accepted too); unlisted kinds use pcr=
//	   :decided[=H]              fewer simulations once the game is decided (KataGo; azmcts SeatConfig.Decided*):
//	                             after decidedn= consecutive searched root values of the seat all above H, or
//	                             all below 1-H (H in (0.5,1); bare :decided is 0.9), its full searches become
//	                             DECIDED ones -- decidedsims= simulations, no root noise, the most-visited
//	                             candidate, no forced= playouts -- recorded to -corpus with the training weight
//	                             decidedw= (the record's "weight" field), until a root value leaves that side's
//	                             band. pcr='s cheap decisions stay cheap. 0 (the default) is off
//	   :decidedn=3               with decided=: the run of decided root values that starts it (default 3)
//	   :decidedsims=40           with decided=: a decided search's simulations (default 40)
//	   :decidedw=0.1             with decided=: a decided search's record weight in [0,1] (default 0.1; 1 records
//	                             it as a full search, 0 does not record it)
//	   :forced[=K]               forced playouts with policy target pruning (KataGo; azmcts Options.ForcedK), at
//	                             the seat's root in full searches only (never a pcr= cheap or a decided search):
//	                             each candidate gets at least sqrt(K * prior * root visits) visits, and the
//	                             recorded visits (and :explore's visit sampling) are the pruned target -- every
//	                             candidate but the most-visited gives back up to that many while its PUCT value
//	                             stays below the most-visited's, and one left with <= 1 visit is dropped. K > 0;
//	                             bare :forced is 2; 0 (the default) is off
//	prior:net=gen1.gpol          the network's policy alone: the argmax of its prior over the
//	                             candidates the search would build, no simulation
//	az:sims=100:remote=unix:/tmp/dzg.sock
//	                             the network served by python -m dzg.serve (gorge/dzg): an MLP,
//	                             transformer or GNN reading gorge's entity encoding, in place
//	                             of a .gpol (also prior:remote=...)
//	az:sims=100:gonet=best.dzgw  dzg's mlp evaluated in this process (gonet.go), from python -m
//	                             dzg.export: the same network as remote= without the socket (also
//	                             prior:gonet=...; the path must not contain a colon)
type policySpec struct {
	Raw, Kind string
	Sims      int
	Net       string
	Remote    string
	GoNet     string
	model     *policynet.Model
	HeurLeaf  bool
	UniPrior  bool
	Worlds    int
	Explore   bool
	NoNoise   bool
	CPUCT     float64
	FPU       float64
	Cands     int
	AutoPay   bool
	Mull      botpolicy.MulliganRule // :mull / :keep7; zero is gorge's 1/3 coin
	TopK      int
	KSims     map[decision.Kind]int // ksims=priority.200,blockers.40: per-kind simulations (patch 0009)
	PCR       float64               // pcr=P: playout-cap randomization's full-search probability; 0 is off
	Cheap     int                   // cheap=N: a cheap search's simulations (defaultCheapSims under pcr=)
	PCRK      map[string]float64    // pcrk=priority.0.65,attackers.0.5: full-search probability by searched kind
	Decided   float64               // decided=H: decided-game searches above H / below 1-H; 0 is off
	DecidedN  int                   // decidedn=N: the run of decided root values that starts them
	DecSims   int                   // decidedsims=N: a decided search's simulations
	DecW      float64               // decidedw=W: a decided search's record weight
	Forced    float64               // forced=K: forced playouts with policy target pruning; 0 is off
	OppNodes  bool
	SkipPass  int
	OppFull   bool         // :oppfull
	Closed    bool         // :closedlist (the belief is set by play, setBelief)
	Kinds     azmcts.Kinds // :kinds= / :morekinds; zero is azmcts.AllKinds
	// lookup resolves card names for sbtactical (setLookup); nil reads View facts only.
	lookup builtins.CardLookup
	// belief is :closedlist's opponent-list belief (setBelief).
	belief *searchprobe.Belief

	// ManualTaps is :manualtaps: no payment plans, no bot-tap reroute (azmcts SeatConfig.ManualTaps).
	ManualTaps bool
	// HoldUpkeep is :holdupkeep, HoldExempt its =NAMES (azmcts Options.HoldUpkeep, HoldUpkeepExempt).
	HoldUpkeep bool
	HoldExempt string
	// Tricks is :tricks, TricksCards its =NAMES (azmcts Options.Tricks, TricksCards; "" the default list).
	Tricks      bool
	TricksCards string
}

// setBelief gives a :closedlist seat its belief (loaded after the specs are parsed).
func (p *policySpec) setBelief(b *searchprobe.Belief) {
	if p.Closed {
		p.belief = b
	}
}

// setLookup gives an sbtactical seat its card lookup over the game's corpus (the
// registry is loaded after the specs are parsed).
func (p *policySpec) setLookup(reg *cards.Registry) {
	if p.Kind == "sbtactical" {
		p.lookup = builtins.NewRegistryLookup(reg)
	}
}

func parsePolicy(s string) (*policySpec, error) {
	// remote=unix:/path and remote=tcp:host:port contain colons: take the rest of the spec.
	rest := ""
	if i := strings.Index(s, ":remote="); i >= 0 {
		s, rest = s[:i], s[i+len(":remote="):]
	}
	parts := strings.Split(s, ":")
	p := &policySpec{Raw: s, Kind: parts[0], Sims: azmcts.DefaultOptions().Sims, CPUCT: -1, FPU: -1, Cands: -1}
	switch p.Kind {
	case "random", "bot", "sbtactical", "az", "prior":
	default:
		return nil, fmt.Errorf("policy %q: want random, bot, sbtactical, az or prior", s)
	}
	cheapSet := false
	var decSet decidedKeys
	for _, kv := range parts[1:] {
		k, v, _ := strings.Cut(kv, "=")
		var err error
		switch k {
		case "sims":
			p.Sims, err = strconv.Atoi(v)
		case "net":
			p.Net = v
		case "gonet":
			p.GoNet = v
		case "leaf":
			if v != "heuristic" && v != "net" {
				return nil, fmt.Errorf("policy %q: leaf=%s, want heuristic or net", s, v)
			}
			p.HeurLeaf = v == "heuristic"
		case "prior":
			if v != "uniform" && v != "net" {
				return nil, fmt.Errorf("policy %q: prior=%s, want uniform or net", s, v)
			}
			p.UniPrior = v == "uniform"
		case "worlds":
			p.Worlds, err = strconv.Atoi(v)
		case "explore":
			p.Explore = true
		case "nonoise":
			p.NoNoise = true
		case "cpuct":
			p.CPUCT, err = strconv.ParseFloat(v, 64)
		case "fpu":
			p.FPU, err = strconv.ParseFloat(v, 64)
		case "cands":
			p.Cands, err = strconv.Atoi(v)
		case "autopay":
			p.AutoPay = true
		case "manualtaps":
			p.ManualTaps = true
		case "holdupkeep":
			p.HoldUpkeep, p.HoldExempt = true, v
			err = checkHoldExempt(v)
		case "tricks":
			p.Tricks, p.TricksCards = true, v
			err = checkHoldExempt(v)
		case "mull":
			p.Mull = botpolicy.MulliganLands
		case "keep7":
			p.Mull = botpolicy.MulliganNever
		case "topk":
			p.TopK, err = strconv.Atoi(v)
		case "ksims":
			p.KSims = map[decision.Kind]int{}
			for _, kv := range strings.Split(v, ",") {
				k, n, ok := strings.Cut(kv, ".")
				if !ok {
					return nil, fmt.Errorf("policy %q: ksims entry %q: want kind.N", s, kv)
				}
				if p.KSims[decision.Kind(k)], err = strconv.Atoi(n); err != nil {
					break
				}
			}
		case "pcr":
			p.PCR, err = strconv.ParseFloat(v, 64)
		case "cheap":
			cheapSet = true
			p.Cheap, err = strconv.Atoi(v)
		case "pcrk":
			p.PCRK, err = parsePCRK(v)
		case "decided":
			p.Decided, err = floatOr(v, defaultDecided)
		case "decidedn":
			decSet.n = true
			p.DecidedN, err = strconv.Atoi(v)
		case "decidedsims":
			decSet.sims = true
			p.DecSims, err = strconv.Atoi(v)
		case "decidedw":
			decSet.w = true
			p.DecW, err = strconv.ParseFloat(v, 64)
		case "forced":
			p.Forced, err = floatOr(v, defaultForcedK)
		case "oppnodes":
			p.OppNodes = true
		case "oppfull":
			p.OppFull = true
		case "closedlist":
			p.Closed = true
		case "morekinds":
			p.Kinds = azmcts.FullKinds()
		case "kinds":
			p.Kinds, err = azmcts.ParseKinds(v)
		case "skippass":
			p.SkipPass = 1
			if v != "" {
				p.SkipPass, err = strconv.Atoi(v)
			}
		default:
			return nil, fmt.Errorf("policy %q: unknown key %q", s, k)
		}
		if err != nil {
			return nil, fmt.Errorf("policy %q: %s: %w", s, k, err)
		}
	}
	if rest != "" {
		p.Raw, p.Remote = s+":remote="+rest, rest
	}
	if p.Kind == "prior" && p.Net == "" && p.Remote == "" && p.GoNet == "" {
		return nil, fmt.Errorf("policy %q: prior needs net=, remote= or gonet=", s)
	}
	if nets := btoi(p.Net != "") + btoi(p.Remote != "") + btoi(p.GoNet != ""); nets > 1 {
		return nil, fmt.Errorf("policy %q: net=, remote= and gonet= are exclusive", s)
	}
	if p.GoNet != "" {
		m, err := gonet.Model(p.GoNet)
		if err != nil {
			return nil, fmt.Errorf("policy %q: %w", s, err)
		}
		p.model = m
	}
	if p.Remote != "" {
		m, err := remoteModel(p.Remote)
		if err != nil {
			return nil, fmt.Errorf("policy %q: %w", s, err)
		}
		p.model = m
	}
	if p.Net != "" {
		m, err := policynet.LoadCheckpointFile(p.Net)
		if err != nil {
			return nil, fmt.Errorf("policy %q: %w", s, err)
		}
		p.model = m
	}
	if p.Closed && p.Kind != "az" {
		return nil, fmt.Errorf("policy %q: closedlist is an az key", s)
	}
	if p.ManualTaps && !p.searches() {
		return nil, fmt.Errorf("policy %q: manualtaps is an az and prior key", s)
	}
	if p.HoldUpkeep && !p.searches() {
		return nil, fmt.Errorf("policy %q: holdupkeep is an az and prior key", s)
	}
	if p.Tricks && !p.searches() {
		return nil, fmt.Errorf("policy %q: tricks is an az and prior key", s)
	}
	if err := p.checkPCR(cheapSet); err != nil {
		return nil, fmt.Errorf("policy %q: %w", s, err)
	}
	if err := p.checkDecided(decSet); err != nil {
		return nil, fmt.Errorf("policy %q: %w", s, err)
	}
	if err := p.checkForced(); err != nil {
		return nil, fmt.Errorf("policy %q: %w", s, err)
	}
	if p.Kind == "az" || p.Kind == "prior" {
		// Validated open: the closed world's belief is loaded later (setBelief).
		q := *p
		q.Closed = false
		if _, err := q.seat(1); err != nil {
			return nil, fmt.Errorf("policy %q: %w", s, err)
		}
	}
	return p, nil
}

// checkHoldExempt checks holdupkeep='s and tricks='s '|'-separated card names (azmcts Options.HoldUpkeepExempt,
// TricksCards, passed on as written): "" is a bare key, and an empty name (a stray separator) is an error.
func checkHoldExempt(v string) error {
	if v == "" {
		return nil
	}
	for _, n := range strings.Split(v, "|") {
		if strings.TrimSpace(n) == "" {
			return fmt.Errorf("an empty card name in %q (want NAME|NAME...)", v)
		}
	}
	return nil
}

// defaultCheapSims is a cheap search's simulations under pcr= without cheap= (DraftZero's
// proposed arm: a cheap search of 32-64 only moves the game on, so it need not be 100).
const defaultCheapSims = 40

// parsePCRK parses pcrk='s comma list of kind.P (or kind=P) entries. The kind names and the
// ranges are azmcts.SeatConfig's to check (NewSeat, at parse time via policySpec.seat).
func parsePCRK(v string) (map[string]float64, error) {
	m := map[string]float64{}
	for _, kv := range strings.Split(v, ",") {
		k, n, ok := strings.Cut(kv, "=")
		if !ok {
			k, n, ok = strings.Cut(kv, ".")
		}
		if !ok || k == "" {
			return nil, fmt.Errorf("entry %q: want kind.P", kv)
		}
		if _, dup := m[k]; dup {
			return nil, fmt.Errorf("kind %q listed twice", k)
		}
		f, err := strconv.ParseFloat(n, 64)
		if err != nil {
			return nil, fmt.Errorf("entry %q: %w", kv, err)
		}
		m[k] = f
	}
	return m, nil
}

// checkPCR checks the playout-cap keys against each other and the kind (az only) and fills
// cheap='s default; the ranges and kind names are checked by azmcts (validatePCR).
func (p *policySpec) checkPCR(cheapSet bool) error {
	if p.PCR == 0 && !cheapSet && p.PCRK == nil {
		return nil
	}
	switch {
	case p.Kind != "az":
		return fmt.Errorf("pcr=, cheap= and pcrk= are az keys")
	case p.PCR != p.PCR || p.PCR < 0 || p.PCR > 1:
		return fmt.Errorf("pcr=%g: want a full-search probability in [0,1] (0 is off)", p.PCR)
	case p.PCR == 0:
		return fmt.Errorf("cheap= and pcrk= need pcr=P > 0")
	case cheapSet && p.Cheap < 1:
		return fmt.Errorf("cheap=%d: want >= 1 simulations", p.Cheap)
	}
	if !cheapSet {
		p.Cheap = defaultCheapSims
	}
	return nil
}

// The decided-game keys' defaults (research memo D7, KataGo's 0.9 for 3 moves and weight 0.1):
// bare :decided's threshold, the run that starts it, a decided search's simulations and its
// record weight; and bare :forced's k (KataGo's 2).
const (
	defaultDecided       = 0.9
	defaultDecidedN      = 3
	defaultDecidedSims   = 40
	defaultDecidedWeight = 0.1
	defaultForcedK       = 2.0
)

// decidedKeys records which of decidedn=, decidedsims= and decidedw= a spec set.
type decidedKeys struct{ n, sims, w bool }

// floatOr parses a key's value, def for a bare key (no =value).
func floatOr(v string, def float64) (float64, error) {
	if v == "" {
		return def, nil
	}
	return strconv.ParseFloat(v, 64)
}

// checkDecided checks the decided-game keys against each other and the kind (az only) and fills
// the defaults of the ones not set; azmcts checks them again (validateDecided).
func (p *policySpec) checkDecided(set decidedKeys) error {
	if p.Decided == 0 && !set.n && !set.sims && !set.w {
		return nil
	}
	switch {
	case p.Kind != "az":
		return fmt.Errorf("decided=, decidedn=, decidedsims= and decidedw= are az keys")
	case p.Decided == 0:
		return fmt.Errorf("decidedn=, decidedsims= and decidedw= need decided=H")
	case !(p.Decided > 0.5 && p.Decided < 1):
		return fmt.Errorf("decided=%g: want a root-value threshold in (0.5,1) (0 is off)", p.Decided)
	case set.n && p.DecidedN < 1:
		return fmt.Errorf("decidedn=%d: want >= 1 root values", p.DecidedN)
	case set.sims && p.DecSims < 1:
		return fmt.Errorf("decidedsims=%d: want >= 1 simulations", p.DecSims)
	case set.w && !(p.DecW >= 0 && p.DecW <= 1):
		return fmt.Errorf("decidedw=%g: want a record weight in [0,1]", p.DecW)
	}
	if !set.n {
		p.DecidedN = defaultDecidedN
	}
	if !set.sims {
		p.DecSims = defaultDecidedSims
	}
	if !set.w {
		p.DecW = defaultDecidedWeight
	}
	return nil
}

// checkForced checks forced= (az only, finite and >= 0; 0 is off).
func (p *policySpec) checkForced() error {
	switch {
	case p.Forced == 0:
		return nil
	case p.Kind != "az":
		return fmt.Errorf("forced= is an az key")
	case !(p.Forced > 0) || math.IsInf(p.Forced, 1):
		return fmt.Errorf("forced=%g: want a finite k > 0 (0 is off)", p.Forced)
	}
	return nil
}

// recordFeatures is the encoding visit corpora are written in (play -record-features).
var recordFeatures = policynet.FeaturesMZ

// recordEvery keeps one searched decision in K in the corpus (play -record-every).
var recordEvery = 1

func (p *policySpec) searches() bool { return p.Kind == "az" || p.Kind == "prior" }

func (p *policySpec) azConfig() azmcts.SeatConfig {
	cfg := azmcts.DefaultSeatConfig()
	cfg.Search.Sims = p.Sims
	cfg.Search.Kinds = azmcts.AllKinds()
	if p.Kinds != (azmcts.Kinds{}) {
		cfg.Search.Kinds = p.Kinds
	}
	cfg.Search.HeuristicLeaf = p.HeurLeaf
	cfg.Search.UniformPrior = p.UniPrior
	if p.CPUCT >= 0 {
		cfg.Search.CPUCT = p.CPUCT
	}
	if p.FPU >= 0 {
		cfg.Search.FPU = p.FPU
	}
	if p.Cands > 0 {
		cfg.Search.Limit = p.Cands
	}
	cfg.Search.AutoPayment = p.AutoPay
	cfg.ManualTaps = p.ManualTaps
	cfg.Search.HoldUpkeep, cfg.Search.HoldUpkeepExempt = p.HoldUpkeep, p.HoldExempt
	cfg.Search.Tricks, cfg.Search.TricksCards = p.Tricks, p.TricksCards
	cfg.Mulligan = p.Mull
	cfg.Search.PriorTopK = p.TopK
	cfg.SimsByKind = p.KSims
	cfg.FullProb, cfg.CheapSims, cfg.FullProbByKind = p.PCR, p.Cheap, p.PCRK
	cfg.DecidedHigh, cfg.DecidedN, cfg.DecidedSims, cfg.DecidedWeight = p.Decided, p.DecidedN, p.DecSims, p.DecW
	cfg.Search.ForcedK = p.Forced
	cfg.Search.OpponentNodes = p.OppNodes
	cfg.Search.SkipPass = p.SkipPass
	cfg.Search.OpponentMirror = p.OppFull
	// Honest worlds only: the seat never searches the real engine's hidden zones. The
	// prior-only student asks for no world at all; redeal just satisfies NewSeat.
	cfg.World = azmcts.WorldRedeal
	if p.Closed {
		cfg.World, cfg.Belief = azmcts.WorldRedealClosed, p.belief
	}
	cfg.Worlds = p.Worlds
	cfg.Explore, cfg.NoNoise = p.Explore, p.NoNoise
	cfg.PriorOnly = p.Kind == "prior"
	cfg.RecordFeatures = recordFeatures
	return cfg
}

// seat builds one seat of one game from its per-seat seed.
func (p *policySpec) seat(seed uint64) (seat.Seat, error) {
	switch p.Kind {
	case "random":
		return builtins.New(builtins.Uniform, builtins.AutoPay, seed^builtins.UniformSeed), nil
	case "bot":
		b := seat.NewBot(seed)
		if p.AutoPay {
			b = b.EnableAutoPayMana()
		}
		if p.Mull != botpolicy.MulliganCoin {
			b = b.WithMulligan(p.Mull)
		}
		return b, nil
	case "sbtactical":
		t := builtins.NewTactical(builtins.AutoPay, seed, p.lookup, sbtactical.Hosted())
		t.SetMulligan(p.Mull)
		return t, nil
	default:
		return azmcts.NewSeat(seed, p.model, p.azConfig())
	}
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}
