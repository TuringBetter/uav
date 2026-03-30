package message_test

import (
	"testing"
	"time"
	"uav/pkg/message"
)

func TestMessage_IsExpired(t *testing.T) {
	t.Run("TTL=0 never expires", func(t *testing.T) {
		msg := message.Message{
			TTL:       message.TTLForever,
			Timestamp: time.Now().Add(-10 * time.Second).UnixMilli(),
		}
		if msg.IsExpired() {
			t.Fatal("TTL=0 message should never expire")
		}
	})

	t.Run("expired message", func(t *testing.T) {
		msg := message.Message{
			TTL:       100, // 100ms
			Timestamp: time.Now().Add(-200 * time.Millisecond).UnixMilli(),
		}
		if !msg.IsExpired() {
			t.Fatal("message should be expired")
		}
	})

	t.Run("fresh message not expired", func(t *testing.T) {
		msg := message.Message{
			TTL:       message.TTLState,
			Timestamp: time.Now().UnixMilli(),
		}
		if msg.IsExpired() {
			t.Fatal("fresh message should not be expired")
		}
	})
}

func TestMessage_IsBroadcast(t *testing.T) {
	broadcast := message.Message{To: message.BroadcastID}
	if !broadcast.IsBroadcast() {
		t.Fatal("expected broadcast")
	}
	unicast := message.Message{To: 42}
	if unicast.IsBroadcast() {
		t.Fatal("expected unicast")
	}
}
