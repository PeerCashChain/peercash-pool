// Command peercash-bridge is a solo stratum bridge: it lets a standard
// XMRig (RandomX, rx/0) mine against a peercash node's eth_getWork /
// eth_submitWork RPC. It polls the node for work, serves Monero/XMRig stratum
// over TCP, and relays found block solutions back to the node, which verifies
// and inserts them. Rewards go to the node's configured --miner.etherbase.
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/PeerCashChain/peercash-pool/internal/node"
	"github.com/PeerCashChain/peercash-pool/internal/stratum"
)

func main() {
	nodeURL := flag.String("node", "http://127.0.0.1:8545", "node JSON-RPC HTTP endpoint (eth_getWork/eth_submitWork)")
	stratumAddr := flag.String("stratum", ":3333", "stratum listen address")
	pollInterval := flag.Duration("poll", 500*time.Millisecond, "eth_getWork poll interval")
	flag.Parse()

	client := node.New(*nodeURL)
	srv := stratum.NewServer(*stratumAddr, client)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	p := &poller{client: client, srv: srv}
	// Fetch once synchronously so the first worker to log in already has a job.
	if err := p.fetch(ctx); err != nil {
		log.Printf("warning: initial getWork failed (%v); will keep retrying", err)
	}
	go p.run(ctx, *pollInterval)

	log.Printf("peercash-bridge: node=%s stratum=%s poll=%s", *nodeURL, *stratumAddr, *pollInterval)
	if err := srv.ListenAndServe(ctx); err != nil {
		log.Fatal(err)
	}
}

// poller watches the node's eth_getWork and installs new work on the server
// whenever the seal hash changes.
type poller struct {
	client   *node.Client
	srv      *stratum.Server
	lastSeal string
	jobCtr   uint64
}

func (p *poller) run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := p.fetch(ctx); err != nil {
				log.Printf("getWork: %v", err)
			}
		}
	}
}

func (p *poller) fetch(ctx context.Context) error {
	w, err := p.client.GetWork(ctx)
	if err != nil {
		return err
	}
	if w.SealHash == p.lastSeal {
		return nil
	}
	// New work: find the height for display (best-effort; XMRig drives the
	// RandomX dataset off seed_hash, not height).
	var height uint64
	if h, err := p.client.BlockNumber(ctx); err == nil {
		height = h + 1
	}
	p.jobCtr++
	work, err := stratum.NewWork(strconv.FormatUint(p.jobCtr, 16), w.SealHash, w.SeedHash, w.Target, height)
	if err != nil {
		return err
	}
	p.lastSeal = w.SealHash
	p.srv.SetWork(work)
	return nil
}
