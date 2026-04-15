package raft_test

import (
	"sync"
	"testing"
	"time"
	"uav/node/algorithm"
	"uav/node/algorithm/raft"
	"uav/pkg/message"
)

// ─── mock node ───────────────────────────────

type mockNode struct {
	mu    sync.Mutex
	id    uint16
	peers []uint16
	sent  []sentMsg
}

type sentMsg struct {
	to  uint16
	msg message.Message
}

func newMockNode(id uint16, peers []uint16) *mockNode {
	return &mockNode{id: id, peers: peers}
}

func (m *mockNode) ID() uint16 { return m.id }
func (m *mockNode) Peers() []uint16 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.peers
}
func (m *mockNode) PeerAddr(id uint16) (string, bool) { return "", false }
func (m *mockNode) Send(peerID uint16, msg message.Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, sentMsg{to: peerID, msg: msg})
	return nil
}
func (m *mockNode) Now() time.Time                        { return time.Now() }
func (m *mockNode) Broadcast(msg message.Message) error   { return nil }
func (m *mockNode) SetTimer(name string, d time.Duration) {}
func (m *mockNode) CancelTimer(name string)               {}
func (m *mockNode) sentTo(peerID uint16) []message.Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []message.Message
	for _, s := range m.sent {
		if s.to == peerID {
			out = append(out, s.msg)
		}
	}
	return out
}

var _ algorithm.NodeAPI = (*mockNode)(nil)

// TestRaft_InitialState verifies a fresh node starts as Follower.
func TestRaft_InitialState(t *testing.T) {
	node := newMockNode(1, []uint16{2, 3})
	r := raft.New(node, raft.DefaultConfig(), nil)

	if err := r.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	r.Stop()

	if r.IsLeader() {
		t.Error("fresh node should not be leader")
	}
}

// TestRaft_BecomeLeader simulates a 3-node election where node 1 wins.
func TestRaft_BecomeLeader(t *testing.T) {
	node := newMockNode(1, []uint16{2, 3})
	r := raft.New(node, raft.DefaultConfig(), nil)

	if err := r.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer r.Stop()

	// Trigger election manually.
	r.OnTick("raft_election")

	// Simulate receiving vote grants from both peers.
	grantJSON := `{"k":2,"term":1,"granted":true}`
	for _, peer := range []uint16{2, 3} {
		r.OnMessage(message.Message{
			Type:      message.TypeConsensus,
			From:      peer,
			Seq:       1,
			Timestamp: time.Now().UnixMilli(),
			TTL:       message.TTLDefault,
			Payload:   []byte(grantJSON),
		})
	}

	if !r.IsLeader() {
		t.Error("node should be leader after receiving majority votes")
	}
}

// TestRaft_FollowerRejectsOldTerm verifies stale leader heartbeats are rejected.
func TestRaft_FollowerRejectsOldTerm(t *testing.T) {
	node := newMockNode(2, []uint16{1, 3})
	r := raft.New(node, raft.DefaultConfig(), nil)
	if err := r.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer r.Stop()

	// Advance term to 5 by simulating a vote request.
	voteReq := `{"k":1,"term":5,"cid":1,"lli":0,"llt":0}`
	r.OnMessage(message.Message{
		Type:      message.TypeConsensus,
		From:      1,
		Seq:       1,
		Timestamp: time.Now().UnixMilli(),
		TTL:       message.TTLDefault,
		Payload:   []byte(voteReq),
	})

	// Now receive AppendEntries from term 3 (stale).
	staleAE := `{"k":3,"term":3,"lid":1,"pli":0,"plt":0,"entries":[],"lc":0}`
	r.OnMessage(message.Message{
		Type:      message.TypeConsensus,
		From:      1,
		Seq:       2,
		Timestamp: time.Now().UnixMilli(),
		TTL:       message.TTLDefault,
		Payload:   []byte(staleAE),
	})

	// Leader should still be unknown (we rejected the stale AE).
	if r.LeaderID() == 1 {
		t.Error("should not accept leader with stale term")
	}
}

// TestRaft_Propose verifies leader can propose commands.
func TestRaft_Propose(t *testing.T) {
	var applied [][]byte
	var mu sync.Mutex
	cb := func(_ uint32, cmd []byte) {
		mu.Lock()
		defer mu.Unlock()
		applied = append(applied, cmd)
	}

	node := newMockNode(1, []uint16{2, 3})
	r := raft.New(node, raft.DefaultConfig(), cb)
	if err := r.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer r.Stop()

	// Manually become leader.
	r.OnTick("raft_election")
	grant := `{"k":2,"term":1,"granted":true}`
	for _, p := range []uint16{2, 3} {
		r.OnMessage(message.Message{
			Type:      message.TypeConsensus,
			From:      p,
			Seq:       1,
			Payload:   []byte(grant),
			TTL:       message.TTLDefault,
			Timestamp: time.Now().UnixMilli(),
		})
	}

	if !r.Propose([]byte("cmd1")) {
		t.Fatal("Propose should succeed on leader")
	}
}
