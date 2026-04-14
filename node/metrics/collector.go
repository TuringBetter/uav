// Package metrics provides real-time performance metrics collection for the
// UAV distributed communication framework.
//
// The collector is designed for high-frequency, concurrent access using atomic
// counters.  It integrates with the runtime layer via hook functions, requiring
// zero changes to the Algorithm interface.
package metrics

import (
	"sync"
	"sync/atomic"
	"time"
	"uav/pkg/message"
)

// latencyRingSize is the capacity of the ring buffer that stores recent
// end-to-end message latencies (in milliseconds).
const latencyRingSize = 4096

// Collector aggregates per-node communication and synchronisation metrics.
// All public Record* methods are safe for concurrent use.
type Collector struct {
	nodeID    uint16
	startTime time.Time

	// ── Communication overhead ──────────────────────────────────────────
	msgSent   atomic.Uint64
	msgRecv   atomic.Uint64
	bytesSent atomic.Uint64
	bytesRecv atomic.Uint64

	// Per message-type counters (indexed by MessageType 0-4).
	msgSentByType [5]atomic.Uint64
	msgRecvByType [5]atomic.Uint64

	// ── Drop / error counters ───────────────────────────────────────────
	dedupDropped      atomic.Uint64
	ttlExpiredDropped atomic.Uint64
	queueOverflowDrop atomic.Uint64
	sendErrors        atomic.Uint64

	// ── Compression tracking ────────────────────────────────────────────
	compressedBytes   atomic.Uint64
	uncompressedBytes atomic.Uint64

	// ── Latency ring buffer ─────────────────────────────────────────────
	latMu   sync.Mutex
	latBuf  []int64 // ring buffer of latencies in ms
	latPos  int     // next write position
	latFull bool    // true once the buffer has wrapped around
}

// NewCollector creates a Collector for the given node.
func NewCollector(nodeID uint16) *Collector {
	return &Collector{
		nodeID:    nodeID,
		startTime: time.Now(),
		latBuf:    make([]int64, latencyRingSize),
	}
}

// ── Record methods ──────────────────────────────────────────────────────

// RecordSend records an outbound message and its wire size in bytes.
func (c *Collector) RecordSend(msg message.Message, wireBytes int) {
	c.msgSent.Add(1)
	c.bytesSent.Add(uint64(wireBytes))
	if int(msg.Type) < len(c.msgSentByType) {
		c.msgSentByType[msg.Type].Add(1)
	}
}

// RecordRecv records an inbound message and its wire size in bytes.
// It also captures end-to-end latency based on the message timestamp.
func (c *Collector) RecordRecv(msg message.Message, wireBytes int) {
	c.msgRecv.Add(1)
	c.bytesRecv.Add(uint64(wireBytes))
	if int(msg.Type) < len(c.msgRecvByType) {
		c.msgRecvByType[msg.Type].Add(1)
	}
	// Record latency if the message carries a valid timestamp.
	if msg.Timestamp > 0 {
		latency := time.Now().UnixMilli() - msg.Timestamp
		if latency >= 0 {
			c.recordLatency(latency)
		}
	}
}

// RecordDedupDrop records a message dropped by the deduplication filter.
func (c *Collector) RecordDedupDrop() {
	c.dedupDropped.Add(1)
}

// RecordTTLDrop records a message dropped due to TTL expiry.
func (c *Collector) RecordTTLDrop() {
	c.ttlExpiredDropped.Add(1)
}

// RecordQueueDrop records a message evicted from the send queue due to overflow.
func (c *Collector) RecordQueueDrop() {
	c.queueOverflowDrop.Add(1)
}

// RecordSendError records a transport-level send failure.
func (c *Collector) RecordSendError() {
	c.sendErrors.Add(1)
}

// RecordCompression records pre- and post-compression sizes for a single
// message payload so that we can compute the overall compression ratio.
func (c *Collector) RecordCompression(uncompressed, compressed int) {
	c.uncompressedBytes.Add(uint64(uncompressed))
	c.compressedBytes.Add(uint64(compressed))
}

// recordLatency appends a latency sample to the ring buffer.
func (c *Collector) recordLatency(ms int64) {
	c.latMu.Lock()
	c.latBuf[c.latPos] = ms
	c.latPos++
	if c.latPos >= latencyRingSize {
		c.latPos = 0
		c.latFull = true
	}
	c.latMu.Unlock()
}

// latencies returns a copy of all recorded latency samples in order.
func (c *Collector) latencies() []int64 {
	c.latMu.Lock()
	defer c.latMu.Unlock()

	var n int
	if c.latFull {
		n = latencyRingSize
	} else {
		n = c.latPos
	}
	if n == 0 {
		return nil
	}

	out := make([]int64, n)
	if c.latFull {
		// Ring has wrapped: read from latPos..end, then 0..latPos.
		k := copy(out, c.latBuf[c.latPos:])
		copy(out[k:], c.latBuf[:c.latPos])
	} else {
		copy(out, c.latBuf[:c.latPos])
	}
	return out
}

// Reset zeroes all counters and clears the latency buffer.
// Useful between experiment runs.
func (c *Collector) Reset() {
	c.startTime = time.Now()
	c.msgSent.Store(0)
	c.msgRecv.Store(0)
	c.bytesSent.Store(0)
	c.bytesRecv.Store(0)
	for i := range c.msgSentByType {
		c.msgSentByType[i].Store(0)
	}
	for i := range c.msgRecvByType {
		c.msgRecvByType[i].Store(0)
	}
	c.dedupDropped.Store(0)
	c.ttlExpiredDropped.Store(0)
	c.queueOverflowDrop.Store(0)
	c.sendErrors.Store(0)
	c.compressedBytes.Store(0)
	c.uncompressedBytes.Store(0)

	c.latMu.Lock()
	c.latPos = 0
	c.latFull = false
	c.latMu.Unlock()
}

// NodeID returns the collector's node identifier.
func (c *Collector) NodeID() uint16 { return c.nodeID }

// Uptime returns the duration since the collector was created or last reset.
func (c *Collector) Uptime() time.Duration { return time.Since(c.startTime) }
