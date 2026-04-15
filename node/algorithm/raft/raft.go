// Package raft implements a simplified Raft consensus algorithm.
//
// Supported features:
//   - Randomised leader election with term tracking
//   - Heartbeat-based leader authority maintenance
//   - Basic log replication (AppendEntries)
//
// Not implemented (out of scope for prototype):
//   - Log persistence / crash recovery
//   - Snapshot & log compaction
//   - Cluster membership changes
package raft

import (
	"encoding/json"
	"math/rand"
	"sync"
	"time"
	"uav/node/algorithm"
	"uav/pkg/message"
)

// ─── Timer names ─────────────────────────────
const (
	timerElection  = "raft_election"
	timerHeartbeat = "raft_heartbeat"
)

// ─── Raft message kinds (encoded in Payload) ─
type raftKind uint8

const (
	kindRequestVote      raftKind = 1
	kindRequestVoteReply raftKind = 2
	kindAppendEntries    raftKind = 3
	kindAppendReply      raftKind = 4
)

// nodeState represents the three Raft roles.
type nodeState uint8

const (
	stateFollower  nodeState = 0
	stateCandidate nodeState = 1
	stateLeader    nodeState = 2
)

// LogEntry is a single entry in the Raft replicated log.
type LogEntry struct {
	Term    uint32 `json:"term"`
	Command []byte `json:"cmd"` // opaque application command
}

// ─── Wire message payloads ────────────────────

type requestVote struct {
	Kind         raftKind `json:"k"`
	Term         uint32   `json:"term"`
	CandidateID  uint16   `json:"cid"`
	LastLogIndex uint32   `json:"lli"`
	LastLogTerm  uint32   `json:"llt"`
}

type requestVoteReply struct {
	Kind        raftKind `json:"k"`
	Term        uint32   `json:"term"`
	VoteGranted bool     `json:"granted"`
}

type appendEntries struct {
	Kind         raftKind   `json:"k"`
	Term         uint32     `json:"term"`
	LeaderID     uint16     `json:"lid"`
	PrevLogIndex uint32     `json:"pli"`
	PrevLogTerm  uint32     `json:"plt"`
	Entries      []LogEntry `json:"entries"`
	LeaderCommit uint32     `json:"lc"`
}

type appendReply struct {
	Kind    raftKind `json:"k"`
	Term    uint32   `json:"term"`
	Success bool     `json:"ok"`
	// MatchIndex is the last log index the follower successfully replicated.
	MatchIndex uint32 `json:"mi"`
}

// ─── Algorithm ───────────────────────────────

// Algorithm is a Raft consensus implementation.
type Algorithm struct {
	node algorithm.NodeAPI

	cfg RaftConfig

	mu sync.Mutex

	// Persistent state (would be written to stable storage in production).
	currentTerm uint32
	votedFor    uint16 // 0 = none
	log         []LogEntry

	// Volatile state.
	state       nodeState
	commitIndex uint32
	lastApplied uint32
	leaderID    uint16

	// Leader-only volatile state.
	nextIndex  map[uint16]uint32
	matchIndex map[uint16]uint32

	// Election state.
	votesReceived int
	rng           *rand.Rand

	// Applied command callback (optional).
	applyCb func(index uint32, cmd []byte)
}

// RaftConfig holds tunable parameters.
type RaftConfig struct {
	ElectionTimeoutMin time.Duration
	ElectionTimeoutMax time.Duration
	HeartbeatInterval  time.Duration
}

// DefaultConfig returns sensible Raft defaults.
func DefaultConfig() RaftConfig {
	return RaftConfig{
		ElectionTimeoutMin: 150 * time.Millisecond,
		ElectionTimeoutMax: 300 * time.Millisecond,
		HeartbeatInterval:  50 * time.Millisecond,
	}
}

// New creates a Raft Algorithm instance.
// applyCb is invoked on the leader when a log entry is committed (may be nil).
func New(node algorithm.NodeAPI, cfg RaftConfig, applyCb func(uint32, []byte)) *Algorithm {
	return &Algorithm{
		node:       node,
		cfg:        cfg,
		state:      stateFollower,
		nextIndex:  make(map[uint16]uint32),
		matchIndex: make(map[uint16]uint32),
		rng:        rand.New(rand.NewSource(node.Now().UnixNano() + int64(node.ID())*9999999)),
		applyCb:    applyCb,
	}
}

