package main

// play -declog PATH: a decision log for diagnostics (DraftZero docs/034 §13: instant-speed play and targets). It only
// reads the game; play's output is unchanged. One JSON line per
//
//   - "prio": a priority decision of either seat whose options include casting an instant-speed spell (an instant,
//     or a card with flash) from its hand, or whose answer casts one. With the active player, step, stack, the
//     seat's and its opponent's untapped lands, what it could cast at instant speed and what it chose.
//   - "target": a target decision of either seat: the stack object it targets for (the top of the stack, or the
//     spell being cast) and the chosen targets, each an object (name, controller, zone) or a player.
//   - "stack": each spell and ability on the stack, once, as last seen by a decision (its targets are chosen by
//     then), written when the game ends: controller, kind, source card, the turn and step it was first seen in,
//     whose turn, and its targets.
//   - "card": each card name the log names, once per file: types, keywords and oracle text.
//
// Every line carries pair, leg and game, which join it to the game record (games.jsonl).

import (
	"fmt"
	"strings"
	"sync"

	"github.com/adams-shaun/gorge/cards"
	"github.com/adams-shaun/gorge/decision"
	"github.com/adams-shaun/gorge/internal/policynet"
	"github.com/adams-shaun/gorge/rules"
	"github.com/adams-shaun/gorge/state"
)

var (
	declogPath  string
	declogW     *auditWriter
	declogCards sync.Map // card name -> true once its "card" line is written
)

type dlObj struct {
	ID    uint32 `json:"id"`
	Name  string `json:"name"`
	Ctl   int    `json:"ctl"`
	Zone  string `json:"zone,omitempty"`
	Kind  string `json:"kind,omitempty"` // a stack object's: spell, activated, triggered
	Instm bool   `json:"instant_speed,omitempty"`
	// Combat is a battlefield creature's: attacking or blocking.
	Combat string `json:"combat,omitempty"`
}

type dlTarget struct {
	Player *int   `json:"player,omitempty"`
	Obj    *dlObj `json:"obj,omitempty"`
}

type dlHead struct {
	T      string `json:"t"`
	Game   int    `json:"game"`
	Pair   int    `json:"pair"`
	Leg    int    `json:"leg"`
	Seat   int    `json:"seat"`
	Turn   int32  `json:"turn"`
	Active int    `json:"active"`
	Step   string `json:"step"`
	Seq    uint64 `json:"seq"`
}

type dlPrio struct {
	dlHead
	Stack    []dlObj  `json:"stack"`
	Lands    int      `json:"lands"`     // the seat's untapped lands
	OppLands int      `json:"opp_lands"` // its opponent's
	Hand     int      `json:"hand"`
	Castable []dlObj  `json:"castable"` // instant-speed spells from hand it could cast now
	Choice   string   `json:"choice"`   // the chosen option's kind (pass, cast, play_land, activate, ...), or payment
	Cast     *dlObj   `json:"cast,omitempty"`
	Options  []string `json:"options"` // every option's kind:label
	// Search is the seat's search record for this decision, when it searched (cands index its priority vocabulary:
	// [0] is pass, then the payment casts in engine order).
	Search *auditSide `json:"search,omitempty"`
}

type dlTargetLine struct {
	dlHead
	Prompt  string     `json:"prompt"`
	For     *dlObj     `json:"for,omitempty"` // the top of the stack
	Chosen  []dlTarget `json:"chosen"`
	Options int        `json:"options"`
	// Offered is every option the decision offered: an object (name, controller, zone, combat) or a player.
	Offered []dlTarget `json:"offered"`
}

type dlStackLine struct {
	T         string     `json:"t"`
	Game      int        `json:"game"`
	Pair      int        `json:"pair"`
	Leg       int        `json:"leg"`
	Obj       dlObj      `json:"obj"`
	Turn      int32      `json:"turn"`
	Active    int        `json:"active"`
	Step      string     `json:"step"`
	Depth     int        `json:"depth"`     // the stack's size when first seen, this object included
	Attackers int        `json:"attackers"` // attacking creatures when first seen
	Blockers  int        `json:"blockers"`
	Targets   []dlTarget `json:"targets"`
	SubTarget []dlTarget `json:"sub_targets,omitempty"`
}

