package main

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net"
	"slices"
	"strings"
	"sync"
)

type Player struct {
	Name  string `json:"name"`
	Thing string `json:"thing"`
	Done  bool   `json:"done"`
	Off   bool   `json:"off"`
}

type State struct {
	Phase   string   `json:"phase"`
	Turn    int      `json:"turn"`
	Players []Player `json:"players"`
	Assigns []int    `json:"assigns"`
	Log     []string `json:"log"`
}

type Cmd struct {
	Cmd  string `json:"cmd"`
	Name string `json:"name,omitempty"`
	Text string `json:"text,omitempty"`
}

type Update struct {
	State State `json:"state"`
	You   int   `json:"you"`
}

type host struct {
	mu    sync.Mutex
	state State
	conns map[int]*json.Encoder
}

func serve(addr string) (string, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return "", err
	}
	h := &host{state: State{Phase: "lobby"}, conns: map[int]*json.Encoder{}}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go h.client(c)
		}
	}()
	return ln.Addr().String(), nil
}

func (h *host) client(c net.Conn) {
	defer c.Close()
	dec := json.NewDecoder(c)
	me := -1
	for {
		var cmd Cmd
		if err := dec.Decode(&cmd); err != nil {
			break
		}
		if me < 0 {
			if cmd.Cmd != "join" {
				break
			}
			me = h.join(cmd.Name, json.NewEncoder(c))
		} else {
			h.apply(me, cmd)
		}
	}
	if me >= 0 {
		h.leave(me)
	}
}

func (h *host) join(name string, enc *json.Encoder) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	me := len(h.state.Players)
	if name = strings.TrimSpace(name); name == "" {
		name = fmt.Sprintf("player%d", me+1)
	} else if r := []rune(name); len(r) > 16 {
		name = string(r[:16])
	}
	h.state.Players = append(h.state.Players, Player{Name: name})
	h.conns[me] = enc
	h.broadcast()
	return me
}

func live(p []Player) []int {
	ids := make([]int, 0, len(p))
	for i, q := range p {
		if !q.Off {
			ids = append(ids, i)
		}
	}
	return ids
}

func ring(p []Player) []int {
	ids := live(p)
	rand.Shuffle(len(ids), func(i, j int) { ids[i], ids[j] = ids[j], ids[i] })
	a := slices.Repeat([]int{-1}, len(p))
	for i, id := range ids {
		a[id] = ids[(i+1)%len(ids)]
	}
	return a
}

func (h *host) apply(me int, c Cmd) {
	h.mu.Lock()
	defer h.mu.Unlock()
	p := h.state.Players

	switch {
	case c.Cmd == "start" && me == 0 && h.state.Phase == "lobby" && len(live(p)) >= 2:
		h.newRound("Assign phase — everyone name the thing for their target.")

	case c.Cmd == "restart" && me == 0 && h.state.Phase != "lobby" && len(live(p)) >= 2:
		h.state.Log = nil
		h.newRound("New round — assign again.")

	case c.Cmd == "thing" && h.state.Phase == "assign" && c.Text != "" && h.state.Assigns[me] >= 0:
		p[h.state.Assigns[me]].Thing = c.Text
		h.startPlay()

	case (c.Cmd == "guess" || c.Cmd == "skip") && h.state.Phase == "play" && me == h.state.Turn:
		switch {
		case c.Cmd == "skip":
			h.log(p[me].Name + " passed.")
		case match(c.Text, p[me].Thing):
			p[me].Done = true
			h.log(p[me].Name + " got it: " + p[me].Thing + " ✓")
		default:
			h.log(p[me].Name + ` guessed "` + c.Text + `" — nope.`)
		}
		h.nextTurn()

	default:
		return
	}
	h.broadcast()
}

func (h *host) newRound(why string) {
	for i := range h.state.Players {
		h.state.Players[i].Thing, h.state.Players[i].Done = "", false
	}
	h.state.Phase = "assign"
	h.state.Assigns = ring(h.state.Players)
	h.log(why)
}

func (h *host) startPlay() {
	p := h.state.Players
	ids := live(p)
	if len(ids) == 0 || slices.ContainsFunc(p, func(q Player) bool { return q.Thing == "" && !q.Off }) {
		return
	}
	h.state.Phase = "play"
	h.state.Turn = ids[rand.IntN(len(ids))]
	h.log("Game on. " + p[h.state.Turn].Name + " goes first.")
}

func (h *host) leave(me int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.conns, me)
	h.state.Players[me].Off = true
	h.log(h.state.Players[me].Name + " dropped.")
	switch h.state.Phase {
	case "assign":
		if tgt := h.state.Assigns[me]; tgt >= 0 && h.state.Players[tgt].Thing == "" {
			h.newRound("Redrawing — they left before assigning.")
		}
		h.startPlay()
	case "play":
		if h.state.Turn == me {
			h.nextTurn()
		}
	}
	h.broadcast()
}

func (h *host) nextTurn() {
	p := h.state.Players
	if !slices.ContainsFunc(p, func(q Player) bool { return !q.Done && !q.Off }) {
		h.state.Phase = "over"
		h.log("Everyone got it. GG.")
		return
	}
	t := h.state.Turn
	for range p {
		t = (t + 1) % len(p)
		if !p[t].Done && !p[t].Off {
			break
		}
	}
	h.state.Turn = t
}

func (h *host) log(line string) {
	h.state.Log = append(h.state.Log, line)
	if len(h.state.Log) > 8 {
		h.state.Log = h.state.Log[len(h.state.Log)-8:]
	}
}

func (h *host) broadcast() {
	for i, enc := range h.conns {
		if err := enc.Encode(Update{State: h.state, You: i}); err != nil {
			delete(h.conns, i)
		}
	}
}

func match(guess, target string) bool {
	g := strings.ToLower(strings.TrimSpace(guess))
	t := strings.ToLower(strings.TrimSpace(target))
	if g == "" || t == "" {
		return false
	}
	return g == t ||
		(len(g) > 3 && strings.Contains(t, g)) ||
		(len(t) > 3 && strings.Contains(g, t))
}

var localIP = sync.OnceValue(func() string {
	c, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return "127.0.0.1"
	}
	defer c.Close()
	return c.LocalAddr().(*net.UDPAddr).IP.String()
})
