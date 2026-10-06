package javaws

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// Hub holds the key tree the Java protocol serves and the connected clients
// (Java WS.java): a client registers key paths and gets every key under them,
// then only what changes; a key set to null has been removed, with everything
// under it.
type Hub struct {
	mu      sync.Mutex
	state   Keys
	clients map[*client]struct{}
	nextID  int

	// The tree is rebuilt lazily: only with clients connected, and at most
	// every minInterval, because a whole game is tens of thousands of keys.
	build func() Keys
	dirty bool
	kick  chan struct{}

	// set handles a client's Set; nil refuses them all.
	set func(key string, value any, r *http.Request) error
}

// OnSet lets clients change keys: f is called with the key, the new value
// (nil to clear it) and the client's WebSocket request (who it is), and its
// error is the answer to the client. Keys f refuses stay read-only.
func (h *Hub) OnSet(f func(key string, value any, r *http.Request) error) {
	h.mu.Lock()
	h.set = f
	h.mu.Unlock()
}

const minInterval = 200 * time.Millisecond

type client struct {
	paths []string
	send  chan []byte
	id    string
}

// NewHub starts with an empty tree.
func NewHub() *Hub {
	h := &Hub{state: Keys{}, clients: map[*client]struct{}{}, kick: make(chan struct{}, 1)}
	go h.run()
	return h
}

// Invalidate says the tree has changed; build makes the new one. It is
// called when a client needs it, not now.
func (h *Hub) Invalidate(build func() Keys) {
	h.mu.Lock()
	h.build, h.dirty = build, true
	h.mu.Unlock()
	select {
	case h.kick <- struct{}{}:
	default:
	}
}

func (h *Hub) run() {
	for range h.kick {
		h.refresh()
		time.Sleep(minInterval)
	}
}

// refresh rebuilds the tree if it changed and anyone is listening.
func (h *Hub) refresh() {
	h.mu.Lock()
	build, need := h.build, h.dirty && len(h.clients) > 0
	if need {
		h.dirty = false
	}
	h.mu.Unlock()
	if need && build != nil {
		h.Replace(build())
	}
}

// Replace sets the whole tree (after events, or when the current game
// changes) and sends each client what changed under its paths.
func (h *Hub) Replace(k Keys) {
	h.mu.Lock()
	defer h.mu.Unlock()
	changed := Keys{}
	for key, v := range k {
		if old, ok := h.state[key]; !ok || !equal(old, v) {
			changed[key] = v
		}
	}
	for key := range h.state {
		if _, ok := k[key]; !ok {
			changed[key] = nil
		}
	}
	h.state = k
	h.broadcast(changed)
}

// Update changes some keys (clock ticks) and sends what changed.
func (h *Hub) Update(k Keys) {
	h.mu.Lock()
	defer h.mu.Unlock()
	changed := Keys{}
	for key, v := range k {
		if old, ok := h.state[key]; !ok || !equal(old, v) {
			changed[key] = v
			h.state[key] = v
		}
	}
	h.broadcast(changed)
}

// Apply changes keys as a client receives them: a nil value removes the
// key and every key under it (Java's WS protocol), and each client gets
// what changed under its paths. crgproxy mirrors a scoreboard this way.
func (h *Hub) Apply(k Keys) {
	h.mu.Lock()
	defer h.mu.Unlock()
	changed := Keys{}
	for key, v := range k {
		if v != nil {
			if old, ok := h.state[key]; !ok || !equal(old, v) {
				changed[key] = v
				h.state[key] = v
			}
			continue
		}
		for have := range h.state {
			if have == key || strings.HasPrefix(have, key+".") || strings.HasPrefix(have, key+"(") {
				delete(h.state, have)
				changed[have] = nil
			}
		}
	}
	h.broadcast(changed)
}

// equal compares key values; they are all strings, numbers, booleans or nil.
func equal(a, b any) bool { return a == b }

// broadcast is called with h.mu held.
func (h *Hub) broadcast(changed Keys) {
	if len(changed) == 0 {
		return
	}
	for c := range h.clients {
		if msg := c.message(changed); msg != nil {
			select {
			case c.send <- msg:
			default:
				// Too slow: disconnect; the client reconnects and registers again.
				delete(h.clients, c)
				close(c.send)
			}
		}
	}
}

