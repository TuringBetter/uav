// Package config provides unified configuration for all layers of the UAV
// communication framework.
package config

import "time"

// NodeConfig holds configuration for a single node instance.
type NodeConfig struct {
	// NodeID is the unique 16-bit identifier for this node (1–65535; 0 is reserved for broadcast).
	NodeID uint16

	// Transport selects the underlying transport implementation ("udp" or "tcp").
	Transport string

	// ListenAddr is the local address to bind to (e.g., ":9000", "0.0.0.0:9001").
	ListenAddr string

	// Peers maps peer node IDs to their network addresses.
	Peers map[uint16]string

	// SendQueueSize is the capacity of the outbound priority queue.
	SendQueueSize int

	// RecvBufSize is the capacity of the inbound receive channel.
	RecvBufSize int

	// DedupWindowSize controls how many recent sequence numbers are tracked
	// per peer for deduplication.  Default: 256.
	DedupWindowSize int
}

// TCPConfig holds TCP-specific transport configuration.
type TCPConfig struct {
	// DialTimeout is the maximum time to wait when establishing a new TCP connection.
	DialTimeout time.Duration

	// ReconnectBaseDelay is the initial delay for exponential-backoff reconnection.
	ReconnectBaseDelay time.Duration

	// ReconnectMaxDelay caps the exponential backoff delay.
	ReconnectMaxDelay time.Duration

	// MaxConnections is the maximum number of simultaneous outbound connections
	// held in the connection pool.
	MaxConnections int
}

// UDPConfig holds UDP-specific transport configuration.
type UDPConfig struct {
	// ReadBufferSize is the OS-level socket receive buffer size in bytes.
	// 0 means use the system default.
	ReadBufferSize int
}

// GossipConfig holds parameters for the Gossip algorithm.
type GossipConfig struct {
	// Fanout is the number of random peers each gossip round targets.
	Fanout int

	// Interval is how often a gossip round is initiated.
	Interval time.Duration

	// SeenCacheTTL controls how long a seen message ID is retained for deduplication.
	SeenCacheTTL time.Duration
}

// RaftConfig holds parameters for the Raft consensus algorithm.
type RaftConfig struct {
	// ElectionTimeoutMin / Max define the range for the randomised election timeout.
	ElectionTimeoutMin time.Duration
	ElectionTimeoutMax time.Duration

	// HeartbeatInterval is how often the leader sends heartbeats.
	HeartbeatInterval time.Duration
}

// WeakNetConfig holds parameters for the custom WeakNet algorithm.
type WeakNetConfig struct {
	// SyncInterval is how often a Push-Pull sync round is initiated.
	SyncInterval time.Duration

	// Fanout is the number of peers contacted per sync round.
	Fanout int

	// CompressThreshold is the payload size (bytes) above which zlib compression
	// is applied automatically.
	CompressThreshold int

	// RetransmitTimeout is how long to wait for an ACK before retransmitting a
	// priority-0 (collision-avoidance) message.
	RetransmitTimeout time.Duration
}

// DefaultNodeConfig returns a NodeConfig with sensible defaults.
func DefaultNodeConfig(id uint16, listenAddr string) NodeConfig {
	return NodeConfig{
		NodeID:          id,
		Transport:       "udp",
		ListenAddr:      listenAddr,
		Peers:           make(map[uint16]string),
		SendQueueSize:   512,
		RecvBufSize:     256,
		DedupWindowSize: 256,
	}
}

// DefaultTCPConfig returns a TCPConfig with sensible defaults.
func DefaultTCPConfig() TCPConfig {
	return TCPConfig{
		DialTimeout:        3 * time.Second,
		ReconnectBaseDelay: 100 * time.Millisecond,
		ReconnectMaxDelay:  30 * time.Second,
		MaxConnections:     64,
	}
}

// DefaultGossipConfig returns a GossipConfig with sensible defaults.
func DefaultGossipConfig() GossipConfig {
	return GossipConfig{
		Fanout:       3,
		Interval:     200 * time.Millisecond,
		SeenCacheTTL: 5 * time.Second,
	}
}

// DefaultRaftConfig returns a RaftConfig with sensible defaults.
func DefaultRaftConfig() RaftConfig {
	return RaftConfig{
		ElectionTimeoutMin: 150 * time.Millisecond,
		ElectionTimeoutMax: 300 * time.Millisecond,
		HeartbeatInterval:  50 * time.Millisecond,
	}
}

// DefaultWeakNetConfig returns a WeakNetConfig with sensible defaults.
func DefaultWeakNetConfig() WeakNetConfig {
	return WeakNetConfig{
		SyncInterval:      150 * time.Millisecond,
		Fanout:            3,
		CompressThreshold: 256,
		RetransmitTimeout: 200 * time.Millisecond,
	}
}