type dlCard struct {
	T        string   `json:"t"`
	Name     string   `json:"name"`
	Types    []string `json:"types"`
	Keywords []string `json:"keywords,omitempty"`
	Cost     string   `json:"cost,omitempty"`
	Oracle   string   `json:"oracle,omitempty"`
}

// dlGame is one game's counts of every priority decision per seat (the log's denominators), written at its end.
type dlGame struct {
	T    string              `json:"t"`
	Game int                 `json:"game"`
	Pair int                 `json:"pair"`
	Leg  int                 `json:"leg"`
	N    [2]map[string]int64 `json:"counts"`
}

// decLog is one game's decision log.
type decLog struct {
	jb    job
	order []uint64
	seen  map[uint64]*dlStackLine
	game  dlGame
	last  [2]*policynet.VisitRecord // each seat's latest search record (searchRecorder)
}

// searchRecorder keeps seat's search records for the prio lines (installed only when the game records no corpus).
func (l *decLog) searchRecorder(seat int) func(policynet.VisitRecord) {
	return func(r policynet.VisitRecord) { l.last[seat] = &r }
}

func newDecLog(jb job) *decLog {
	return &decLog{jb: jb, seen: map[uint64]*dlStackLine{},
		game: dlGame{T: "game", Game: jb.game, Pair: jb.pair, Leg: jb.leg, N: [2]map[string]int64{{}, {}}}}
}

// count adds one priority decision of seat to the game's counts, keyed by whose turn it is (own, opp), whether the
// stack is empty, and what it offered: any cast option or payment action, an instant-speed card in hand.
func (l *decLog) count(e *rules.Engine, seat int, d *decision.Decision, payments []decision.PaymentAction, inHand,
	lands, castable int) {
	if seat < 0 || seat > 1 {
		return
	}
	turn := "own"
	if int(e.G.Active) != seat {
		turn = "opp"
	}
	stack := "empty"
	if len(e.G.Stack) > 0 {
		stack = "stack"
	}
	casts, plans := 0, 0
	for _, o := range d.Options {
		if o.Kind == "cast" {
			casts++
		}
	}
	for _, a := range payments {
		if len(a.Plans) > 0 {
			plans++
		}
	}
	n := l.game.N[seat]
	n["prio:"+turn]++
	n["prio:"+turn+":"+stack]++
	if casts > 0 {
		n["prio:"+turn+":cast_options"]++
	}
	if plans > 0 {
		n["prio:"+turn+":payment_actions"]++
	}
	if inHand > 0 {
		n["prio:"+turn+":instant_in_hand"]++
	}
	if castable > 0 {
		n["prio:"+turn+":instant_castable"]++
	}
	n[fmt.Sprintf("prio:%s:lands_up:%d", turn, min(lands, 4))]++
	if inHand > 0 && lands > 0 {
		n["prio:"+turn+":instant_in_hand_lands_up"]++
	}
	for _, o := range d.Options {
		n["prio:"+turn+":opt:"+o.Kind]++
	}
}

func instantSpeed(f *cards.Face) bool {
	if f == nil {
		return false
	}
	if f.IsInstant() {
		return true
	}
	_, flash := f.KeywordParam("Flash")
	return flash
}

func (l *decLog) card(f *cards.Face) {
	if f == nil || f.Name == "" {
		return
	}
	if _, dup := declogCards.LoadOrStore(f.Name, true); dup {
		return
	}
	declogW.write(dlCard{T: "card", Name: f.Name, Types: f.Types, Keywords: f.Keywords, Cost: f.ManaCost,
		Oracle: f.Oracle})
}

// obj describes object id: a stack ability by its source card's name.
func (l *decLog) obj(e *rules.Engine, id state.ObjID) *dlObj {
	o := e.G.Obj(id)
	if o == nil {
		return nil
	}
	d := &dlObj{ID: uint32(id), Ctl: int(o.Controller), Zone: o.Zone.String()}
	f := o.Face()
	if o.Zone == state.ZStack && o.StackKindKnown && o.StackKind != state.StackKindSpell {
		d.Kind = map[state.StackObjKind]string{state.StackKindActivated: "activated",
			state.StackKindTriggered: "triggered"}[o.StackKind]
		if src := e.G.Obj(o.Source); src != nil {
			f = src.Face()
		}
	} else if o.Zone == state.ZStack {
		d.Kind = "spell"
	}
	switch {
	case o.Zone == state.ZBattlefield && o.IsAttacking:
		d.Combat = "attacking"
	case o.Zone == state.ZBattlefield && o.IsBlocking:
		d.Combat = "blocking"
	}
	if f != nil {
		d.Name = f.Name
		d.Instm = instantSpeed(f)
		l.card(f)
	}
	return d
}

