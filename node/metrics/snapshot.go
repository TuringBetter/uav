package metrics

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"
	"uav/pkg/message"
)

// MetricsSnapshot is a point-in-time copy of all metrics from a Collector.
// It contains both raw counters and derived values (rates, percentiles).
type MetricsSnapshot struct {
	// ── Identity & timing ───────────────────────────────────────────────
	NodeID    uint16        `json:"node_id"`
	Timestamp time.Time     `json:"timestamp"`
	Uptime    time.Duration `json:"uptime_ns"`
	UptimeSec float64       `json:"uptime_sec"`

	// ── Communication overhead ──────────────────────────────────────────
	MessagesSent uint64 `json:"messages_sent"`
	MessagesRecv uint64 `json:"messages_recv"`
	BytesSent    uint64 `json:"bytes_sent"`
	BytesRecv    uint64 `json:"bytes_recv"`

	// Derived rates.
	MsgSentPerSec   float64 `json:"msg_sent_per_sec"`
	MsgRecvPerSec   float64 `json:"msg_recv_per_sec"`
	BytesSentPerSec float64 `json:"bytes_sent_per_sec"`
	BytesRecvPerSec float64 `json:"bytes_recv_per_sec"`

	// Per message-type breakdown.
	MsgSentByType map[string]uint64 `json:"msg_sent_by_type"`
	MsgRecvByType map[string]uint64 `json:"msg_recv_by_type"`

	// ── Drop / error counters ───────────────────────────────────────────
	DedupDropped         uint64  `json:"dedup_dropped"`
	TTLExpiredDropped    uint64  `json:"ttl_expired_dropped"`
	QueueOverflowDropped uint64  `json:"queue_overflow_dropped"`
	SendErrors           uint64  `json:"send_errors"`
	DropRate             float64 `json:"drop_rate"` // (dedup+ttl)/(recv+dedup+ttl)

	// ── Latency (ms) ────────────────────────────────────────────────────
	AvgLatencyMs float64 `json:"avg_latency_ms"`
	MaxLatencyMs float64 `json:"max_latency_ms"`
	MinLatencyMs float64 `json:"min_latency_ms"`
	P50LatencyMs float64 `json:"p50_latency_ms"`
	P95LatencyMs float64 `json:"p95_latency_ms"`
	P99LatencyMs float64 `json:"p99_latency_ms"`

	// ── Compression efficiency ──────────────────────────────────────────
	CompressedBytes   uint64  `json:"compressed_bytes"`
	UncompressedBytes uint64  `json:"uncompressed_bytes"`
	CompressionRatio  float64 `json:"compression_ratio"` // compressed/uncompressed (lower = better)
}

// typeNames maps MessageType indices to human-readable strings.
var typeNames = [5]string{
	message.TypeHeartbeat: "HEARTBEAT",
	message.TypeState:     "STATE",
	message.TypeControl:   "CONTROL",
	message.TypeConsensus: "CONSENSUS",
	message.TypeCustom:    "CUSTOM",
}

// Snapshot captures and returns a MetricsSnapshot from a Collector.
func (c *Collector) Snapshot() MetricsSnapshot {
	uptime := c.Uptime()
	sec := uptime.Seconds()
	if sec < 0.001 {
		sec = 0.001 // avoid division by zero
	}

	msgSent := c.msgSent.Load()
	msgRecv := c.msgRecv.Load()
	bytesSent := c.bytesSent.Load()
	bytesRecv := c.bytesRecv.Load()
	dedupDrop := c.dedupDropped.Load()
	ttlDrop := c.ttlExpiredDropped.Load()
	queueDrop := c.queueOverflowDrop.Load()
	sendErr := c.sendErrors.Load()
	compB := c.compressedBytes.Load()
	uncompB := c.uncompressedBytes.Load()

	// Per-type maps.
	sentByType := make(map[string]uint64, 5)
	recvByType := make(map[string]uint64, 5)
	for i := 0; i < 5; i++ {
		sentByType[typeNames[i]] = c.msgSentByType[i].Load()
		recvByType[typeNames[i]] = c.msgRecvByType[i].Load()
	}

	// Drop rate: fraction of total arriving messages that were discarded.
	totalArrived := msgRecv + dedupDrop + ttlDrop
	var dropRate float64
	if totalArrived > 0 {
		dropRate = float64(dedupDrop+ttlDrop) / float64(totalArrived)
	}

	// Compression ratio.
	var compRatio float64
	if uncompB > 0 {
		compRatio = float64(compB) / float64(uncompB)
	}

	// Latency statistics.
	lats := c.latencies()
	avg, max, min, p50, p95, p99 := latencyStats(lats)

	return MetricsSnapshot{
		NodeID:    c.nodeID,
		Timestamp: time.Now(),
		Uptime:    uptime,
		UptimeSec: sec,

		MessagesSent: msgSent,
		MessagesRecv: msgRecv,
		BytesSent:    bytesSent,
		BytesRecv:    bytesRecv,

		MsgSentPerSec:   float64(msgSent) / sec,
		MsgRecvPerSec:   float64(msgRecv) / sec,
		BytesSentPerSec: float64(bytesSent) / sec,
		BytesRecvPerSec: float64(bytesRecv) / sec,

		MsgSentByType: sentByType,
		MsgRecvByType: recvByType,

		DedupDropped:         dedupDrop,
		TTLExpiredDropped:    ttlDrop,
		QueueOverflowDropped: queueDrop,
		SendErrors:           sendErr,
		DropRate:             dropRate,

		AvgLatencyMs: avg,
		MaxLatencyMs: max,
		MinLatencyMs: min,
		P50LatencyMs: p50,
		P95LatencyMs: p95,
		P99LatencyMs: p99,

		CompressedBytes:   compB,
		UncompressedBytes: uncompB,
		CompressionRatio:  compRatio,
	}
}

