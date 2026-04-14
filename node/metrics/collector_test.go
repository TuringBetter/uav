package metrics_test

import (
	"bytes"
	"encoding/json"
	"os"
	"sync"
	"testing"
	"time"
	"uav/node/metrics"
	"uav/pkg/message"
)

func TestCollector_RecordSendRecv(t *testing.T) {
	c := metrics.NewCollector(1)

	msg := message.Message{
		Type:      message.TypeState,
		Timestamp: time.Now().UnixMilli(),
	}
	c.RecordSend(msg, 100)
	c.RecordSend(msg, 200)
	c.RecordRecv(msg, 50)

	snap := c.Snapshot()
	if snap.MessagesSent != 2 {
		t.Errorf("MessagesSent = %d, want 2", snap.MessagesSent)
	}
	if snap.BytesSent != 300 {
		t.Errorf("BytesSent = %d, want 300", snap.BytesSent)
	}
	if snap.MessagesRecv != 1 {
		t.Errorf("MessagesRecv = %d, want 1", snap.MessagesRecv)
	}
	if snap.BytesRecv != 50 {
		t.Errorf("BytesRecv = %d, want 50", snap.BytesRecv)
	}
	if snap.MsgSentByType["STATE"] != 2 {
		t.Errorf("MsgSentByType[STATE] = %d, want 2", snap.MsgSentByType["STATE"])
	}
}

func TestCollector_DropCounters(t *testing.T) {
	c := metrics.NewCollector(1)

	c.RecordDedupDrop()
	c.RecordDedupDrop()
	c.RecordTTLDrop()
	c.RecordQueueDrop()
	c.RecordSendError()
	c.RecordSendError()

	snap := c.Snapshot()
	if snap.DedupDropped != 2 {
		t.Errorf("DedupDropped = %d, want 2", snap.DedupDropped)
	}
	if snap.TTLExpiredDropped != 1 {
		t.Errorf("TTLExpiredDropped = %d, want 1", snap.TTLExpiredDropped)
	}
	if snap.QueueOverflowDropped != 1 {
		t.Errorf("QueueOverflowDropped = %d, want 1", snap.QueueOverflowDropped)
	}
	if snap.SendErrors != 2 {
		t.Errorf("SendErrors = %d, want 2", snap.SendErrors)
	}
}

func TestCollector_DropRate(t *testing.T) {
	c := metrics.NewCollector(1)

	// Simulate: 8 received + 1 dedup + 1 ttl = 10 total arriving.
	// Drop rate = 2/10 = 0.2
	for i := 0; i < 8; i++ {
		c.RecordRecv(message.Message{Type: message.TypeState}, 10)
	}
	c.RecordDedupDrop()
	c.RecordTTLDrop()

	snap := c.Snapshot()
	if snap.DropRate < 0.19 || snap.DropRate > 0.21 {
		t.Errorf("DropRate = %.4f, want ~0.2", snap.DropRate)
	}
}

func TestCollector_LatencyStats(t *testing.T) {
	c := metrics.NewCollector(1)

	// Insert messages with known timestamps so we get predictable latencies.
	// The latency = now - msg.Timestamp, we record directly.
	now := time.Now().UnixMilli()
	for i := 0; i < 100; i++ {
		// Timestamp 10ms ago → latency ≈ 10ms.
		msg := message.Message{
			Type:      message.TypeHeartbeat,
			Timestamp: now - 10,
		}
		c.RecordRecv(msg, 25)
	}

	// Allow a bit for processing time variance.
	snap := c.Snapshot()
	if snap.AvgLatencyMs < 5 || snap.AvgLatencyMs > 50 {
		t.Errorf("AvgLatencyMs = %.1f, expected roughly 10", snap.AvgLatencyMs)
	}
}