func (l *decLog) targets(e *rules.Engine, ts []state.Target) []dlTarget {
	out := make([]dlTarget, 0, len(ts))
	for _, t := range ts {
		if t.IsPlayer {
			p := int(t.Player)
			out = append(out, dlTarget{Player: &p})
			continue
		}
		out = append(out, dlTarget{Obj: l.obj(e, t.Obj)})
	}
	return out
}

func untappedLands(e *rules.Engine, p state.PlayerID) int {
	n := 0
	for _, id := range e.G.Zone(state.ZBattlefield, p) {
		o := e.G.Obj(id)
		if o != nil && !o.Tapped && o.Controller == p {
			if f := o.Face(); f != nil && f.IsLand() {
				n++
			}
		}
	}
	return n
}

// combatants counts the attacking and the blocking creatures on the battlefield.
func combatants(e *rules.Engine) (att, blk int) {
	for p := range e.G.Players {
		for _, id := range e.G.Zone(state.ZBattlefield, state.PlayerID(p)) {
			if o := e.G.Obj(id); o != nil && o.IsAttacking {
				att++
			} else if o != nil && o.IsBlocking {
				blk++
			}
		}
	}
	return att, blk
}

// instantWindow reports whether seat can cast an instant-speed spell from its hand at priority decision d (the live
// engine's pending one): a cast option, or an auto-pay cast with a plan (built if need be: a pure read).
func instantWindow(e *rules.Engine, d *decision.Decision) bool {
	if e == nil || d.Kind != decision.KPriority || instantsInHand(e, d.Player) == 0 {
		return false
	}
	ok := func(id state.ObjID) bool {
		o := e.G.Obj(id)
		return o != nil && o.Zone == state.ZHand && instantSpeed(o.Face())
	}
	for _, o := range d.Options {
		if o.Kind == "cast" && ok(o.Obj) {
			return true
		}
	}
	payments := d.PaymentActions
	if !d.PaymentActionsBuilt {
		payments = e.EnsurePaymentActions()
	}
	for _, a := range payments {
		if len(a.Plans) > 0 && ok(a.Cast.Object) {
			return true
		}
	}
	return false
}

func instantsInHand(e *rules.Engine, p state.PlayerID) int {
	n := 0
	for _, id := range e.G.Zone(state.ZHand, p) {
		if o := e.G.Obj(id); o != nil && instantSpeed(o.Face()) {
			n++
		}
	}
	return n
}

func (l *decLog) head(t string, e *rules.Engine, seat int, d *decision.Decision) dlHead {
	return dlHead{T: t, Game: l.jb.game, Pair: l.jb.pair, Leg: l.jb.leg, Seat: seat, Turn: e.G.Turn,
		Active: int(e.G.Active), Step: e.G.Step.String(), Seq: d.Seq}
}

// castObject is the object an answer casts, if it casts one: a "cast" option, or a payment or announce action.
func castObject(d *decision.Decision, payments []decision.PaymentAction, in decision.Intent) (state.ObjID, bool) {
	action := ""
	switch {
	case in.Payment != nil:
		action = in.Payment.ActionID
	case in.Announce != nil:
		action = in.Announce.ActionID
	}
	if action != "" {
		for _, a := range payments {
			if a.ID == action {
				return a.Cast.Object, true
			}
		}
		return 0, false
	}
	if len(in.Choices) == 0 {
		return 0, false
	}
	for _, o := range d.Options {
		if o.Index == in.Choices[0] && o.Kind == "cast" {
			return o.Obj, true
		}
	}
	return 0, false
}

