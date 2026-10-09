// Command peercash-bridge is a solo stratum bridge: it lets a standard
// XMRig (RandomX, rx/0) mine against a peercash node's eth_getWork /
// eth_submitWork RPC. It polls the node for work, serves Monero/XMRig stratum
// over TCP, and relays found block solutions back to the node, which verifies
// and inserts them. Rewards go to the node's configured --miner.etherbase.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"log"
	"net"
	"net/http"
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
	stratumAddr := flag.String("stratum", "127.0.0.1:3333", "stratum listen address (loopback-only by default)")
	statsAddr := flag.String("stats", "", "optional stats HTTP endpoint, e.g. 127.0.0.1:8090 (off by default)")
	pollInterval := flag.Duration("poll", 500*time.Millisecond, "eth_getWork poll interval")
	maxConns := flag.Int("max-conns", 256, "max total simultaneous connections")
	maxPerIP := flag.Int("max-per-ip", 16, "max simultaneous connections from one IP")
	flag.Parse()

	cfg := stratum.DefaultConfig()
	cfg.MaxConns = *maxConns
	cfg.MaxPerIP = *maxPerIP

	if !isLoopback(*stratumAddr) {
		log.Printf("WARNING: -stratum %s is not loopback. This is a SOLO bridge: every "+
			"block any connected miner finds pays the node's configured etherbase, "+
			"regardless of the miner's login. Only expose it to miners you intend to "+
			"pay that one address.", *stratumAddr)
	}

	client := node.New(*nodeURL)
	srv := stratum.NewServerWithConfig(*stratumAddr, client, cfg)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	p := &poller{client: client, srv: srv}
	// Fetch once synchronously so the first worker to log in already has a job.
	if err := p.fetch(ctx); err != nil {
		log.Printf("warning: initial getWork failed (%v); will keep retrying", err)
	}
	go p.run(ctx, *pollInterval)

	if *statsAddr != "" {
		go serveStats(ctx, *statsAddr, srv)
	}

	log.Printf("peercash-bridge: node=%s stratum=%s poll=%s maxConns=%d maxPerIP=%d",
		*nodeURL, *stratumAddr, *pollInterval, cfg.MaxConns, cfg.MaxPerIP)
	if err := srv.ListenAndServe(ctx); err != nil {
		log.Fatal(err)
	}
	log.Printf("peercash-bridge: shut down cleanly")
}

// isLoopback reports whether a "host:port" listen address binds only loopback.
// An empty or wildcard host (":3333", "0.0.0.0:3333", "[::]:3333") is not
// loopback; a resolvable non-loopback IP/host is not loopback either.
func isLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil || host == "" || host == "0.0.0.0" || host == "::" {
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return host == "localhost"
}

// serveStats runs the optional read-only stats endpoint until ctx is done.
func serveStats(ctx context.Context, addr string, srv *stratum.Server) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(srv.Snapshot())
	})
	hs := &http.Server{Addr: addr, Handler: mux}
	go func() { <-ctx.Done(); hs.Close() }()
	log.Printf("stats endpoint on http://%s", addr)
	if err := hs.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Printf("stats endpoint: %v", err)
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
