// Package runtime implements the node communication framework layer.
//
// peer.go — Peer registry: maintains the list of known peers and their addresses.
// Thread-safe and designed for concurrent read-heavy access.
package runtime

import (
	"fmt"
	"sync"
)

// Peer represents a remote node.
type Peer struct {
	ID   uint16
	Addr string // network address, e.g., "192.168.1.2:9000"
}

// PeerManager manages the set of known remote peers.
type PeerManager struct {
	mu    sync.RWMutex
	peers map[uint16]Peer
}

// NewPeerManager creates an empty PeerManager.
func NewPeerManager() *PeerManager {
	return &PeerManager{peers: make(map[uint16]Peer)}
}

// Add registers (or updates) a peer.
func (pm *PeerManager) Add(id uint16, addr string) error {
	if id == 0 {
		return fmt.Errorf("peer: id 0 is reserved for broadcast")
	}
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.peers[id] = Peer{ID: id, Addr: addr}
	return nil
}

// Remove deletes a peer from the registry.
func (pm *PeerManager) Remove(id uint16) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	delete(pm.peers, id)
}

// Get returns the Peer for id and a boolean indicating whether it was found.
func (pm *PeerManager) Get(id uint16) (Peer, bool) {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	p, ok := pm.peers[id]
	return p, ok
}

// List returns all known peer IDs.
func (pm *PeerManager) List() []uint16 {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	ids := make([]uint16, 0, len(pm.peers))
	for id := range pm.peers {
		ids = append(ids, id)
	}
	return ids
}

// Addrs returns a snapshot map of id → address for all known peers.
func (pm *PeerManager) Addrs() map[uint16]string {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	out := make(map[uint16]string, len(pm.peers))
	for id, p := range pm.peers {
		out[id] = p.Addr
	}
	return out
}
