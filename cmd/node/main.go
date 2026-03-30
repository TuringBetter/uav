// cmd/node/main.go — Example entry point demonstrating the three-layer architecture.
//
// Usage:
//
//	# Node 1
//	go run ./cmd/node --id=1 --addr=:9001 --peers=2=:9002,3=:9003 --algo=gossip
//
//	# Node 2
//	go run ./cmd/node --id=2 --addr=:9002 --peers=1=:9001,3=:9003 --algo=gossip
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
	"uav/node/algorithm/gossip"
	"uav/node/algorithm/raft"
	"uav/node/algorithm/weaknet"
	"uav/node/runtime"
	"uav/node/transport/udp"
)

func main() {
	idFlag := flag.Uint("id", 1, "This node's unique ID (1–65535)")
	addrFlag := flag.String("addr", ":9001", "Local listen address")
	peersFlag := flag.String("peers", "", "Comma-separated peer list: id=addr,id=addr ...")
	algoFlag := flag.String("algo", "gossip", "Algorithm to run: gossip | raft | weaknet")
	flag.Parse()

	nodeID := uint16(*idFlag)
	peers, err := parsePeers(*peersFlag)
	if err != nil {
		log.Fatalf("parse peers: %v", err)
	}

	// ── 1. Network transport layer ──────────────────────────────────────────
	tr := udp.New(*addrFlag, 256)

	// ── 2. Node communication framework layer ──────────────────────────────
	n := runtime.NewNode(nodeID, tr,
		runtime.WithSendQueueSize(512),
		runtime.WithRecvBufSize(256),
	)
	for id, addr := range peers {
		if err := n.AddPeer(id, addr); err != nil {
			log.Printf("warn: addPeer %d: %v", id, err)
		}
	}

	// ── 3. Distributed algorithm layer ─────────────────────────────────────
	switch *algoFlag {
	case "gossip":
		cfg := gossip.New(n, 3, 200*time.Millisecond)
		n.RegisterAlgorithm(cfg)
		log.Printf("[node %d] running Gossip algorithm", nodeID)

	case "raft":
		r := raft.New(n, raft.DefaultConfig(), func(idx uint32, cmd []byte) {
			log.Printf("[node %d] raft applied[%d]: %s", nodeID, idx, cmd)
		})
		n.RegisterAlgorithm(r)
		log.Printf("[node %d] running Raft algorithm", nodeID)

	case "weaknet":
		wn := weaknet.New(n, weaknet.DefaultConfig())
		n.RegisterAlgorithm(wn)
		// Seed a local state entry for demonstration.
		wn.Set("nodeID", []byte(strconv.Itoa(int(nodeID))))
		log.Printf("[node %d] running WeakNet Push-Pull algorithm", nodeID)

	default:
		fmt.Fprintf(os.Stderr, "unknown algorithm %q (choose: gossip | raft | weaknet)\n", *algoFlag)
		os.Exit(1)
	}

	// ── Start ────────────────────────────────────────────────────────────────
	if err := n.Start(); err != nil {
		log.Fatalf("node start: %v", err)
	}
	log.Printf("[node %d] listening on %s", nodeID, tr.LocalAddr())

	// ── Graceful shutdown ────────────────────────────────────────────────────
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig

	log.Printf("[node %d] shutting down…", nodeID)
	n.Stop()
}

// parsePeers parses "2=:9002,3=:9003" into map[uint16]string.
func parsePeers(s string) (map[uint16]string, error) {
	out := make(map[uint16]string)
	if s == "" {
		return out, nil
	}
	for _, item := range strings.Split(s, ",") {
		parts := strings.SplitN(item, "=", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid peer spec %q (want id=addr)", item)
		}
		id64, err := strconv.ParseUint(strings.TrimSpace(parts[0]), 10, 16)
		if err != nil {
			return nil, fmt.Errorf("invalid peer ID %q: %w", parts[0], err)
		}
		out[uint16(id64)] = strings.TrimSpace(parts[1])
	}
	return out, nil
}
