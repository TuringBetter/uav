package runtime

import (
	"container/heap"
	"sync"
	"uav/node/metrics"
	"uav/pkg/message"
)

// ─────────────────────────────────────────────
// Outbound priority queue (container/heap)
// ─────────────────────────────────────────────
// Improvement over spec: uses a heap rather than slice-sort, giving O(log n)
// push/pop instead of O(n log n) sort per insertion.

// queueItem wraps a message with heap metadata.
type queueItem struct {
	msg   message.Message
	seq   uint64 // insertion order; used for FIFO tie-breaking within same priority
	index int    // heap internal index
}

// msgHeap implements heap.Interface for a min-heap of *queueItem.
type msgHeap []*queueItem

func (h msgHeap) Len() int { return len(h) }
func (h msgHeap) Less(i, j int) bool {
	if h[i].msg.Priority != h[j].msg.Priority {
		return h[i].msg.Priority < h[j].msg.Priority // lower number = higher priority
	}
	return h[i].seq < h[j].seq // FIFO within same priority tier
}
func (h msgHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].index = i
	h[j].index = j
}
func (h *msgHeap) Push(x interface{}) {
	item := x.(*queueItem)
	item.index = len(*h)
	*h = append(*h, item)
}
func (h *msgHeap) Pop() interface{} {
	old := *h
	n := len(old)
	item := old[n-1]
	old[n-1] = nil
	*h = old[:n-1]
	return item
}

// SendQueue is a thread-safe, priority-based outbound message queue.
type SendQueue struct {
	mu      sync.Mutex
	h       msgHeap
	notifyC chan struct{}
	counter uint64
	cap     int
	mc      *metrics.Collector // optional; nil = no metrics
}

// NewSendQueue creates a SendQueue with the given capacity.
// mc is optional (nil = no metrics recording).
func NewSendQueue(capacity int, mc *metrics.Collector) *SendQueue {
	h := make(msgHeap, 0, capacity)
	heap.Init(&h)
	return &SendQueue{
		h:       h,
		notifyC: make(chan struct{}, 1),
		cap:     capacity,
		mc:      mc,
	}
}

// Push adds msg to the queue.  If the queue is at capacity the lowest-priority
// item is evicted (drop-tail with priority-awareness).
func (q *SendQueue) Push(msg message.Message) {
	q.mu.Lock()
	if len(q.h) >= q.cap {
		// Evict the lowest-priority (largest Priority value) item.
		worst := q.h[0]
		for _, it := range q.h {
			if it.msg.Priority > worst.msg.Priority {
				worst = it
			}
		}
		heap.Remove(&q.h, worst.index)
		if q.mc != nil {
			q.mc.RecordQueueDrop()
		}
	}
	q.counter++
	heap.Push(&q.h, &queueItem{msg: msg, seq: q.counter})
	q.mu.Unlock()

	// Non-blocking notify.
	select {
	case q.notifyC <- struct{}{}:
	default:
	}
}

// Pop removes and returns the highest-priority message.
// ok is false if the queue is empty.
func (q *SendQueue) Pop() (message.Message, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.h) == 0 {
		return message.Message{}, false
	}
	item := heap.Pop(&q.h).(*queueItem)
	return item.msg, true
}

// Wait returns a channel that receives a value whenever items are pushed.
func (q *SendQueue) Wait() <-chan struct{} {
	return q.notifyC
}

// ─────────────────────────────────────────────
// Per-peer deduplication window
// ─────────────────────────────────────────────
// Tracks recently received sequence numbers per sender to detect duplicates.
// Improvement over a naive map: the window is bounded (dedupWindowSize entries),
// preventing unbounded memory growth under sustained packet storms.

const dedupWindowSize = 256

// peerDedup tracks the deduplication state for one remote peer.
type peerDedup struct {
	mu      sync.Mutex
	highest [256]uint32
	windows [256]map[uint32]struct{}
	inited  [256]bool
}

func newPeerDedup() *peerDedup {
	pd := &peerDedup{}
	for i := range pd.windows {
		pd.windows[i] = make(map[uint32]struct{}, dedupWindowSize)
	}
	return pd
}

// IsDuplicate returns true if seq has already been seen for this peer on this stream.
// As a side-effect it records seq in the window.
func (d *peerDedup) IsDuplicate(streamID uint8, seq uint32) bool {
	d.mu.Lock()
	defer d.mu.Unlock()

	if !d.inited[streamID] {
		d.highest[streamID] = seq
		d.inited[streamID] = true
		d.windows[streamID][seq] = struct{}{}
		return false
	}

	if seq > d.highest[streamID] {
		// Advance window: evict entries that fell out of range.
		cutoff := seq - uint32(dedupWindowSize) + 1
		if seq >= uint32(dedupWindowSize) {
			for k := range d.windows[streamID] {
				if k < cutoff {
					delete(d.windows[streamID], k)
				}
			}
		}
		d.highest[streamID] = seq
	} else if d.highest[streamID]-seq >= uint32(dedupWindowSize) {
		// Too old: treat as duplicate to drop stale replayed packets.
		return true
	}

	if _, seen := d.windows[streamID][seq]; seen {
		return true
	}
	d.windows[streamID][seq] = struct{}{}
	return false
}

// ─────────────────────────────────────────────
// Router — inbound message dispatch
// ─────────────────────────────────────────────

// Router validates and dispatches inbound messages to an algorithm callback.
// It integrates TTL expiry checking and per-peer sequence-number deduplication.
type Router struct {
	nodeID  uint16
	dupsMu  sync.Mutex
	dups    map[uint16]*peerDedup // keyed by From (sender ID)
	handler func(msg message.Message)
	mc      *metrics.Collector // optional; nil = no metrics
}

// NewRouter creates a Router for the node identified by nodeID.
// handler is called for every valid (non-expired, non-duplicate) message.
// mc is optional (nil = no metrics recording).
func NewRouter(nodeID uint16, handler func(msg message.Message), mc *metrics.Collector) *Router {
	return &Router{
		nodeID:  nodeID,
		dups:    make(map[uint16]*peerDedup),
		handler: handler,
		mc:      mc,
	}
}

// Dispatch validates msg and, if it passes all checks, calls the handler.
func (r *Router) Dispatch(msg message.Message) {
	// 1. TTL expiry check.
	if msg.IsExpired() {
		if r.mc != nil {
			r.mc.RecordTTLDrop()
		}
		return
	}

	// 2. Per-peer deduplication.
	if r.isDuplicate(msg.From, msg.StreamID, msg.Seq) {
		if r.mc != nil {
			r.mc.RecordDedupDrop()
		}
		return
	}

	// 3. Deliver to algorithm.
	r.handler(msg)
}

func (r *Router) isDuplicate(from uint16, streamID uint8, seq uint32) bool {
	r.dupsMu.Lock()
	d, ok := r.dups[from]
	if !ok {
		d = newPeerDedup()
		r.dups[from] = d
	}
	r.dupsMu.Unlock()
	return d.IsDuplicate(streamID, seq)
}