// message is {"state": {...}} with the keys under the client's paths, or nil.
func (c *client) message(k Keys) []byte {
	out := map[string]any{}
	for key, v := range k {
		for _, p := range c.paths {
			if matches(p, key) {
				out[key] = v
				break
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	b, _ := json.Marshal(map[string]any{"state": out})
	return b
}

// matches says whether a registered path covers a key, as Java's
// PathTrie does: both are split before every "." and "(", the path's parts
// must start the key's, and "(*)" matches any id, dots included. So
// "ScoreBoard.CurrentGame.Team" covers "ScoreBoard.CurrentGame.Team(1).Name".
func matches(path, key string) bool {
	return covers(parts(path), parts(key))
}

func covers(ps, ks []string) bool {
	j := 0
	for _, p := range ps {
		if j >= len(ks) {
			return false
		}
		if p == "(*)" && ks[j][0] == '(' {
			for j < len(ks) && !strings.HasSuffix(ks[j], ")") {
				j++
			}
			j++
			continue
		}
		if p != ks[j] {
			return false
		}
		j++
	}
	return true
}

// parts splits a key before every "." and "(" (Java's split("(?=[.(])")).
func parts(k string) []string {
	var out []string
	start := 0
	for i := 1; i < len(k); i++ {
		if k[i] == '.' || k[i] == '(' {
			out = append(out, k[start:i])
			start = i
		}
	}
	return append(out, k[start:])
}

// ServeHTTP is the /WS/ endpoint.
func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	conn.SetReadLimit(1 << 20)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	h.mu.Lock()
	h.nextID++
	c := &client{send: make(chan []byte, 512), id: fmt.Sprintf("client-%d", h.nextID)}
	h.clients[c] = struct{}{}
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		if _, ok := h.clients[c]; ok {
			delete(h.clients, c)
			close(c.send)
		}
		h.mu.Unlock()
	}()

	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	hello, _ := json.Marshal(map[string]any{"state": map[string]any{
		"WS.Device.Id": c.id, "WS.Device.Name": r.URL.Query().Get("source"), "WS.Client.Id": c.id, "WS.Client.RemoteAddress": host,
	}})
	c.send <- hello

	go func() { // writer
		defer cancel()
		for msg := range c.send {
			wctx, wcancel := context.WithTimeout(ctx, 10*time.Second)
			err := conn.Write(wctx, websocket.MessageText, msg)
			wcancel()
			if err != nil {
				return
			}
		}
		conn.Close(websocket.StatusGoingAway, "too slow")
	}()

	for { // reader
		_, data, err := conn.Read(ctx)
		if err != nil {
			conn.CloseNow()
			return
		}
		var msg struct {
			Action string   `json:"action"`
			Paths  []string `json:"paths"`
			Key    string   `json:"key"`
			Value  any      `json:"value"`
		}
		if json.Unmarshal(data, &msg) != nil {
			continue
		}
		var reply []byte
		switch msg.Action {
		case "Register":
			h.refresh()
			h.mu.Lock()
			reg := &client{paths: msg.Paths}
			reply = reg.message(h.state)
			c.paths = append(c.paths, msg.Paths...)
			h.mu.Unlock()
		case "Ping":
			reply, _ = json.Marshal(map[string]string{"Pong": ""})
		case "Set":
			// Display settings and team colours (the overlay's admin page);
			// the game itself is changed through the API.
			h.mu.Lock()
			set := h.set
			h.mu.Unlock()
			err := errors.New("Not authorized for Set")
			if set != nil {
				err = set(msg.Key, msg.Value, r)
			}
			if err != nil {
				reply, _ = json.Marshal(map[string]string{"authorization": err.Error()})
			}
		default:
			// Changing the game over this protocol isn't supported
			// (docs/engine.md); answer as Java does without permission.
			reply, _ = json.Marshal(map[string]string{"authorization": "Not authorized for " + msg.Action})
		}
		if reply != nil {
			h.mu.Lock()
			if _, ok := h.clients[c]; ok {
				select {
				case c.send <- reply:
				default:
				}
			}
			h.mu.Unlock()
		}
	}
}
