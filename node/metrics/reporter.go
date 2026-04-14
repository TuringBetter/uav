package metrics

import (
	"fmt"
	"io"
	"log"
	"os"
	"sync"
	"time"
)

// Reporter periodically logs a MetricsSnapshot and optionally appends rows
// to a CSV file for later analysis.
type Reporter struct {
	collector *Collector
	interval  time.Duration
	logger    *log.Logger

	csvPath string   // empty = no CSV output
	csvFile *os.File // opened lazily on first tick

	stopCh chan struct{}
	wg     sync.WaitGroup
}

// ReporterOption configures a Reporter.
type ReporterOption func(*Reporter)

// WithCSVOutput configures the reporter to append metrics rows to a CSV file.
func WithCSVOutput(path string) ReporterOption {
	return func(r *Reporter) { r.csvPath = path }
}

// WithLogger overrides the default logger.
func WithLogger(l *log.Logger) ReporterOption {
	return func(r *Reporter) { r.logger = l }
}

// NewReporter creates a Reporter that snapshots the collector every interval.
func NewReporter(c *Collector, interval time.Duration, opts ...ReporterOption) *Reporter {
	r := &Reporter{
		collector: c,
		interval:  interval,
		logger:    log.New(os.Stdout, "[metrics] ", log.Ltime),
		stopCh:    make(chan struct{}),
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// Start begins periodic reporting in a background goroutine.
func (r *Reporter) Start() {
	r.wg.Add(1)
	go r.loop()
}

// Stop terminates reporting and flushes output.
func (r *Reporter) Stop() {
	close(r.stopCh)
	r.wg.Wait()
	if r.csvFile != nil {
		r.csvFile.Close()
	}
}

// Flush immediately takes a snapshot and writes it to log and CSV.
func (r *Reporter) Flush() {
	snap := r.collector.Snapshot()
	r.output(snap)
}

// FlushToFile writes a final JSON snapshot to the given path.
func (r *Reporter) FlushToFile(path string) error {
	snap := r.collector.Snapshot()
	return snap.ExportJSON(path)
}

func (r *Reporter) loop() {
	defer r.wg.Done()
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			snap := r.collector.Snapshot()
			r.output(snap)
		case <-r.stopCh:
			// Final flush on shutdown.
			snap := r.collector.Snapshot()
			r.output(snap)
			return
		}
	}
}

func (r *Reporter) output(snap MetricsSnapshot) {
	// Log human-readable summary.
	r.logger.Println(snap.String())

	// Append CSV row if configured.
	if r.csvPath != "" {
		r.appendCSV(snap)
	}
}

func (r *Reporter) appendCSV(snap MetricsSnapshot) {
	if r.csvFile == nil {
		f, err := os.OpenFile(r.csvPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			r.logger.Printf("csv open error: %v", err)
			return
		}
		r.csvFile = f
		// Write header if the file is new (size == 0).
		info, _ := f.Stat()
		if info != nil && info.Size() == 0 {
			_ = WriteCSVHeader(f)
		}
	}
	_ = snap.WriteCSVRow(r.csvFile)
}

// PrintSummary writes a formatted summary table to w.
// Useful for end-of-experiment reports.
func PrintSummary(w io.Writer, snaps ...MetricsSnapshot) {
	if len(snaps) == 0 {
		return
	}
	fmt.Fprintln(w, "╔══════════════════════════════════════════════════════════════════╗")
	fmt.Fprintln(w, "║                    EXPERIMENT METRICS SUMMARY                   ║")
	fmt.Fprintln(w, "╠══════════════════════════════════════════════════════════════════╣")
	for _, s := range snaps {
		fmt.Fprintf(w, "║ Node %-4d │ uptime=%.1fs                                       ║\n", s.NodeID, s.UptimeSec)
		fmt.Fprintf(w, "║   Sent: %8d msgs  %10d bytes  (%.1f msg/s)           ║\n",
			s.MessagesSent, s.BytesSent, s.MsgSentPerSec)
		fmt.Fprintf(w, "║   Recv: %8d msgs  %10d bytes  (%.1f msg/s)           ║\n",
			s.MessagesRecv, s.BytesRecv, s.MsgRecvPerSec)
		fmt.Fprintf(w, "║   Drop: dedup=%d ttl=%d queue=%d err=%d rate=%.2f%%          ║\n",
			s.DedupDropped, s.TTLExpiredDropped, s.QueueOverflowDropped,
			s.SendErrors, s.DropRate*100)
		fmt.Fprintf(w, "║   Latency: avg=%.1f p50=%.1f p95=%.1f p99=%.1f max=%.1f ms    ║\n",
			s.AvgLatencyMs, s.P50LatencyMs, s.P95LatencyMs, s.P99LatencyMs, s.MaxLatencyMs)
		if s.UncompressedBytes > 0 {
			fmt.Fprintf(w, "║   Compression: %.2f%% (%d → %d bytes)                       ║\n",
				s.CompressionRatio*100, s.UncompressedBytes, s.CompressedBytes)
		}
		fmt.Fprintln(w, "╠══════════════════════════════════════════════════════════════════╣")
	}
	fmt.Fprintln(w, "╚══════════════════════════════════════════════════════════════════╝")
}
