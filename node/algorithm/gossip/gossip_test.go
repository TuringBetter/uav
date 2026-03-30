package gossip_test

import (
	"sync"
	"testing"
	"time"
	"uav/node/algorithm"
	"uav/node/algorithm/gossip"
	"uav/pkg/message"
)

// mockNodeAPI is a lightweight stand-in for algorithm.NodeAPI.
type mockNodeAPI struct {
	mu      sync.Mutex
	id      uint16
	peers   []uint16
	sent    []message.Message
	timers  map[string]time.Duration
	tickCb  func(string)
}

func newMockNode(id uint16, peers []uint16) *mockNodeAPI {
	return &mockNodeAPI{id: id, peers: peers, timers: make(map[string]time.Duration)}
}

func (m *mockNodeAPI) ID() uint16   { return m.id }
func (m *mockNodeAPI) Peers() []uint16 {
	m.mu.Lock(); defer m.mu.Unlock(); return m.peers
}
func (m *mockNodeAPI) PeerAddr(id uint16) (string, bool) { return "", false }
func (m *mockNodeAPI) Send(peerID uint16, msg message.Message) error {
	m.mu.Lock(); defer m.mu.Unlock()
	msg.To = peerID
	m.sent = append(m.sent, msg)
	return nil
}
func (m *mockNodeAPI) Broadcast(msg message.Message) error { return nil }
func (m *mockNodeAPI) SetTimer(name string, d time.Duration) {
	m.mu.Lock(); defer m.mu.Unlock()
	m.timers[name] = d
}
func (m *mockNodeAPI) CancelTimer(name string) {
	m.mu.Lock(); defer m.mu.Unlock()
	delete(m.timers, name)
}
func (m *mockNodeAPI) sentCount() int {
	m.mu.Lock(); defer m.mu.Unlock(); return len(m.sent)
}

var _ algorithm.NodeAPI = (*mockNodeAPI)(nil)

// TestGossip_SetGet verifies local state reads and writes.
func TestGossip_SetGet(t *testing.T) {
	node := newMockNode(1, []uint16{2, 3})
	g := gossip.New(node, 2, 100*time.Millisecond)

	g.Set("altitude", "100m")
	e, ok := g.Get("altitude")
	if !ok {
		t.Fatal("expected key 'altitude' to exist")
	}
	if e.Value != "100m" {
		t.Errorf("got %q, want %q", e.Value, "100m")
	}
}

// TestGossip_VersionIncrement confirms version auto-increments on each Set.
func TestGossip_VersionIncrement(t *testing.T) {
	node := newMockNode(1, nil)
	g := gossip.New(node, 2, 100*time.Millisecond)

	g.Set("k", "v1")
	e1, _ := g.Get("k")
	g.Set("k", "v2")
	e2, _ := g.Get("k")

	if e2.Version <= e1.Version {
		t.Errorf("version should increase: v1=%d v2=%d", e1.Version, e2.Version)
	}
}

// TestGossip_GossipRound verifies that a tick sends to ≤fanout peers.
func TestGossip_GossipRound(t *testing.T) {
	peers := []uint16{2, 3, 4, 5}
	node := newMockNode(1, peers)
	g := gossip.New(node, 2, 100*time.Millisecond)
	g.Set("x", "1")

	if err := g.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer g.Stop()

	g.OnTick("gossip") // manually trigger one round

	if got := node.sentCount(); got > 2 {
		t.Errorf("fanout=2 should send to at most 2 peers, sent %d", got)
	}
	if got := node.sentCount(); got == 0 {
		t.Error("expected at least one message to be sent")
	}
}

// TestGossip_MergeOnMessage verifies that remote state is merged correctly.
func TestGossip_MergeOnMessage(t *testing.T) {
	import_node := newMockNode(2, nil)
	g := gossip.New(import_node, 2, time.Second)
	g.Set("local_key", "local_val")

	// Simulate receiving a gossip message from node 1 with a higher-version entry.
	import_json := `{"state":{"local_key":{"v":"remote_updated","ver":10},"new_key":{"v":"new","ver":1}}}`
	msg := message.Message{
		Type:      message.TypeState,
		From:      1,
		Seq:       1,
		Timestamp: time.Now().UnixMilli(),
		TTL:       message.TTLState,
		Payload:   []byte(import_json),
	}
	g.OnMessage(msg)

	// "local_key" should be updated because remote version (10) > local (1).
	if e, ok := g.Get("local_key"); !ok || e.Value != "remote_updated" {
		t.Errorf("expected 'remote_updated', got %v ok=%v", e, ok)
	}
	// "new_key" should be added.
	if _, ok := g.Get("new_key"); !ok {
		t.Error("expected 'new_key' to be merged")
	}
}
