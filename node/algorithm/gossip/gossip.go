// Package gossip implements a push-based anti-entropy gossip algorithm.
//
// Each node periodically selects `Fanout` random peers and pushes its current
// state (a key→value map). Receiving nodes merge incoming state with their own.
// Message deduplication uses a seen-ID cache keyed by (From, Seq).
package gossip

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"sync"
	"time"
	"uav/node/algorithm"
	"uav/pkg/message"
)

const timerGossip = "gossip"

// State is the replicated key-value store each node maintains.
type State map[string]Entry

// Entry holds a single state value with its logical version.
type Entry struct {
	Value   string `json:"v"`
	Version uint64 `json:"ver"` // monotonically increasing; last-write-wins
}

// gossipPayload is the JSON-encoded body of a gossip message.
type gossipPayload struct {
	State State `json:"state"`
}

// Algorithm implements a push gossip protocol.
type Algorithm struct {
	node algorithm.NodeAPI

	mu      sync.RWMutex
	state   State                // local key-value state
	seen    map[string]time.Time // (from:seq) → received-at; for dedup
	seenTTL time.Duration

	fanout   int
	interval time.Duration
	rng      *rand.Rand
}

// New creates a Gossip Algorithm.
// node is the framework NodeAPI; fanout and interval configure gossip aggressiveness.
func New(node algorithm.NodeAPI, fanout int, interval time.Duration) *Algorithm {
	if fanout <= 0 {
		fanout = 3
	}
	if interval <= 0 {
		interval = 200 * time.Millisecond
	}
	return &Algorithm{
		node:     node,
		state:    make(State),
		seen:     make(map[string]time.Time),
		seenTTL:  5 * time.Second,
		fanout:   fanout,
		interval: interval,
		rng:      rand.New(rand.NewSource(node.Now().UnixNano() + int64(node.ID())*12345)),
	}
}

// Set updates a local key→value entry. Version is auto-incremented.
func (a *Algorithm) Set(key, value string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	ver := uint64(1)
	if e, ok := a.state[key]; ok {
		ver = e.Version + 1
	}
	a.state[key] = Entry{Value: value, Version: ver}
}

// Get reads a local key.
func (a *Algorithm) Get(key string) (Entry, bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	e, ok := a.state[key]
	return e, ok
}

// StateSnapshot returns a deep copy of the current state map.
// Useful for external convergence checking without holding the lock.
func (a *Algorithm) StateSnapshot() State {
	a.mu.RLock()
	defer a.mu.RUnlock()
	out := make(State, len(a.state))
	for k, v := range a.state {
		out[k] = v
	}
	return out
}

// Start registers the gossip timer and begins periodic rounds.
func (a *Algorithm) Start() error {
	a.node.SetTimer(timerGossip, a.interval)
	return nil
}

// Stop cancels the gossip timer.
func (a *Algorithm) Stop() {
	a.node.CancelTimer(timerGossip)
}

// OnMessage processes an incoming gossip message and merges remote state.
func (a *Algorithm) OnMessage(msg message.Message) {
	if msg.Type != message.TypeState {
		return
	}

	// Deduplication (belt-and-suspenders in addition to framework layer dedup).
	key := seenKey(msg.From, msg.Seq)
	a.mu.Lock()
	if _, dup := a.seen[key]; dup {
		a.mu.Unlock()
		return
	}
	a.seen[key] = time.Now()
	a.mu.Unlock()

	var p gossipPayload
	if err := json.Unmarshal(msg.Payload, &p); err != nil {
		return
	}
	a.merge(p.State)
}

// OnTick triggers a gossip round when the gossip timer fires.
func (a *Algorithm) OnTick(name string) {
	if name != timerGossip {
		return
	}
	a.evictSeen()
	a.gossipRound()
}

// gossipRound pushes local state to `fanout` randomly selected peers.
func (a *Algorithm) gossipRound() {
	peers := a.node.Peers()
	if len(peers) == 0 {
		return
	}

	a.mu.RLock()
	payload, err := json.Marshal(gossipPayload{State: a.state})
	a.mu.RUnlock()
	if err != nil {
		return
	}

	targets := sampleN(a.rng, peers, a.fanout)
	for _, peerID := range targets {
		_ = a.node.Send(peerID, message.Message{
			Type:      message.TypeState,
			TTL:       message.TTLState,
			Priority:  message.PriorityState,
			Timestamp: a.node.Now().UnixMilli(),
			Payload:   payload,
		})
	}
}

// merge applies last-write-wins for every key in remote.
func (a *Algorithm) merge(remote State) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for k, re := range remote {
		if local, ok := a.state[k]; !ok || re.Version > local.Version {
			a.state[k] = re
		}
	}
}

// evictSeen removes expired entries from the seen cache.
func (a *Algorithm) evictSeen() {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now()
	for k, t := range a.seen {
		if now.Sub(t) > a.seenTTL {
			delete(a.seen, k)
		}
	}
}

func seenKey(from uint16, seq uint32) string {
	// Simple string key: "from:seq"
	return fmt.Sprintf("%d:%d", from, seq)
}

// sampleN returns up to n randomly selected elements from peers.
func sampleN(rng *rand.Rand, peers []uint16, n int) []uint16 {
	if n >= len(peers) {
		return peers
	}
	perm := rng.Perm(len(peers))
	out := make([]uint16, n)
	for i := range out {
		out[i] = peers[perm[i]]
	}
	return out
}
