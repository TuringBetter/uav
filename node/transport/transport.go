// Package transport defines the network transport layer interface.
//
// The transport layer is algorithm-agnostic: it only sends and receives raw
// Message values and knows nothing about routing, consensus, or topology.
// Concrete implementations (UDP, TCP) live in sub-packages.
package transport

import "uav/pkg/message"

// Transport is the minimal interface the network transport layer must satisfy.
// All higher layers program against this interface, never against concrete types.
type Transport interface {
	// Start begins listening for incoming messages on the configured address.
	Start() error

	// Stop shuts down the transport gracefully and releases all resources.
	// After Stop returns, Recv() will not produce further messages.
	Stop() error

	// Send encodes and transmits msg to the peer identified by addr.
	// addr format depends on the concrete implementation (e.g., "192.168.1.2:9000").
	Send(addr string, msg message.Message) error

	// Recv returns a read-only channel that delivers incoming messages.
	// The channel is never closed by the transport; callers should stop reading
	// once Stop() has been called.
	Recv() <-chan message.Message

	// LocalAddr returns the local network address this transport is bound to.
	LocalAddr() string
}
