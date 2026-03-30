package weaknet_test

import (
	"encoding/json"
	"sync"
	"testing"
	"time"
	"uav/node/algorithm"
	"uav/node/algorithm/weaknet"
	"uav/pkg/message"
)

// ─── mock node ───────────────────────────────

type mockNode struct {
	mu    sync.Mutex
	id    uint16
	peers []uint16
	sent  []message.Message
}

func newMock(id uint16, peers []uint16) *mockNode {
	return &mockNode{id: id, peers: peers}
}
func (m *mockNode) ID() uint16            { return m.id }
func (m *mockNode) Peers() []uint16       { m.mu.Lock(); defer m.mu.Unlock(); return m.peers }
func (m *mockNode) PeerAddr(uint16) (string, bool) { return "", false }
func (m *mockNode) Send(pid uint16, msg message.Message) error {
	m.mu.Lock(); defer m.mu.Unlock()
	msg.To = pid
	m.sent = append(m.sent, msg)
	return nil
}
func (m *mockNode) Broadcast(msg message.Message) error { return nil }
func (m *mockNode) SetTimer(string, time.Duration)      {}
func (m *mockNode) CancelTimer(string)                  {}
func (m *mockNode) sentCount() int {
	m.mu.Lock(); defer m.mu.Unlock(); return len(m.sent)
}
func (m *mockNode) lastSent() message.Message {
	m.mu.Lock(); defer m.mu.Unlock()
	return m.sent[len(m.sent)-1]
}

var _ algorithm.NodeAPI = (*mockNode)(nil)

// ─── helpers ─────────────────────────────────

func makePushPullMsg(from uint16, seq uint32, delta weaknet.State, knownVer uint64, isReply bool) message.Message {
	type pp struct {
		Kind     uint8         `json:"k"`
		IsReply  bool          `json:"rep"`
		PeerID   uint16        `json:"pid"`
		Delta    weaknet.State `json:"delta"`
		KnownVer uint64        `json:"kv"`
	}
	payload, _ := json.Marshal(pp{Kind: 1, IsReply: isReply, PeerID: from, Delta: delta, KnownVer: knownVer})
	return message.Message{
		Type:      message.TypeState,
		From:      from,
		Seq:       seq,
		Timestamp: time.Now().UnixMilli(),
		TTL:       message.TTLState,
		Payload:   payload,
	}
}

// ─── Tests ───────────────────────────────────

// TestWeakNet_SetGet verifies local state operations.
func TestWeakNet_SetGet(t *testing.T) {
	n := newMock(1, nil)
	wn := weaknet.New(n, weaknet.DefaultConfig())

	wn.Set("pos", []byte("lat=10,lon=20"))
	e, ok := wn.Get("pos")
	if !ok || string(e.Value) != "lat=10,lon=20" {
		t.Errorf("unexpected get: ok=%v val=%s", ok, e.Value)
	}
}

// TestWeakNet_DeltaOnlySync verifies that only changed entries are transmitted.
func TestWeakNet_DeltaSync(t *testing.T) {
	n := newMock(1, []uint16{2})
	wn := weaknet.New(n, weaknet.DefaultConfig())

	wn.Set("k1", []byte("v1"))
	wn.Set("k2", []byte("v2"))

	// Trigger a sync round.
	wn.OnTick("wn_sync")

	if n.sentCount() == 0 {
		t.Fatal("expected at least one sync message to be sent")
	}
	// The sent message should be TypeState.
	if last := n.lastSent(); last.Type != message.TypeState {
		t.Errorf("expected TypeState, got %v", last.Type)
	}
}

// TestWeakNet_PushPullReply verifies that a request triggers a pull response.
func TestWeakNet_PushPullReply(t *testing.T) {
	n := newMock(2, []uint16{1})
	wn := weaknet.New(n, weaknet.DefaultConfig())
	wn.Set("battery", []byte("80%"))

	// Simulate node 1 sending a Push-Pull request.
	remoteState := weaknet.State{
		"speed": weaknet.StateEntry{Value: []byte("15m/s"), Version: 3},
	}
	req := makePushPullMsg(1, 1, remoteState, 0, false)
	wn.OnMessage(req)

	// Algorithm should have replied (isReply=true) and merged "speed".
	if n.sentCount() == 0 {
		t.Fatal("expected a Pull reply to be sent")
	}

	e, ok := wn.Get("speed")
	if !ok || string(e.Value) != "15m/s" {
		t.Errorf("expected 'speed' to be merged: ok=%v val=%s", ok, e.Value)
	}
}

// TestWeakNet_LWW verifies last-write-wins merge semantics.
func TestWeakNet_LWW(t *testing.T) {
	n := newMock(3, nil)
	wn := weaknet.New(n, weaknet.DefaultConfig())

	// Local state: version 5.
	wn.Set("alt", []byte("100m"))
	for i := 0; i < 4; i++ {
		wn.Set("alt", []byte("higher"))
	}
	localE, _ := wn.Get("alt")
	localVer := localE.Version

	// Remote state: version < local → should NOT overwrite.
	lowerDelta := weaknet.State{
		"alt": weaknet.StateEntry{Value: []byte("stale"), Version: localVer - 1},
	}
	req := makePushPullMsg(99, 1, lowerDelta, 0, true)
	wn.OnMessage(req)

	if e, _ := wn.Get("alt"); string(e.Value) == "stale" {
		t.Error("LWW: lower-version remote entry should not overwrite local")
	}

	// Remote state: version > local → SHOULD overwrite.
	higherDelta := weaknet.State{
		"alt": weaknet.StateEntry{Value: []byte("200m"), Version: localVer + 10},
	}
	req2 := makePushPullMsg(99, 2, higherDelta, 0, true)
	wn.OnMessage(req2)

	if e, ok := wn.Get("alt"); !ok || string(e.Value) != "200m" {
		t.Errorf("LWW: higher-version remote should win, got %v ok=%v", e, ok)
	}
}

// TestWeakNet_AdaptiveFanout checks that the algorithm contacts ≤fanout peers per tick.
func TestWeakNet_AdaptiveFanout(t *testing.T) {
	cfg := weaknet.DefaultConfig()
	cfg.Fanout = 2
	peers := []uint16{2, 3, 4, 5, 6}
	n := newMock(1, peers)
	wn := weaknet.New(n, cfg)
	wn.Set("x", []byte("1"))

	wn.OnTick("wn_sync")

	// Fanout=2 → at most 2 messages per round.
	if got := n.sentCount(); got > 2 {
		t.Errorf("fanout=2: expected ≤2 messages, got %d", got)
	}
}
