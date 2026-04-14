// cmd/gossip_bench/main.go — Gossip 状态同步收敛测试
//
// 模拟 5 架无人机编队场景，每个节点拥有各自的位置信息（pos_X），
// 通过 Gossip 算法同步到所有节点。程序自动检测收敛并打印全部指标。
//
// 用法:
//
//	go run ./cmd/gossip_bench
//	go run ./cmd/gossip_bench -nodes=7 -fanout=2 -interval=300ms -timeout=30s
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"uav/node/algorithm/gossip"
	"uav/node/metrics"
	"uav/node/runtime"
	"uav/node/transport/udp"
)

// nodeBundle groups all per-node objects for easy access.
type nodeBundle struct {
	id   uint16
	node *runtime.Node
	algo *gossip.Algorithm
	mc   *metrics.Collector
	tr   *udp.Transport
}

func main() {
	// ── CLI flags ──────────────────────────────────────────────────────
	numNodes := flag.Int("nodes", 5, "Number of nodes in the cluster")
	fanout := flag.Int("fanout", 3, "Gossip fanout per round")
	interval := flag.Duration("interval", 200*time.Millisecond, "Gossip round interval")
	timeout := flag.Duration("timeout", 15*time.Second, "Max wait time for convergence")
	flag.Parse()

	log.SetFlags(log.Ltime | log.Lmicroseconds)
	log.Printf("=== Gossip 状态同步收敛测试 ===")
	log.Printf("配置: nodes=%d  fanout=%d  interval=%v  timeout=%v",
		*numNodes, *fanout, *interval, *timeout)

	// ── Build nodes ────────────────────────────────────────────────────
	nodes := make([]nodeBundle, *numNodes)
	for i := range nodes {
		id := uint16(i + 1)
		mc := metrics.NewCollector(id)
		tr := udp.New(":0", 256) // OS picks a free port
		n := runtime.NewNode(id, tr,
			runtime.WithSendQueueSize(512),
			runtime.WithRecvBufSize(256),
			runtime.WithMetrics(mc),
		)
		algo := gossip.New(n, *fanout, *interval)
		n.RegisterAlgorithm(algo)
		nodes[i] = nodeBundle{id: id, node: n, algo: algo, mc: mc, tr: tr}
	}

	// Start transports first so we can read LocalAddr.
	for i := range nodes {
		if err := nodes[i].node.Start(); err != nil {
			// Start failed, likely port issue — start transport manually.
			log.Fatalf("node %d Start: %v", nodes[i].id, err)
		}
	}

	// Register peers (full mesh).
	for i := range nodes {
		for j := range nodes {
			if i == j {
				continue
			}
			addr := nodes[j].tr.LocalAddr()
			_ = nodes[i].node.AddPeer(nodes[j].id, addr)
		}
	}

	// ── Seed initial state ─────────────────────────────────────────────
	// Each node owns a unique key "pos_X" with its initial position value.
	log.Println("")
	log.Println("── 初始状态 ──")
	for i := range nodes {
		key := fmt.Sprintf("pos_%d", nodes[i].id)
		val := fmt.Sprintf("x=%.1f,y=%.1f,z=%.1f",
			float64(nodes[i].id)*10.0,
			float64(nodes[i].id)*5.0,
			100.0)
		nodes[i].algo.Set(key, val)
		log.Printf("  节点 %d 设置 %s = %s", nodes[i].id, key, val)
	}

	// ── Wait for convergence ───────────────────────────────────────────
	log.Println("")
	log.Println("── 等待收敛 ──")
	expectedKeys := *numNodes
	startTime := time.Now()
	convergedAt := time.Duration(0)
	checkInterval := 50 * time.Millisecond

	deadline := time.After(*timeout)
	ticker := time.NewTicker(checkInterval)
	defer ticker.Stop()

	lastReport := time.Now()

convergenceLoop:
	for {
		select {
		case <-deadline:
			log.Println("⚠ 超时未收敛！")
			break convergenceLoop
		case <-ticker.C:
			allConverged := true
			for _, nb := range nodes {
				snap := nb.algo.StateSnapshot()
				if len(snap) < expectedKeys {
					allConverged = false
					break
				}
			}
			elapsed := time.Since(startTime)

			// Print progress every second.
			if time.Since(lastReport) > 1*time.Second {
				counts := make([]int, len(nodes))
				for i, nb := range nodes {
					counts[i] = len(nb.algo.StateSnapshot())
				}
				log.Printf("  [%.1fs] 各节点状态条目数: %v (目标=%d)",
					elapsed.Seconds(), counts, expectedKeys)
				lastReport = time.Now()
			}

			if allConverged {
				convergedAt = elapsed
				log.Printf("✅ 全部节点收敛！耗时 = %v", convergedAt)
				break convergenceLoop
			}
		}
	}

	// Let metrics accumulate a bit more after convergence for accurate rates.
	time.Sleep(200 * time.Millisecond)

	// ── Verify final state ─────────────────────────────────────────────
	log.Println("")
	log.Println("── 验证最终状态 ──")
	allMatch := true
	for _, nb := range nodes {
		snap := nb.algo.StateSnapshot()
		missing := 0
		for i := range nodes {
			key := fmt.Sprintf("pos_%d", nodes[i].id)
			if _, ok := snap[key]; !ok {
				missing++
			}
		}
		if missing > 0 {
			log.Printf("  ❌ 节点 %d: 缺少 %d 个 key", nb.id, missing)
			allMatch = false
		} else {
			log.Printf("  ✅ 节点 %d: 拥有全部 %d 个 key", nb.id, expectedKeys)
		}
	}

	// ── Print metrics ──────────────────────────────────────────────────
	log.Println("")
	log.Println("══════════════════════════════════════════════════════════════")
	log.Println("                     实验指标报告")
	log.Println("══════════════════════════════════════════════════════════════")

	snaps := make([]metrics.MetricsSnapshot, len(nodes))
	for i, nb := range nodes {
		snaps[i] = nb.mc.Snapshot()
	}

	var totalSent, totalRecv, totalBytesSent, totalBytesRecv uint64
	var totalDedup, totalTTL, totalQueueDrop, totalSendErr uint64

	for _, s := range snaps {
		log.Printf("── 节点 %d ──", s.NodeID)
		log.Printf("  发送: %d 条 (%d 字节, %.1f msg/s, %.1f KB/s)",
			s.MessagesSent, s.BytesSent, s.MsgSentPerSec, s.BytesSentPerSec/1024)
		log.Printf("  接收: %d 条 (%d 字节, %.1f msg/s, %.1f KB/s)",
			s.MessagesRecv, s.BytesRecv, s.MsgRecvPerSec, s.BytesRecvPerSec/1024)
		log.Printf("  丢弃: dedup=%d  ttl=%d  queue=%d  err=%d  丢弃率=%.2f%%",
			s.DedupDropped, s.TTLExpiredDropped, s.QueueOverflowDropped,
			s.SendErrors, s.DropRate*100)
		log.Printf("  延迟: avg=%.1fms  p50=%.1fms  p95=%.1fms  p99=%.1fms  max=%.1fms",
			s.AvgLatencyMs, s.P50LatencyMs, s.P95LatencyMs, s.P99LatencyMs, s.MaxLatencyMs)

		totalSent += s.MessagesSent
		totalRecv += s.MessagesRecv
		totalBytesSent += s.BytesSent
		totalBytesRecv += s.BytesRecv
		totalDedup += s.DedupDropped
		totalTTL += s.TTLExpiredDropped
		totalQueueDrop += s.QueueOverflowDropped
		totalSendErr += s.SendErrors
	}

	log.Println("")
	log.Println("── 汇总 ──")
	log.Printf("  收敛时间:       %v", convergedAt)
	log.Printf("  总发送消息:     %d 条", totalSent)
	log.Printf("  总接收消息:     %d 条", totalRecv)
	log.Printf("  总发送字节:     %d (%.2f KB)", totalBytesSent, float64(totalBytesSent)/1024)
	log.Printf("  总接收字节:     %d (%.2f KB)", totalBytesRecv, float64(totalBytesRecv)/1024)
	log.Printf("  平均每节点发送: %.1f 条  %.2f KB",
		float64(totalSent)/float64(*numNodes), float64(totalBytesSent)/float64(*numNodes)/1024)
	log.Printf("  去重丢弃总计:   %d", totalDedup)
	log.Printf("  TTL过期总计:    %d", totalTTL)
	log.Printf("  队列溢出总计:   %d", totalQueueDrop)
	log.Printf("  发送失败总计:   %d", totalSendErr)
	if convergedAt > 0 {
		log.Printf("  共识成功:       ✅ (所有节点一致)")
	} else {
		log.Printf("  共识成功:       ❌ (未能收敛)")
	}
	log.Println("══════════════════════════════════════════════════════════════")

	// ── Export JSON ─────────────────────────────────────────────────────
	for i, s := range snaps {
		path := fmt.Sprintf("gossip_bench_node%d.json", nodes[i].id)
		if err := s.ExportJSON(path); err != nil {
			log.Printf("导出 %s 失败: %v", path, err)
		} else {
			log.Printf("已导出: %s", path)
		}
	}

	// ── Shutdown ────────────────────────────────────────────────────────
	for i := range nodes {
		nodes[i].node.Stop()
	}

	if !allMatch {
		os.Exit(1)
	}
}