// observe logs decision d of seat (answered with in), and notes every stack object it can see.
func (l *decLog) observe(e *rules.Engine, seat int, d *decision.Decision, in decision.Intent) {
	for i, id := range e.G.Stack {
		o := e.G.Obj(id)
		if o == nil {
			continue
		}
		key := uint64(id)<<32 | uint64(o.Incarnation)
		s := l.seen[key]
		if s == nil {
			ob := l.obj(e, id)
			if ob == nil {
				continue
			}
			s = &dlStackLine{T: "stack", Game: l.jb.game, Pair: l.jb.pair, Leg: l.jb.leg, Obj: *ob, Turn: e.G.Turn,
				Active: int(e.G.Active), Step: e.G.Step.String(), Depth: i + 1}
			s.Attackers, s.Blockers = combatants(e)
			l.seen[key] = s
			l.order = append(l.order, key)
		}
		if len(o.Targets) > 0 || s.Targets == nil {
			s.Targets = l.targets(e, o.Targets)
		}
		if len(o.SubTargets) > 0 {
			s.SubTarget = l.targets(e, o.SubTargets)
		}
	}
	switch d.Kind {
	case decision.KPriority:
		l.priority(e, seat, d, in)
	case decision.KTarget:
		t := dlTargetLine{dlHead: l.head("target", e, seat, d), Prompt: d.Prompt, Options: len(d.Options)}
		if n := len(e.G.Stack); n > 0 {
			t.For = l.obj(e, e.G.Stack[n-1])
		}
		for _, o := range d.Options {
			if o.Obj != 0 {
				t.Offered = append(t.Offered, dlTarget{Obj: l.obj(e, o.Obj)})
			} else {
				p := int(o.Player)
				t.Offered = append(t.Offered, dlTarget{Player: &p})
			}
		}
		for _, c := range in.Choices {
			for _, o := range d.Options {
				if o.Index != c {
					continue
				}
				if o.Obj != 0 {
					t.Chosen = append(t.Chosen, dlTarget{Obj: l.obj(e, o.Obj)})
				} else {
					p := int(o.Player)
					t.Chosen = append(t.Chosen, dlTarget{Player: &p})
				}
			}
		}
		declogW.write(t)
	}
}

func (l *decLog) priority(e *rules.Engine, seat int, d *decision.Decision, in decision.Intent) {
	p := state.PlayerID(seat)
	inHand, lands := instantsInHand(e, p), untappedLands(e, p)
	// The auto-pay casts (payment actions) are built lazily, and a search builds them on its own copy of the
	// decision, so the live one may not hold them: build them (a pure read of the board) when the answer is one,
	// or when an instant-speed card in hand might be castable.
	payments := d.PaymentActions
	if !d.PaymentActionsBuilt && (in.Payment != nil || in.Announce != nil || (inHand > 0 && lands > 0)) {
		payments = e.EnsurePaymentActions()
	}
	castable, have := []dlObj{}, map[state.ObjID]bool{}
	add := func(id state.ObjID) {
		o := e.G.Obj(id)
		if o == nil || have[id] || o.Zone != state.ZHand || !instantSpeed(o.Face()) {
			return
		}
		have[id] = true
		if ob := l.obj(e, id); ob != nil {
			castable = append(castable, *ob)
		}
	}
	for _, o := range d.Options {
		if o.Kind == "cast" {
			add(o.Obj)
		}
	}
	for _, a := range payments {
		if len(a.Plans) > 0 {
			add(a.Cast.Object)
		}
	}
	l.count(e, seat, d, payments, inHand, lands, len(castable))
	castID, casts := castObject(d, payments, in)
	var cast *dlObj
	if casts {
		cast = l.obj(e, castID)
	}
	if len(castable) == 0 && (cast == nil || !cast.Instm) {
		return
	}
	r := dlPrio{dlHead: l.head("prio", e, seat, d), Lands: lands, OppLands: untappedLands(e, 1-p),
		Hand: len(e.G.Zone(state.ZHand, p)), Castable: castable, Cast: cast}
	for _, id := range e.G.Stack {
		if ob := l.obj(e, id); ob != nil {
			r.Stack = append(r.Stack, *ob)
		}
	}
	switch {
	case in.Payment != nil:
		r.Choice = "payment"
	case in.Announce != nil:
		r.Choice = "announce"
	case len(in.Choices) > 0:
		r.Choice = optionKind(d, in.Choices[0])
	}
	for _, o := range d.Options {
		r.Options = append(r.Options, o.Kind+":"+strings.TrimSpace(o.Label))
	}
	if sr := l.last[seat]; sr != nil && sr.Sequence == d.Seq {
		t := trimRecord(sr)
		r.Search = &t
	}
	declogW.write(r)
}

// finish writes the game's stack objects.
func (l *decLog) finish() {
	for _, k := range l.order {
		declogW.write(l.seen[k])
	}
	declogW.write(l.game)
}