// ── Export methods ───────────────────────────────────────────────────────

// ExportJSON writes the snapshot as indented JSON to the given file path.
func (s *MetricsSnapshot) ExportJSON(path string) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("metrics: marshal JSON: %w", err)
	}
	return os.WriteFile(path, data, 0644)
}

// WriteCSVHeader writes a CSV header row matching WriteCSVRow.
func WriteCSVHeader(w io.Writer) error {
	cw := csv.NewWriter(w)
	err := cw.Write(csvHeader())
	cw.Flush()
	return err
}

// WriteCSVRow appends one row of metrics data.
func (s *MetricsSnapshot) WriteCSVRow(w io.Writer) error {
	cw := csv.NewWriter(w)
	err := cw.Write(s.csvRow())
	cw.Flush()
	return err
}

// String returns a compact human-readable summary suitable for log output.
func (s *MetricsSnapshot) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "[node %d] uptime=%.1fs ", s.NodeID, s.UptimeSec)
	fmt.Fprintf(&b, "sent=%d(%.1f/s,%.1fKB) recv=%d(%.1f/s,%.1fKB) ",
		s.MessagesSent, s.MsgSentPerSec, float64(s.BytesSent)/1024,
		s.MessagesRecv, s.MsgRecvPerSec, float64(s.BytesRecv)/1024)
	fmt.Fprintf(&b, "drop(dedup=%d,ttl=%d,queue=%d,err=%d,rate=%.2f%%) ",
		s.DedupDropped, s.TTLExpiredDropped, s.QueueOverflowDropped,
		s.SendErrors, s.DropRate*100)
	fmt.Fprintf(&b, "latency(avg=%.1f,p50=%.1f,p95=%.1f,p99=%.1f,max=%.1f ms)",
		s.AvgLatencyMs, s.P50LatencyMs, s.P95LatencyMs, s.P99LatencyMs, s.MaxLatencyMs)
	if s.UncompressedBytes > 0 {
		fmt.Fprintf(&b, " compress=%.2f%%", s.CompressionRatio*100)
	}
	return b.String()
}

// ── Internal helpers ────────────────────────────────────────────────────

func csvHeader() []string {
	return []string{
		"timestamp", "node_id", "uptime_sec",
		"msg_sent", "msg_recv", "bytes_sent", "bytes_recv",
		"msg_sent_per_sec", "msg_recv_per_sec",
		"bytes_sent_per_sec", "bytes_recv_per_sec",
		"dedup_dropped", "ttl_expired_dropped",
		"queue_overflow_dropped", "send_errors", "drop_rate",
		"avg_latency_ms", "max_latency_ms", "min_latency_ms",
		"p50_latency_ms", "p95_latency_ms", "p99_latency_ms",
		"compressed_bytes", "uncompressed_bytes", "compression_ratio",
	}
}

func (s *MetricsSnapshot) csvRow() []string {
	return []string{
		s.Timestamp.Format(time.RFC3339),
		fmt.Sprintf("%d", s.NodeID),
		fmt.Sprintf("%.3f", s.UptimeSec),
		fmt.Sprintf("%d", s.MessagesSent),
		fmt.Sprintf("%d", s.MessagesRecv),
		fmt.Sprintf("%d", s.BytesSent),
		fmt.Sprintf("%d", s.BytesRecv),
		fmt.Sprintf("%.2f", s.MsgSentPerSec),
		fmt.Sprintf("%.2f", s.MsgRecvPerSec),
		fmt.Sprintf("%.2f", s.BytesSentPerSec),
		fmt.Sprintf("%.2f", s.BytesRecvPerSec),
		fmt.Sprintf("%d", s.DedupDropped),
		fmt.Sprintf("%d", s.TTLExpiredDropped),
		fmt.Sprintf("%d", s.QueueOverflowDropped),
		fmt.Sprintf("%d", s.SendErrors),
		fmt.Sprintf("%.6f", s.DropRate),
		fmt.Sprintf("%.3f", s.AvgLatencyMs),
		fmt.Sprintf("%.3f", s.MaxLatencyMs),
		fmt.Sprintf("%.3f", s.MinLatencyMs),
		fmt.Sprintf("%.3f", s.P50LatencyMs),
		fmt.Sprintf("%.3f", s.P95LatencyMs),
		fmt.Sprintf("%.3f", s.P99LatencyMs),
		fmt.Sprintf("%d", s.CompressedBytes),
		fmt.Sprintf("%d", s.UncompressedBytes),
		fmt.Sprintf("%.6f", s.CompressionRatio),
	}
}

// latencyStats computes avg, max, min, p50, p95, p99 from a sorted copy of lats.
func latencyStats(lats []int64) (avg, max, min, p50, p95, p99 float64) {
	if len(lats) == 0 {
		return
	}

	sorted := make([]int64, len(lats))
	copy(sorted, lats)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	var sum int64
	for _, v := range sorted {
		sum += v
	}
	n := len(sorted)
	avg = float64(sum) / float64(n)
	min = float64(sorted[0])
	max = float64(sorted[n-1])
	p50 = percentile(sorted, 0.50)
	p95 = percentile(sorted, 0.95)
	p99 = percentile(sorted, 0.99)
	return
}

// percentile returns the p-th percentile from a sorted slice.
func percentile(sorted []int64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(p * float64(len(sorted)-1))
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return float64(sorted[idx])
}