// Start begins Raft by setting the election timer.
func (a *Algorithm) Start() error {
	a.resetElectionTimer()
	return nil
}

// Stop cancels all Raft timers.
func (a *Algorithm) Stop() {
	a.node.CancelTimer(timerElection)
	a.node.CancelTimer(timerHeartbeat)
}

// Propose submits a command to the Raft cluster (only effective on the leader).
// Returns false if this node is not the leader.
func (a *Algorithm) Propose(cmd []byte) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.state != stateLeader {
		return false
	}
	a.log = append(a.log, LogEntry{Term: a.currentTerm, Command: cmd})
	a.broadcastAppendEntries()
	return true
}

// IsLeader reports whether this node currently believes itself to be the leader.
func (a *Algorithm) IsLeader() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.state == stateLeader
}

// LeaderID returns the last known leader ID (0 if unknown).
func (a *Algorithm) LeaderID() uint16 {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.leaderID
}

// RaftStatus holds observable state for external inspection.
type RaftStatus struct {
	Role        string // "follower", "candidate", "leader"
	Term        uint32
	LeaderID    uint16
	LogLen      int
	CommitIndex uint32
	LastApplied uint32
}

// StatusSnapshot returns a snapshot of the Raft node's current state.
func (a *Algorithm) StatusSnapshot() RaftStatus {
	a.mu.Lock()
	defer a.mu.Unlock()
	role := "follower"
	switch a.state {
	case stateCandidate:
		role = "candidate"
	case stateLeader:
		role = "leader"
	}
	return RaftStatus{
		Role:        role,
		Term:        a.currentTerm,
		LeaderID:    a.leaderID,
		LogLen:      len(a.log),
		CommitIndex: a.commitIndex,
		LastApplied: a.lastApplied,
	}
}

// CommitIndex returns the current commit index.
func (a *Algorithm) CommitIndex() uint32 {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.commitIndex
}

// LogLen returns the current log length.
func (a *Algorithm) LogLen() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.log)
}

// ─── Algorithm interface ─────────────────────

