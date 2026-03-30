// Package udp provides a UDP-based implementation of the transport.Transport interface.
//
// UDP is the preferred transport for real-time UAV data because:
//   - No head-of-line blocking (unlike TCP)
//   - Minimal per-packet overhead
//   - Natural multicast support
//
// Each outbound send is a single WriteToUDP call; framing is handled by the
// codec layer (each datagram = exactly one Message).
package udp

import (
	"fmt"
	"net"
	"sync"
	"uav/node/transport/codec"
	"uav/pkg/message"
)

// maxUDPPacket is the maximum safe UDP payload size (excluding IP/UDP headers).
const maxUDPPacket = 65507

// Transport is a UDP implementation of transport.Transport.
type Transport struct {
	addr    string
	conn    *net.UDPConn
	recvCh  chan message.Message
	stopCh  chan struct{}
	wg      sync.WaitGroup
	once    sync.Once // guards Stop
	bufSize int
}

// New creates a new UDP Transport that will bind to addr (e.g., ":9000").
// recvBufSize controls the depth of the inbound message channel.
func New(addr string, recvBufSize int) *Transport {
	if recvBufSize <= 0 {
		recvBufSize = 256
	}
	return &Transport{
		addr:    addr,
		recvCh:  make(chan message.Message, recvBufSize),
		stopCh:  make(chan struct{}),
		bufSize: recvBufSize,
	}
}

// Start binds the UDP socket and starts the receive loop.
func (t *Transport) Start() error {
	udpAddr, err := net.ResolveUDPAddr("udp", t.addr)
	if err != nil {
		return fmt.Errorf("udp: resolve %q: %w", t.addr, err)
	}
	conn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		return fmt.Errorf("udp: listen %q: %w", t.addr, err)
	}
	t.conn = conn
	t.wg.Add(1)
	go t.readLoop()
	return nil
}

// readLoop reads datagrams and pushes decoded messages into recvCh.
func (t *Transport) readLoop() {
	defer t.wg.Done()
	buf := make([]byte, maxUDPPacket)

	for {
		// Non-blocking check for stop signal.
		select {
		case <-t.stopCh:
			return
		default:
		}

		n, _, err := t.conn.ReadFromUDP(buf)
		if err != nil {
			select {
			case <-t.stopCh:
				return // expected error after Close()
			default:
				continue // transient error; keep running
			}
		}

		msg, err := codec.Decode(buf[:n])
		if err != nil {
			// Malformed datagram — discard silently (common in lossy networks).
			continue
		}

		select {
		case t.recvCh <- msg:
		default:
			// Channel full: drop the oldest message by draining one slot.
			// This prevents the goroutine from blocking while providing
			// backpressure behaviour.
			select {
			case <-t.recvCh:
			default:
			}
			t.recvCh <- msg
		}
	}
}

// Stop closes the UDP socket and waits for the read loop to finish.
func (t *Transport) Stop() error {
	var closeErr error
	t.once.Do(func() {
		close(t.stopCh)
		if t.conn != nil {
			closeErr = t.conn.Close()
		}
		t.wg.Wait()
	})
	return closeErr
}

// Send encodes msg and transmits it to the given UDP address.
func (t *Transport) Send(addr string, msg message.Message) error {
	data, err := codec.Encode(msg)
	if err != nil {
		return fmt.Errorf("udp: encode: %w", err)
	}
	udpAddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return fmt.Errorf("udp: resolve peer addr %q: %w", addr, err)
	}
	_, err = t.conn.WriteToUDP(data, udpAddr)
	return err
}

// Recv returns the inbound message channel.
func (t *Transport) Recv() <-chan message.Message {
	return t.recvCh
}

// LocalAddr returns the local address of the bound socket.
func (t *Transport) LocalAddr() string {
	if t.conn != nil {
		return t.conn.LocalAddr().String()
	}
	return t.addr
}
