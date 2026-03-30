// Package algorithm defines the pluggable distributed algorithm layer.
//
// Architecture contract:
//   - Algorithms implement the Algorithm interface.
//   - Algorithms interact with the network exclusively through NodeAPI.
//   - Algorithms never import transport packages directly.
package algorithm

import (
	"time"
	"uav/pkg/message"
)

// Algorithm is the interface every pluggable distributed algorithm must implement.
//
// Improvement over spec: OnTick receives the timer name so a single algorithm
// instance can distinguish between multiple independent timers.
type Algorithm interface {
	// Start initialises and begins running the algorithm.
	Start() error

	// Stop gracefully shuts down the algorithm and releases resources.
	Stop()

	// OnMessage is called by the framework layer whenever a valid (non-expired,
	// non-duplicate) message arrives addressed to this node.
	OnMessage(msg message.Message)

	// OnTick is called when a named timer set via NodeAPI.SetTimer fires.
	// name identifies which timer triggered the callback.
	OnTick(name string)
}

// NodeAPI is the capability interface the framework layer exposes to algorithms.
// Algorithms call these methods instead of accessing the network directly.
//
// Improvement over spec: adds CancelTimer and Peers/ID introspection methods.
type NodeAPI interface {
	// ID returns this node's unique identifier.
	ID() uint16

	// Peers returns the current list of known peer node IDs.
	Peers() []uint16

	// PeerAddr returns the network address of the given peer, and whether it exists.
	PeerAddr(peerID uint16) (addr string, ok bool)

	// Send enqueues a unicast message to peerID for delivery.
	// The message is placed in the outbound priority queue.
	Send(peerID uint16, msg message.Message) error

	// Broadcast enqueues msg for delivery to all currently known peers.
	Broadcast(msg message.Message) error

	// SetTimer registers (or resets) a recurring timer identified by name.
	// OnTick(name) will be called every d until CancelTimer(name) is called.
	SetTimer(name string, d time.Duration)

	// CancelTimer stops the named timer.  Calling CancelTimer for an unknown
	// name is a no-op.
	CancelTimer(name string)
}
