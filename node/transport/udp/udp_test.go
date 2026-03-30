package udp_test

import (
	"testing"
	"time"
	"uav/node/transport/udp"
	"uav/pkg/message"
)

func TestUDP_SendRecv(t *testing.T) {
	receiver := udp.New(":0", 32)
	if err := receiver.Start(); err != nil {
		t.Fatalf("receiver Start: %v", err)
	}
	defer receiver.Stop()

	sender := udp.New(":0", 32)
	if err := sender.Start(); err != nil {
		t.Fatalf("sender Start: %v", err)
	}
	defer sender.Stop()

	want := message.Message{
		Type:      message.TypeHeartbeat,
		From:      1,
		To:        2,
		Seq:       7,
		Timestamp: time.Now().UnixMilli(),
		TTL:       message.TTLState,
		Priority:  message.PriorityState,
		Payload:   []byte("ping"),
	}

	if err := sender.Send(receiver.LocalAddr(), want); err != nil {
		t.Fatalf("Send: %v", err)
	}

	select {
	case got := <-receiver.Recv():
		if got.Type != want.Type || got.From != want.From || string(got.Payload) != string(want.Payload) {
			t.Errorf("received message mismatch: got %+v, want %+v", got, want)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timeout waiting for message")
	}
}

// TestUDP_SimulatedLoss verifies that the receiver handles missing datagrams
// gracefully (no crash/deadlock) — simulated by simply not sending some messages.
func TestUDP_SimulatedLoss(t *testing.T) {
	recv := udp.New(":0", 64)
	if err := recv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer recv.Stop()

	send := udp.New(":0", 64)
	if err := send.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer send.Stop()

	sent := 0
	for i := 0; i < 10; i++ {
		// Simulate 30% loss by skipping every 3rd message.
		if i%3 == 0 {
			continue
		}
		msg := message.Message{
			Type:      message.TypeState,
			From:      1,
			Seq:       uint32(i),
			Timestamp: time.Now().UnixMilli(),
			TTL:       message.TTLState,
		}
		if err := send.Send(recv.LocalAddr(), msg); err != nil {
			t.Fatalf("Send i=%d: %v", i, err)
		}
		sent++
	}

	received := 0
	deadline := time.After(300 * time.Millisecond)
outer:
	for {
		select {
		case <-recv.Recv():
			received++
			if received == sent {
				break outer
			}
		case <-deadline:
			break outer
		}
	}

	if received != sent {
		t.Errorf("received %d / %d sent messages", received, sent)
	}
}
