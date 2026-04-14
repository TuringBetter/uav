// Package runtime — node.go
//
// Node is the central orchestrator of the framework layer.  It ties together:
//   - A Transport for raw network I/O
//   - A PeerManager for topology management
//   - A Router for inbound TTL/dedup filtering and dispatch
//   - A SendQueue for priority-ordered outbound delivery
//   - A TimerManager for algorithm timers
//   - An Algorithm that plugs into the above via NodeAPI
package runtime

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"
	"uav/node/algorithm"
	"uav/node/metrics"
	"uav/node/transport"
	"uav/node/transport/codec"
	"uav/pkg/message"
)

// Node is the core node orchestrator implementing algorithm.NodeAPI.
type Node struct {
	cfg  nodeOptions
	tr   transport.Transport
	pm   *PeerManager
	sq   *SendQueue
	tm   *TimerManager
	rt   *Router
	algo algorithm.Algorithm
	mc   *metrics.Collector // optional; nil = no metrics

	seqCounter atomic.Uint32
	stopCh     chan struct{}
	wg         sync.WaitGroup
	once       sync.Once
}

type nodeOptions struct {
	id            uint16
	sendQueueSize int
	recvBufSize   int
	mc            *metrics.Collector
}

// Option is a functional option for Node construction.
type Option func(*nodeOptions)

// WithSendQueueSize sets the outbound priority queue capacity.
func WithSendQueueSize(n int) Option { return func(o *nodeOptions) { o.sendQueueSize = n } }

// WithRecvBufSize sets the inbound channel buffer depth.
func WithRecvBufSize(n int) Option { return func(o *nodeOptions) { o.recvBufSize = n } }

// WithMetrics attaches a metrics Collector to the node.
// When set, every send/recv/drop event is recorded automatically.
func WithMetrics(mc *metrics.Collector) Option {
	return func(o *nodeOptions) { o.mc = mc }
}

// NewNode constructs a Node with the given transport.
// id is this node's unique identifier (≥1).
// Call RegisterAlgorithm before Start.
func NewNode(id uint16, tr transport.Transport, opts ...Option) *Node {
	o := nodeOptions{id: id, sendQueueSize: 512, recvBufSize: 256}
	for _, opt := range opts {
		opt(&o)
	}
	n := &Node{
		cfg:    o,
		tr:     tr,
		pm:     NewPeerManager(),
		sq:     NewSendQueue(o.sendQueueSize, o.mc),
		mc:     o.mc,
		stopCh: make(chan struct{}),
	}
	n.rt = NewRouter(id, func(msg message.Message) {
		if n.algo != nil {
			n.algo.OnMessage(msg)
		}
	}, o.mc)
	n.tm = NewTimerManager(func(name string) {
		if n.algo != nil {
			n.algo.OnTick(name)
		}
	})
	return n
}

// AddPeer registers a remote peer.
func (n *Node) AddPeer(id uint16, addr string) error {
	return n.pm.Add(id, addr)
}

// RegisterAlgorithm attaches a pluggable algorithm. Must be called before Start.
func (n *Node) RegisterAlgorithm(algo algorithm.Algorithm) {
	n.algo = algo
}

// Start launches the transport, receive loop, send loop, and algorithm.
func (n *Node) Start() error {
	if err := n.tr.Start(); err != nil {
		return fmt.Errorf("node %d: transport start: %w", n.cfg.id, err)
	}
	n.wg.Add(2)
	go n.recvLoop()
	go n.sendLoop()
	if n.algo != nil {
		return n.algo.Start()
	}
	return nil
}

// Stop gracefully shuts down the node.
func (n *Node) Stop() {
	n.once.Do(func() {
		if n.algo != nil {
			n.algo.Stop()
		}
		n.tm.StopAll()
		close(n.stopCh)
		n.tr.Stop() //nolint:errcheck
		n.wg.Wait()
	})
}

// recvLoop reads from transport and dispatches via router.
func (n *Node) recvLoop() {
	defer n.wg.Done()
	for {
		select {
		case <-n.stopCh:
			return
		case msg, ok := <-n.tr.Recv():
			if !ok {
				return
			}
			// Record inbound metrics.
			if n.mc != nil {
				wireBytes := message.HeaderSize + len(msg.Payload)
				n.mc.RecordRecv(msg, wireBytes)
			}
			n.rt.Dispatch(msg)
		}
	}
}

// sendLoop drains the priority queue and forwards to transport.
func (n *Node) sendLoop() {
	defer n.wg.Done()
	for {
		select {
		case <-n.stopCh:
			return
		case <-n.sq.Wait():
			for {
				msg, ok := n.sq.Pop()
				if !ok {
					break
				}
				peer, found := n.pm.Get(msg.To)
				if !found {
					continue // unknown peer; discard
				}
				err := n.tr.Send(peer.Addr, msg)
				if n.mc != nil {
					if err != nil {
						n.mc.RecordSendError()
					} else {
						wireBytes, _ := codec.Encode(msg)
						n.mc.RecordSend(msg, len(wireBytes))
					}
				}
			}
		}
	}
}

// nextSeq atomically increments and returns the per-node sequence counter.
func (n *Node) nextSeq() uint32 {
	return n.seqCounter.Add(1)
}

// ─────────────────────────────────────────────
// algorithm.NodeAPI implementation
// ─────────────────────────────────────────────

// ID returns this node's unique identifier.
func (n *Node) ID() uint16 { return n.cfg.id }

// Peers returns the IDs of all known peers.
func (n *Node) Peers() []uint16 { return n.pm.List() }

// PeerAddr returns the address of the given peer.
func (n *Node) PeerAddr(peerID uint16) (string, bool) {
	p, ok := n.pm.Get(peerID)
	return p.Addr, ok
}

// Send enqueues a unicast message to peerID.
// It fills in From, Seq, and Timestamp automatically.
func (n *Node) Send(peerID uint16, msg message.Message) error {
	msg.From = n.cfg.id
	msg.To = peerID
	msg.Seq = n.nextSeq()
	if msg.Timestamp == 0 {
		msg.Timestamp = time.Now().UnixMilli()
	}
	n.sq.Push(msg)
	return nil
}

// Broadcast enqueues msg for delivery to all known peers.
func (n *Node) Broadcast(msg message.Message) error {
	msg.From = n.cfg.id
	msg.To = message.BroadcastID
	if msg.Timestamp == 0 {
		msg.Timestamp = time.Now().UnixMilli()
	}
	for _, peerID := range n.pm.List() {
		m := msg
		m.To = peerID
		m.Seq = n.nextSeq()
		n.sq.Push(m)
	}
	return nil
}

// SetTimer registers a recurring timer identified by name.
func (n *Node) SetTimer(name string, d time.Duration) {
	n.tm.Set(name, d)
}

// CancelTimer stops the named timer.
func (n *Node) CancelTimer(name string) {
	n.tm.Cancel(name)
}

// Metrics returns the attached metrics Collector (may be nil).
func (n *Node) Metrics() *metrics.Collector {
	return n.mc
}