func TestCollector_ConcurrentWrites(t *testing.T) {
	c := metrics.NewCollector(1)

	const goroutines = 50
	const iterations = 1000

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			msg := message.Message{
				Type:      message.TypeConsensus,
				Timestamp: time.Now().UnixMilli(),
			}
			for i := 0; i < iterations; i++ {
				c.RecordSend(msg, 64)
				c.RecordRecv(msg, 64)
				c.RecordDedupDrop()
			}
		}()
	}
	wg.Wait()

	snap := c.Snapshot()
	expected := uint64(goroutines * iterations)
	if snap.MessagesSent != expected {
		t.Errorf("MessagesSent = %d, want %d", snap.MessagesSent, expected)
	}
	if snap.MessagesRecv != expected {
		t.Errorf("MessagesRecv = %d, want %d", snap.MessagesRecv, expected)
	}
	if snap.DedupDropped != expected {
		t.Errorf("DedupDropped = %d, want %d", snap.DedupDropped, expected)
	}
}

func TestCollector_Reset(t *testing.T) {
	c := metrics.NewCollector(1)

	c.RecordSend(message.Message{Type: message.TypeState}, 100)
	c.RecordDedupDrop()
	c.Reset()

	snap := c.Snapshot()
	if snap.MessagesSent != 0 {
		t.Errorf("after Reset, MessagesSent = %d, want 0", snap.MessagesSent)
	}
	if snap.DedupDropped != 0 {
		t.Errorf("after Reset, DedupDropped = %d, want 0", snap.DedupDropped)
	}
}

func TestSnapshot_ExportJSON(t *testing.T) {
	c := metrics.NewCollector(42)
	c.RecordSend(message.Message{Type: message.TypeHeartbeat}, 50)

	snap := c.Snapshot()
	path := t.TempDir() + "/test_metrics.json"
	if err := snap.ExportJSON(path); err != nil {
		t.Fatalf("ExportJSON: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}

	var parsed metrics.MetricsSnapshot
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if parsed.NodeID != 42 {
		t.Errorf("NodeID = %d, want 42", parsed.NodeID)
	}
	if parsed.MessagesSent != 1 {
		t.Errorf("MessagesSent = %d, want 1", parsed.MessagesSent)
	}
}

func TestSnapshot_WriteCSV(t *testing.T) {
	c := metrics.NewCollector(1)
	c.RecordSend(message.Message{Type: message.TypeState}, 200)
	snap := c.Snapshot()

	var buf bytes.Buffer
	if err := metrics.WriteCSVHeader(&buf); err != nil {
		t.Fatalf("WriteCSVHeader: %v", err)
	}
	if err := snap.WriteCSVRow(&buf); err != nil {
		t.Fatalf("WriteCSVRow: %v", err)
	}

	output := buf.String()
	// Should have header + data row (2 lines at minimum).
	lines := bytes.Count([]byte(output), []byte("\n"))
	if lines < 2 {
		t.Errorf("expected at least 2 lines in CSV output, got %d", lines)
	}
}

func TestSnapshot_String(t *testing.T) {
	c := metrics.NewCollector(7)
	c.RecordSend(message.Message{Type: message.TypeState}, 100)
	snap := c.Snapshot()

	s := snap.String()
	if len(s) == 0 {
		t.Fatal("String() returned empty")
	}
	// Should contain node ID.
	if !bytes.Contains([]byte(s), []byte("node 7")) {
		t.Errorf("String() missing node ID, got: %s", s)
	}
}

func TestCollector_Compression(t *testing.T) {
	c := metrics.NewCollector(1)
	c.RecordCompression(1000, 600)
	c.RecordCompression(2000, 1200)

	snap := c.Snapshot()
	if snap.UncompressedBytes != 3000 {
		t.Errorf("UncompressedBytes = %d, want 3000", snap.UncompressedBytes)
	}
	if snap.CompressedBytes != 1800 {
		t.Errorf("CompressedBytes = %d, want 1800", snap.CompressedBytes)
	}
	// 1800 / 3000 = 0.6
	if snap.CompressionRatio < 0.59 || snap.CompressionRatio > 0.61 {
		t.Errorf("CompressionRatio = %.4f, want ~0.6", snap.CompressionRatio)
	}
}