func (a *Algorithm) OnMessage(msg message.Message) {
	if msg.Type != message.TypeConsensus || len(msg.Payload) == 0 {
		return
	}

	// Peek at the kind byte to dispatch.
	var envelope struct {
		Kind raftKind `json:"k"`
	}
	if err := json.Unmarshal(msg.Payload, &envelope); err != nil {
		return
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	switch envelope.Kind {
	case kindRequestVote:
		var rv requestVote
		if err := json.Unmarshal(msg.Payload, &rv); err != nil {
			return
		}
		a.handleRequestVote(msg.From, rv)
	case kindRequestVoteReply:
		var rvr requestVoteReply
		if err := json.Unmarshal(msg.Payload, &rvr); err != nil {
			return
		}
		a.handleRequestVoteReply(rvr)
	case kindAppendEntries:
		var ae appendEntries
		if err := json.Unmarshal(msg.Payload, &ae); err != nil {
			return
		}
		a.handleAppendEntries(msg.From, ae)
	case kindAppendReply:
		var ar appendReply
		if err := json.Unmarshal(msg.Payload, &ar); err != nil {
			return
		}
		a.handleAppendReply(msg.From, ar)
	}
}

func (a *Algorithm) OnTick(name string) {
	switch name {
	case timerElection:
		a.mu.Lock()
		a.startElection()
		a.mu.Unlock()
	case timerHeartbeat:
		a.mu.Lock()
		if a.state == stateLeader {
			a.broadcastAppendEntries()
		}
		a.mu.Unlock()
	}
}

// ─── Internal Raft logic ─────────────────────

func (a *Algorithm) resetElectionTimer() {
	d := a.cfg.ElectionTimeoutMin + time.Duration(a.rng.Int63n(
		int64(a.cfg.ElectionTimeoutMax-a.cfg.ElectionTimeoutMin),
	))
	a.node.SetTimer(timerElection, d)
}

func (a *Algorithm) stepDown(term uint32) {
	a.currentTerm = term
	a.state = stateFollower
	a.votedFor = 0
	a.node.CancelTimer(timerHeartbeat)
	a.resetElectionTimer()
}

func (a *Algorithm) startElection() {
	a.state = stateCandidate
	a.currentTerm++
	a.votedFor = a.node.ID()
	a.votesReceived = 1 // vote for self
	a.resetElectionTimer()

	lastIdx, lastTerm := a.lastLogInfo()
	payload, _ := json.Marshal(requestVote{
		Kind:         kindRequestVote,
		Term:         a.currentTerm,
		CandidateID:  a.node.ID(),
		LastLogIndex: lastIdx,
		LastLogTerm:  lastTerm,
	})
	a.sendToAll(message.TypeConsensus, message.TTLDefault, message.PriorityState, payload)
}

func (a *Algorithm) becomeLeader() {
	a.state = stateLeader
	a.leaderID = a.node.ID()
	a.node.CancelTimer(timerElection)

	// Initialise leader tracking indices.
	nextIdx := uint32(len(a.log) + 1)
	for _, p := range a.node.Peers() {
		a.nextIndex[p] = nextIdx
		a.matchIndex[p] = 0
	}
	a.node.SetTimer(timerHeartbeat, a.cfg.HeartbeatInterval)
	a.broadcastAppendEntries()
}

func (a *Algorithm) broadcastAppendEntries() {
	for _, p := range a.node.Peers() {
		a.sendAppendEntries(p)
	}
}

func (a *Algorithm) sendAppendEntries(peerID uint16) {
	nextIdx := a.nextIndex[peerID]
	prevIdx := uint32(0)
	prevTerm := uint32(0)
	if nextIdx > 1 {
		prevIdx = nextIdx - 1
		if int(prevIdx) <= len(a.log) {
			prevTerm = a.log[prevIdx-1].Term
		}
	}
	entries := []LogEntry{}
	if int(nextIdx)-1 < len(a.log) {
		entries = a.log[nextIdx-1:]
	}
	payload, _ := json.Marshal(appendEntries{
		Kind:         kindAppendEntries,
		Term:         a.currentTerm,
		LeaderID:     a.node.ID(),
		PrevLogIndex: prevIdx,
		PrevLogTerm:  prevTerm,
		Entries:      entries,
		LeaderCommit: a.commitIndex,
	})
	_ = a.node.Send(peerID, message.Message{
		Type:      message.TypeConsensus,
		TTL:       message.TTLDefault,
		Priority:  message.PriorityState,
		Timestamp: a.node.Now().UnixMilli(),
		Payload:   payload,
	})
}

func (a *Algorithm) handleRequestVote(from uint16, rv requestVote) {
	if rv.Term > a.currentTerm {
		a.stepDown(rv.Term)
	}

	reply := requestVoteReply{Kind: kindRequestVoteReply, Term: a.currentTerm}

	lastIdx, lastTerm := a.lastLogInfo()
	logOK := rv.LastLogTerm > lastTerm ||
		(rv.LastLogTerm == lastTerm && rv.LastLogIndex >= lastIdx)
	termOK := rv.Term == a.currentTerm
	notVoted := a.votedFor == 0 || a.votedFor == rv.CandidateID

	if termOK && logOK && notVoted {
		a.votedFor = rv.CandidateID
		reply.VoteGranted = true
		a.resetElectionTimer()
	}

	payload, _ := json.Marshal(reply)
	_ = a.node.Send(from, message.Message{
		Type:      message.TypeConsensus,
		TTL:       message.TTLDefault,
		Priority:  message.PriorityState,
		Timestamp: a.node.Now().UnixMilli(),
		Payload:   payload,
	})
}

func (a *Algorithm) handleRequestVoteReply(rvr requestVoteReply) {
	if rvr.Term > a.currentTerm {
		a.stepDown(rvr.Term)
		return
	}
	if a.state != stateCandidate || rvr.Term != a.currentTerm {
		return
	}
	if rvr.VoteGranted {
		a.votesReceived++
		majority := len(a.node.Peers())/2 + 1
		if a.votesReceived >= majority {
			a.becomeLeader()
		}
	}
}

func (a *Algorithm) handleAppendEntries(from uint16, ae appendEntries) {
	reply := appendReply{Kind: kindAppendReply, Term: a.currentTerm}

	if ae.Term < a.currentTerm {
		payload, _ := json.Marshal(reply)
		_ = a.node.Send(from, message.Message{
			Type: message.TypeConsensus, Payload: payload,
			Timestamp: a.node.Now().UnixMilli(),
		})
		return
	}

	// Valid leader contact — become follower, reset election timer.
	if ae.Term > a.currentTerm {
		a.stepDown(ae.Term)
	} else {
		a.state = stateFollower
		a.resetElectionTimer()
	}
	a.leaderID = ae.LeaderID

	// Log consistency check.
	if ae.PrevLogIndex > 0 {
		if uint32(len(a.log)) < ae.PrevLogIndex ||
			a.log[ae.PrevLogIndex-1].Term != ae.PrevLogTerm {
			payload, _ := json.Marshal(reply)
			_ = a.node.Send(from, message.Message{
				Type: message.TypeConsensus, Payload: payload,
				Timestamp: a.node.Now().UnixMilli(),
			})
			return
		}
	}

	// Append new entries.
	insertIdx := ae.PrevLogIndex
	for i, entry := range ae.Entries {
		idx := insertIdx + uint32(i)
		if uint32(len(a.log)) > idx {
			if a.log[idx].Term != entry.Term {
				a.log = a.log[:idx] // truncate conflicting entries
			} else {
				continue
			}
		}
		a.log = append(a.log, entry)
	}

	if ae.LeaderCommit > a.commitIndex {
		if ae.LeaderCommit < uint32(len(a.log)) {
			a.commitIndex = ae.LeaderCommit
		} else {
			a.commitIndex = uint32(len(a.log))
		}
		a.applyCommitted()
	}

	reply.Success = true
	reply.MatchIndex = uint32(len(a.log))
	payload, _ := json.Marshal(reply)
	_ = a.node.Send(from, message.Message{
		Type: message.TypeConsensus, Payload: payload,
		Timestamp: a.node.Now().UnixMilli(),
	})
}

func (a *Algorithm) handleAppendReply(from uint16, ar appendReply) {
	if ar.Term > a.currentTerm {
		a.stepDown(ar.Term)
		return
	}
	if a.state != stateLeader {
		return
	}
	if ar.Success {
		a.matchIndex[from] = ar.MatchIndex
		a.nextIndex[from] = ar.MatchIndex + 1
		a.maybeCommit()
	} else {
		// Back off nextIndex and retry.
		if a.nextIndex[from] > 1 {
			a.nextIndex[from]--
		}
		a.sendAppendEntries(from)
	}
}

func (a *Algorithm) maybeCommit() {
	// Advance commitIndex to the highest index replicated on a majority.
	for n := uint32(len(a.log)); n > a.commitIndex; n-- {
		if a.log[n-1].Term != a.currentTerm {
			break
		}
		count := 1 // self
		for _, mi := range a.matchIndex {
			if mi >= n {
				count++
			}
		}
		peers := a.node.Peers()
		if count > (len(peers)+1)/2 {
			a.commitIndex = n
			a.applyCommitted()
			break
		}
	}
}

func (a *Algorithm) applyCommitted() {
	for a.lastApplied < a.commitIndex {
		a.lastApplied++
		if a.applyCb != nil {
			entry := a.log[a.lastApplied-1]
			a.applyCb(a.lastApplied, entry.Command)
		}
	}
}

func (a *Algorithm) lastLogInfo() (index, term uint32) {
	if len(a.log) == 0 {
		return 0, 0
	}
	last := a.log[len(a.log)-1]
	return uint32(len(a.log)), last.Term
}

func (a *Algorithm) sendToAll(msgType message.MessageType, ttl uint16, pri message.Priority, payload []byte) {
	for _, p := range a.node.Peers() {
		_ = a.node.Send(p, message.Message{
			Type:      msgType,
			TTL:       ttl,
			Priority:  pri,
			Timestamp: time.Now().UnixMilli(),
			Payload:   payload,
		})
	}
}
