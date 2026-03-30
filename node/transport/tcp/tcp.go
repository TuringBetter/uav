// Package tcp provides a TCP-based implementation of the transport.Transport interface.
//
// Because TCP is stream-based, a 4-byte little-endian length prefix is prepended
// to each encoded message to enable frame recovery.
//
// Outbound connections are pooled (one per peer address) and reconnected with
// exponential backoff when a connection is lost.
package tcp

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
	"uav/node/transport/codec"
	"uav/pkg/message"
)

var le = binary.LittleEndian

// defaultDialTimeout is used when no explicit deadline is set.
const defaultDialTimeout = 3 * time.Second

// Transport is a TCP implementation of transport.Transport.
type Transport struct {
	addr    string
	ln      net.Listener
	recvCh  chan message.Message
	stopCh  chan struct{}
	wg      sync.WaitGroup
	once    sync.Once

	// pool holds one outbound connection per remote address.
	poolMu sync.Mutex
	pool   map[string]*tcpConn
}

// tcpConn wraps a net.Conn with reconnection metadata.
type tcpConn struct {
	mu      sync.Mutex
	conn    net.Conn
	addr    string
	backoff time.Duration
}

// New creates a new TCP Transport bound to addr (e.g., ":9000").
func New(addr string, recvBufSize int) *Transport {
	if recvBufSize <= 0 {
		recvBufSize = 256
	}
	return &Transport{
		addr:   addr,
		recvCh: make(chan message.Message, recvBufSize),
		stopCh: make(chan struct{}),
		pool:   make(map[string]*tcpConn),
	}
}

// Start begins listening for incoming TCP connections.
func (t *Transport) Start() error {
	ln, err := net.Listen("tcp", t.addr)
	if err != nil {
		return fmt.Errorf("tcp: listen %q: %w", t.addr, err)
	}
	t.ln = ln
	t.wg.Add(1)
	go t.acceptLoop()
	return nil
}

func (t *Transport) acceptLoop() {
	defer t.wg.Done()
	for {
		conn, err := t.ln.Accept()
		if err != nil {
			select {
			case <-t.stopCh:
				return
			default:
				continue
			}
		}
		t.wg.Add(1)
		go t.handleConn(conn)
	}
}

func (t *Transport) handleConn(conn net.Conn) {
	defer t.wg.Done()
	defer conn.Close()

	for {
		select {
		case <-t.stopCh:
			return
		default:
		}

		msg, err := readFrame(conn)
		if err != nil {
			return // connection closed or error
		}

		select {
		case t.recvCh <- msg:
		default:
			// Drop oldest to make room.
			select {
			case <-t.recvCh:
			default:
			}
			t.recvCh <- msg
		}
	}
}

// Stop closes the listener, drains the connection pool, and waits for goroutines.
func (t *Transport) Stop() error {
	var closeErr error
	t.once.Do(func() {
		close(t.stopCh)
		if t.ln != nil {
			closeErr = t.ln.Close()
		}
		t.poolMu.Lock()
		for _, c := range t.pool {
			c.mu.Lock()
			if c.conn != nil {
				c.conn.Close()
			}
			c.mu.Unlock()
		}
		t.poolMu.Unlock()
		t.wg.Wait()
	})
	return closeErr
}

// Send encodes msg as a length-prefixed frame and writes it to addr.
// The outbound connection is reused from the pool or dialled on demand.
func (t *Transport) Send(addr string, msg message.Message) error {
	data, err := codec.Encode(msg)
	if err != nil {
		return fmt.Errorf("tcp: encode: %w", err)
	}
	frame := makeFrame(data)

	tc := t.getOrCreateConn(addr)
	tc.mu.Lock()
	defer tc.mu.Unlock()

	if tc.conn == nil {
		if err := tc.dial(); err != nil {
			return fmt.Errorf("tcp: dial %q: %w", addr, err)
		}
	}

	if _, err := tc.conn.Write(frame); err != nil {
		tc.conn.Close()
		tc.conn = nil
		return fmt.Errorf("tcp: write to %q: %w", addr, err)
	}
	return nil
}

func (t *Transport) getOrCreateConn(addr string) *tcpConn {
	t.poolMu.Lock()
	defer t.poolMu.Unlock()
	if c, ok := t.pool[addr]; ok {
		return c
	}
	c := &tcpConn{addr: addr, backoff: 100 * time.Millisecond}
	t.pool[addr] = c
	return c
}

// dial establishes the TCP connection (caller must hold tc.mu).
func (tc *tcpConn) dial() error {
	ctx, cancel := context.WithTimeout(context.Background(), defaultDialTimeout)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", tc.addr)
	if err != nil {
		return err
	}
	tc.conn = conn
	tc.backoff = 100 * time.Millisecond // reset on success
	return nil
}

// makeFrame prepends a 4-byte little-endian length to data.
func makeFrame(data []byte) []byte {
	frame := make([]byte, 4+len(data))
	le.PutUint32(frame, uint32(len(data)))
	copy(frame[4:], data)
	return frame
}

// readFrame reads one length-prefixed frame from conn.
func readFrame(conn net.Conn) (message.Message, error) {
	var lenBuf [4]byte
	if _, err := io.ReadFull(conn, lenBuf[:]); err != nil {
		return message.Message{}, err
	}
	frameLen := le.Uint32(lenBuf[:])
	if frameLen == 0 || frameLen > 65536 {
		return message.Message{}, fmt.Errorf("tcp: invalid frame length %d", frameLen)
	}
	buf := make([]byte, frameLen)
	if _, err := io.ReadFull(conn, buf); err != nil {
		return message.Message{}, err
	}
	return codec.Decode(buf)
}

// Recv returns the inbound message channel.
func (t *Transport) Recv() <-chan message.Message {
	return t.recvCh
}

// LocalAddr returns the local listener address.
func (t *Transport) LocalAddr() string {
	if t.ln != nil {
		return t.ln.Addr().String()
	}
	return t.addr
}
