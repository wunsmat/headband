package main

import (
	"encoding/json"
	"io"
	"net"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestRingIsADerangement(t *testing.T) {
	for _, n := range []int{2, 3, 7} {
		for trial := 0; trial < 200; trial++ {
			a := ring(make([]Player, n))
			seen := map[int]bool{}
			for i, tgt := range a {
				if tgt == i {
					t.Fatalf("n=%d: player %d drew themselves", n, i)
				}
				seen[tgt] = true
			}
			if len(seen) != n {
				t.Fatalf("n=%d: %v misses someone", n, a)
			}
		}
	}

	for trial := 0; trial < 200; trial++ {
		a := ring([]Player{{}, {Off: true}, {}, {}})
		if a[1] != -1 || slices.Contains(a, 1) {
			t.Fatalf("dropped player is still in the draw: %v", a)
		}
	}
}

func TestJoinOverSocket(t *testing.T) {
	addr, err := serve("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var last Update
	for _, name := range []string{"ann", "bo"} {
		c, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		if err := json.NewEncoder(c).Encode(Cmd{Cmd: "join", Name: name}); err != nil {
			t.Fatal(err)
		}
		dec := json.NewDecoder(c)
		for len(last.State.Players) < 2 {
			if err := dec.Decode(&last); err != nil {
				t.Fatal(err)
			}
			if name == "ann" {
				break
			}
		}
	}
	if len(last.State.Players) != 2 || last.You != 1 || last.State.Players[0].Name != "ann" {
		t.Fatalf("bad roster: %+v", last)
	}
}

func newHost(names ...string) *host {
	h := &host{state: State{Phase: "lobby"}, conns: map[int]*json.Encoder{}, limit: turnLimit}
	for _, n := range names {
		h.join(n, json.NewEncoder(io.Discard))
	}
	return h
}

func TestDropDuringAssign(t *testing.T) {
	h := newHost("ann", "bo", "cy")
	h.apply(0, Cmd{Cmd: "start"})
	h.apply(0, Cmd{Cmd: "thing", Text: "x"})
	h.apply(1, Cmd{Cmd: "thing", Text: "y"})
	h.leave(2)

	if h.state.Phase != "assign" {
		t.Fatalf("should still be assigning after a redraw: %+v", h.state)
	}
	for _, q := range h.state.Players {
		if q.Thing != "" {
			t.Fatalf("redraw should have cleared things: %+v", h.state.Players)
		}
	}
	h.apply(0, Cmd{Cmd: "thing", Text: "x"})
	h.apply(1, Cmd{Cmd: "thing", Text: "y"})
	if h.state.Phase != "play" {
		t.Fatalf("the two left should be able to play: %+v", h.state)
	}
	if h.state.Turn == 2 {
		t.Fatal("turn landed on the player who left")
	}
}

func TestTurnTimeout(t *testing.T) {
	h := newHost("ann", "bo")
	h.limit = 20 * time.Millisecond
	h.apply(0, Cmd{Cmd: "start"})
	h.apply(0, Cmd{Cmd: "thing", Text: "x"})
	h.apply(1, Cmd{Cmd: "thing", Text: "y"})

	h.mu.Lock()
	deadline := h.state.Deadline
	h.mu.Unlock()
	if deadline == 0 {
		t.Fatal("play started with no deadline")
	}

	time.Sleep(80 * time.Millisecond)

	h.mu.Lock()
	defer h.mu.Unlock()
	if !slices.ContainsFunc(h.state.Log, func(l string) bool { return strings.Contains(l, "ran out of time") }) {
		t.Fatalf("turn never timed out: %v", h.state.Log)
	}
}

func TestTimerStopsOffTurn(t *testing.T) {
	h := newHost("ann", "bo")
	h.limit = 20 * time.Millisecond
	h.apply(0, Cmd{Cmd: "start"})
	h.apply(0, Cmd{Cmd: "thing", Text: "x"})
	h.apply(1, Cmd{Cmd: "thing", Text: "y"})
	h.apply(0, Cmd{Cmd: "restart"})

	time.Sleep(80 * time.Millisecond)

	h.mu.Lock()
	defer h.mu.Unlock()
	if h.state.Phase != "assign" || h.state.Deadline != 0 {
		t.Fatalf("timer still running outside play: %+v", h.state)
	}
	if slices.ContainsFunc(h.state.Log, func(l string) bool { return strings.Contains(l, "ran out of time") }) {
		t.Fatalf("stale timer fired after restart: %v", h.state.Log)
	}
}

func TestClockBar(t *testing.T) {
	at := func(d time.Duration) string {
		return model{state: State{Deadline: time.Now().Add(d).UnixMilli()}}.clock()
	}
	if got := strings.Count(at(turnLimit), "█"); got != barCells {
		t.Fatalf("fresh turn drew %d/%d cells", got, barCells)
	}
	if got := strings.Count(at(-time.Minute), "░"); got != barCells {
		t.Fatalf("expired turn drew %d/%d empty cells", got, barCells)
	}
	if got := strings.Count(at(10*turnLimit), "█"); got != barCells {
		t.Fatalf("a skewed clock overflowed the bar: %d cells", got)
	}
	if (model{}).clock() != "" {
		t.Fatal("no deadline should draw nothing")
	}
}

func TestRestart(t *testing.T) {
	h := newHost("ann", "bo")
	h.apply(0, Cmd{Cmd: "start"})
	h.apply(0, Cmd{Cmd: "thing", Text: "x"})
	h.apply(1, Cmd{Cmd: "thing", Text: "y"})
	h.state.Players[0].Done = true

	h.apply(1, Cmd{Cmd: "restart"})
	if h.state.Phase != "play" {
		t.Fatal("a guest restarted the game")
	}
	h.apply(0, Cmd{Cmd: "restart"})
	if h.state.Phase != "assign" {
		t.Fatalf("host restart should reopen assignment: %+v", h.state)
	}
	for _, q := range h.state.Players {
		if q.Thing != "" || q.Done {
			t.Fatalf("restart should wipe the board: %+v", h.state.Players)
		}
	}
}

func TestGame(t *testing.T) {
	h := newHost("ann", "bo", "cy")

	h.apply(1, Cmd{Cmd: "start"})
	if h.state.Phase != "lobby" {
		t.Fatal("non-host started the game")
	}
	h.apply(0, Cmd{Cmd: "start"})
	if h.state.Phase != "assign" {
		t.Fatal("host could not start")
	}

	things := []string{"Batman", "a duck", "Gandalf"}
	for i := range h.state.Players {
		h.apply(i, Cmd{Cmd: "thing", Text: things[h.state.Assigns[i]]})
		if i < 2 && h.state.Phase != "assign" {
			t.Fatal("started before everyone assigned")
		}
	}
	if h.state.Phase != "play" {
		t.Fatalf("should be playing: %+v", h.state)
	}
	for i, q := range h.state.Players {
		if q.Thing != things[i] {
			t.Fatalf("player %d got %q, wanted %q", i, q.Thing, things[i])
		}
	}
	if h.state.Turn < 0 || h.state.Turn >= 3 {
		t.Fatalf("first player out of range: %d", h.state.Turn)
	}
	h.state.Turn = 0

	h.apply(1, Cmd{Cmd: "guess", Text: things[1]})
	if h.state.Turn != 0 || h.state.Players[1].Done {
		t.Fatal("out-of-turn guess landed")
	}

	h.apply(0, Cmd{Cmd: "guess", Text: "Sauron"})
	if h.state.Players[0].Done || h.state.Turn != 1 {
		t.Fatal("wrong guess should miss and pass the turn")
	}
	h.apply(1, Cmd{Cmd: "skip"})
	if h.state.Turn != 2 {
		t.Fatal("skip should pass the turn")
	}
	h.apply(2, Cmd{Cmd: "guess", Text: "gandalf the grey"})
	if !h.state.Players[2].Done {
		t.Fatal("close-enough guess should count")
	}
	if h.state.Turn != 0 {
		t.Fatal("turn should skip finished players")
	}

	h.apply(0, Cmd{Cmd: "guess", Text: things[0]})
	h.apply(1, Cmd{Cmd: "guess", Text: "duck"})
	if h.state.Phase != "over" {
		t.Fatalf("game should end when everyone is done: %+v", h.state)
	}
}
