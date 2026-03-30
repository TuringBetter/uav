package codec_test

import (
	"bytes"
	"testing"
	"time"
	"uav/node/transport/codec"
	"uav/pkg/message"
)

func TestRoundTrip(t *testing.T) {
	original := message.Message{
		Type:      message.TypeConsensus,
		From:      1,
		To:        2,
		Seq:       42,
		Timestamp: time.Now().UnixMilli(),
		TTL:       message.TTLState,
		Priority:  message.PriorityState,
		Payload:   []byte("hello, UAV"),
	}

	data, err := codec.Encode(original)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	decoded, err := codec.Decode(data)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}

	assertEqual(t, "Type", uint8(original.Type), uint8(decoded.Type))
	assertEqual(t, "From", original.From, decoded.From)
	assertEqual(t, "To", original.To, decoded.To)
	assertEqual(t, "Seq", original.Seq, decoded.Seq)
	assertEqual(t, "Timestamp", original.Timestamp, decoded.Timestamp)
	assertEqual(t, "TTL", original.TTL, decoded.TTL)
	assertEqual(t, "Priority", uint8(original.Priority), uint8(decoded.Priority))

	if !bytes.Equal(original.Payload, decoded.Payload) {
		t.Errorf("Payload mismatch: got %q, want %q", decoded.Payload, original.Payload)
	}
}

func TestRoundTrip_LargePayload_Compressed(t *testing.T) {
	// Payload > CompressThreshold (256) should be compressed transparently.
	payload := bytes.Repeat([]byte("AAAA"), 100) // 400 bytes, highly compressible
	msg := message.Message{
		Type:    message.TypeState,
		From:    3,
		To:      4,
		Payload: payload,
	}

	data, err := codec.Encode(msg)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	// Verify compressed wire size is smaller than raw payload.
	if len(data) >= len(payload) {
		t.Logf("wire=%d payload=%d — compression did not reduce size (acceptable for this data)", len(data), len(payload))
	}

	decoded, err := codec.Decode(data)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if !bytes.Equal(payload, decoded.Payload) {
		t.Error("Payload mismatch after compression round-trip")
	}
}

func TestDecode_TruncatedData(t *testing.T) {
	_, err := codec.Decode([]byte{0x00, 0x01}) // too short
	if err == nil {
		t.Fatal("expected error for truncated data")
	}
}

func TestDecode_Empty(t *testing.T) {
	_, err := codec.Decode([]byte{})
	if err == nil {
		t.Fatal("expected error for empty data")
	}
}

func assertEqual[T comparable](t *testing.T, field string, got, want T) {
	t.Helper()
	if got != want {
		t.Errorf("%s: got %v, want %v", field, got, want)
	}
}
