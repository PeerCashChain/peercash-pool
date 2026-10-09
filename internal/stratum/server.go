package stratum

import (
	"bufio"
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// recentWorks bounds how many past job_ids the server remembers, so a worker
// submitting for a job that was just replaced still resolves. Mirrors the
// node's own remoteWorkCacheSize. The per-job seen-nonce sets are evicted
// alongside their job_id, so their memory is bounded too.
const recentWorks = 16

// maxLineLen caps a single stratum message (including the newline). XMRig's
// login/submit lines are well under 300 bytes; 4 KB is generous headroom while
// still refusing an unbounded read from a hostile peer.
const maxLineLen = 4096

// errLineTooLong is returned by readLine when a line exceeds maxLineLen.
var errLineTooLong = fmt.Errorf("stratum: line exceeds %d bytes", maxLineLen)

// Config tunes the server's hardening limits. The zero value is not useful;
// use DefaultConfig and override as needed.
type Config struct {
	MaxConns     int           // max total simultaneous connections
	MaxPerIP     int           // max simultaneous connections from one IP
	LoginTimeout time.Duration // deadline for the first (login) message
	IdleTimeout  time.Duration // read deadline between messages once logged in
	WriteTimeout time.Duration // deadline for a single write in send()
	SubmitRate   int           // max submits per connection per minute
	BanThreshold int           // invalid submits from an IP before a ban
	BanWindow    time.Duration // window the invalid-submit count is measured over
	BanDuration  time.Duration // how long a ban lasts
}

// DefaultConfig returns the hardening defaults documented in the README/flags.
func DefaultConfig() Config {
	return Config{
		MaxConns:     256,
		MaxPerIP:     16,
		LoginTimeout: 10 * time.Second,
		IdleTimeout:  5 * time.Minute,
		WriteTimeout: 10 * time.Second,
		SubmitRate:   10,
		BanThreshold: 5,
		BanWindow:    10 * time.Minute,
		BanDuration:  30 * time.Minute,
	}
}

// Submitter is the node side the server relays solutions to.
type Submitter interface {
	SubmitWork(ctx context.Context, nonce, sealHash, mixDigest string) (bool, error)
}

// workEntry is the server's memory of one job: the seal hash it relays to the
// node, the network target for local pre-checks, and the set of header nonces
// already submitted for it (to reject duplicates without touching the node).
type workEntry struct {
	sealHash [32]byte
	target   uint64
	nonces   map[string]struct{}
}

// Server is the stratum TCP front-end: it fans the current Work out to all
// connected XMRig clients and relays their submits to the node.
type Server struct {
	addr      string
	submitter Submitter
	cfg       Config

	mu       sync.Mutex
	conns    map[*conn]struct{}
	ipConns  map[string]int         // active connections per IP
	bans     map[string]time.Time   // IP -> ban expiry
	invalids map[string][]time.Time // IP -> recent invalid-submit times
	current  *Work
	works    map[string]*workEntry // job_id -> job memory
	workRing []string              // FIFO of job_ids for eviction
	extraCtr uint32
	sessCtr  uint64

	// stats counters (atomic; read by the optional stats endpoint)
	stAccepted atomic.Uint64
	stRejected atomic.Uint64
	stBans     atomic.Uint64
	stBlocks   atomic.Uint64
}

// conn is one connected miner. Fields under "// guarded by s.mu" are written
// once at login and read from the SetWork / stats goroutines; the rate-limit
// and auth counters are touched only by the connection's own read loop.
type conn struct {
	nc      net.Conn
	writeMu sync.Mutex
	writeTO time.Duration
	ip      string

	// guarded by s.mu
	loggedIn    bool
	sessionID   string
	extranonce  [4]byte
	rigID       string
	login       string
	connectedAt time.Time

	// read-loop-local (no lock needed)
	notAuthed int       // submit/keepalived seen before login
	subWindow time.Time // start of the current submit-rate window
	subCount  int       // submits counted in the current window
}

// NewServer creates a stratum server with default hardening config.
func NewServer(addr string, submitter Submitter) *Server {
	return NewServerWithConfig(addr, submitter, DefaultConfig())
}

// NewServerWithConfig creates a stratum server with explicit config.
func NewServerWithConfig(addr string, submitter Submitter, cfg Config) *Server {
	return &Server{
		addr:      addr,
		submitter: submitter,
		cfg:       cfg,
		conns:     make(map[*conn]struct{}),
		ipConns:   make(map[string]int),
		bans:      make(map[string]time.Time),
		invalids:  make(map[string][]time.Time),
		works:     make(map[string]*workEntry),
	}
}

// ListenAndServe binds the configured address and serves until ctx is done.
func (s *Server) ListenAndServe(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return err
	}
	return s.Serve(ctx, ln)
}

// Serve accepts connections on ln until ctx is done. On shutdown it stops
// accepting and closes every live connection so their goroutines unwind.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	log.Printf("stratum listening on %s (algo rx/0)", ln.Addr())
	go func() {
		<-ctx.Done()
		ln.Close()
		s.closeAll()
	}()
	for {
		nc, err := ln.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			default:
				return err
			}
		}
		go s.handleConn(ctx, nc)
	}
}

// closeAll closes every registered connection (graceful shutdown).
func (s *Server) closeAll() {
	s.mu.Lock()
	cs := make([]*conn, 0, len(s.conns))
	for c := range s.conns {
		cs = append(cs, c)
	}
	s.mu.Unlock()
	for _, c := range cs {
		c.nc.Close()
	}
}

// SetWork installs new work and pushes it to every logged-in worker.
func (s *Server) SetWork(w *Work) {
	s.mu.Lock()
	s.current = w
	s.works[w.JobID] = &workEntry{
		sealHash: w.SealHash,
		target:   w.TargetVal,
		nonces:   make(map[string]struct{}),
	}
	s.workRing = append(s.workRing, w.JobID)
	if len(s.workRing) > recentWorks {
		delete(s.works, s.workRing[0])
		s.workRing = s.workRing[1:]
	}
	conns := make([]*conn, 0, len(s.conns))
	for c := range s.conns {
		if c.loggedIn {
			conns = append(conns, c)
		}
	}
	s.mu.Unlock()

	for _, c := range conns {
		c.send(notification{JSONRPC: "2.0", Method: "job", Params: buildJob(w, c.extranonce)})
	}
	log.Printf("new job %s seal=0x%s... height=%d workers=%d",
		w.JobID, hex.EncodeToString(w.SealHash[:4]), w.Height, len(conns))
}

func buildJob(w *Work, extranonce [4]byte) *job {
	blob := buildBlob(w.SealHash, extranonce)
	return &job{
		Blob:     hex.EncodeToString(blob[:]),
		JobID:    w.JobID,
		Target:   w.TargetLE,
		SeedHash: w.SeedHex,
		Algo:     "rx/0",
		Height:   w.Height,
	}
}

// register admits a new connection unless the peer is banned or over a limit.
func (s *Server) register(c *conn) (ok bool, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if exp, banned := s.bans[c.ip]; banned {
		if time.Now().Before(exp) {
			return false, "banned"
		}
		delete(s.bans, c.ip)
	}
	if len(s.conns) >= s.cfg.MaxConns {
		return false, "server full"
	}
	if s.ipConns[c.ip] >= s.cfg.MaxPerIP {
		return false, "per-IP limit"
	}
	s.conns[c] = struct{}{}
	s.ipConns[c.ip]++
	return true, ""
}

// unregister removes a connection from the active accounting.
func (s *Server) unregister(c *conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.conns[c]; !ok {
		return
	}
	delete(s.conns, c)
	if s.ipConns[c.ip] <= 1 {
		delete(s.ipConns, c.ip)
	} else {
		s.ipConns[c.ip]--
	}
}

// readLine reads one '\n'-terminated line, returning errLineTooLong if it would
// exceed maxLineLen. The trailing newline is not included.
func readLine(r *bufio.Reader) ([]byte, error) {
	var buf []byte
	for {
		b, err := r.ReadByte()
		if err != nil {
			return nil, err
		}
		if b == '\n' {
			return buf, nil
		}
		if len(buf) >= maxLineLen {
			return nil, errLineTooLong
		}
		buf = append(buf, b)
	}
}

func (s *Server) handleConn(ctx context.Context, nc net.Conn) {
	host, _, _ := net.SplitHostPort(nc.RemoteAddr().String())
	c := &conn{nc: nc, ip: host, writeTO: s.cfg.WriteTimeout, connectedAt: time.Now()}

	if ok, reason := s.register(c); !ok {
		log.Printf("refused %s: %s", nc.RemoteAddr(), reason)
		nc.Close()
		return
	}
	defer func() {
		s.unregister(c)
		nc.Close()
	}()

	reader := bufio.NewReaderSize(nc, maxLineLen)
	// The first message (login) must arrive within LoginTimeout; thereafter the
	// read deadline is reset to IdleTimeout on each loop iteration.
	_ = nc.SetReadDeadline(time.Now().Add(s.cfg.LoginTimeout))
	for {
		if c.loggedInLocked(s) {
			_ = nc.SetReadDeadline(time.Now().Add(s.cfg.IdleTimeout))
		}
		line, err := readLine(reader)
		if err != nil {
			if err == errLineTooLong {
				log.Printf("line too long from %s, closing", nc.RemoteAddr())
			}
			return
		}
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var req request
		if err := json.Unmarshal(line, &req); err != nil {
			continue
		}
		loggedIn := c.loggedInLocked(s)
		switch req.Method {
		case "login":
			var p loginParams
			_ = json.Unmarshal(req.Params, &p)
			s.handleLogin(c, req.ID, p)
		case "submit":
			if !loggedIn {
				if c.preLoginStrike() {
					return
				}
				continue // ignore submits until logged in
			}
			var p submitParams
			if err := json.Unmarshal(req.Params, &p); err != nil {
				c.sendError(req.ID, -1, "bad params")
				continue
			}
			if s.handleSubmit(ctx, c, req.ID, p) {
				return // this connection's IP was banned mid-submit
			}
		case "keepalived":
			if !loggedIn {
				if c.preLoginStrike() {
					return
				}
				continue // ignore keepalives until logged in
			}
			c.sendResult(req.ID, statusResult{Status: "KEEPALIVED"})
		default:
			// Unknown methods are ignored (XMRig does not require a reply).
		}
	}
}

// loggedInLocked reads c.loggedIn under s.mu.
func (c *conn) loggedInLocked(s *Server) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return c.loggedIn
}

// preLoginStrike counts a submit/keepalived received before login and reports
// whether the connection should now be closed (after 3 such messages).
func (c *conn) preLoginStrike() bool {
	c.notAuthed++
	if c.notAuthed >= 3 {
		log.Printf("closing %s: %d messages before login", c.nc.RemoteAddr(), c.notAuthed)
		return true
	}
	return false
}

func (s *Server) handleLogin(c *conn, id json.RawMessage, p loginParams) {
	s.mu.Lock()
	s.extraCtr++
	c.extranonce = extranonceBytes(s.extraCtr)
	s.sessCtr++
	c.sessionID = fmt.Sprintf("%016x", s.sessCtr)
	c.rigID = p.RigID
	c.login = p.Login
	c.loggedIn = true
	cur := s.current
	s.mu.Unlock()

	var j *job
	if cur != nil {
		j = buildJob(cur, c.extranonce)
	}
	c.sendResult(id, loginResult{
		ID:         c.sessionID,
		Job:        j,
		Status:     "OK",
		Note:       "solo bridge: rewards go to the node's configured etherbase, not this login",
		Extensions: []string{},
	})
	log.Printf("worker login %s addr=%q rigid=%q agent=%q extranonce=%s",
		c.nc.RemoteAddr(), p.Login, p.RigID, p.Agent, hex.EncodeToString(c.extranonce[:]))
}

// handleSubmit validates a submission locally and, only if it could be a real
// block, relays it to the node. It returns true if the connection's IP was
// banned as a result (the caller then closes this connection).
//
// Validation order (cheapest first, never touching the node until the end):
//   - submit rate limit per connection
//   - well-formed nonce and 32-byte result
//   - known (non-stale) job
//   - not a duplicate (job_id, header-nonce)
//   - meets the network target (last 8 bytes LE <= target), with no RandomX —
//     the node still runs the authoritative RandomX check on what we forward.
func (s *Server) handleSubmit(ctx context.Context, c *conn, id json.RawMessage, p submitParams) (banned bool) {
	// Per-connection submit rate limit. Real block solutions are rare, so this
	// only ever trips on a misbehaving/spamming client. Not counted as an
	// invalid submit (it is throttling, not a bad share).
	now := time.Now()
	if now.Sub(c.subWindow) >= time.Minute {
		c.subWindow = now
		c.subCount = 0
	}
	c.subCount++
	if c.subCount > s.cfg.SubmitRate {
		s.stRejected.Add(1)
		c.sendError(id, -1, "rate limited")
		return false
	}

	nonce4, err := parseNonce4(p.Nonce)
	if err != nil {
		c.sendError(id, -1, "bad nonce")
		return s.markInvalid(c, "bad nonce")
	}
	result := strings.TrimPrefix(p.Result, "0x")
	rb, err := hex.DecodeString(result)
	if err != nil || len(rb) != 32 {
		c.sendError(id, -1, "bad result")
		return s.markInvalid(c, "bad result")
	}
	var result32 [32]byte
	copy(result32[:], rb)

	nonceHex := headerNonce(c.extranonce, nonce4)

	s.mu.Lock()
	we, known := s.works[p.JobID]
	if !known {
		s.mu.Unlock()
		s.stRejected.Add(1)
		c.sendError(id, -1, "job not found")
		// Stale/unknown job is not treated as malicious (a late submit for a
		// just-rotated job is normal), so it does not count toward a ban.
		log.Printf("reject (stale/unknown job %s) from %s", p.JobID, c.nc.RemoteAddr())
		return false
	}
	nonceKey := nonceHex
	if _, dup := we.nonces[nonceKey]; dup {
		s.mu.Unlock()
		c.sendError(id, -1, "duplicate share")
		return s.markInvalid(c, "duplicate share")
	}
	we.nonces[nonceKey] = struct{}{}
	target := we.target
	sealHash := we.sealHash
	s.mu.Unlock()

	// Local target check — no RandomX. A result that cannot meet the network
	// target can never be a block, so we reject it here and never ask the node
	// to run RandomX on spam. The node still does the real RandomX verification
	// on anything that passes this check.
	if !meetsTarget(result32, target) {
		c.sendError(id, -1, "low difficulty share")
		return s.markInvalid(c, "below target")
	}

	sealHex := "0x" + hex.EncodeToString(sealHash[:])
	mixHex := "0x" + result

	subCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	accepted, err := s.submitter.SubmitWork(subCtx, nonceHex, sealHex, mixHex)
	if err != nil {
		s.stRejected.Add(1)
		c.sendError(id, -1, "submit error")
		log.Printf("submit error job=%s worker=%s: %v", p.JobID, c.nc.RemoteAddr(), err)
		return false
	}
	if accepted {
		s.stAccepted.Add(1)
		s.stBlocks.Add(1)
		c.sendResult(id, statusResult{Status: "OK"})
		log.Printf("BLOCK ACCEPTED job=%s worker=%s nonce=%s", p.JobID, c.nc.RemoteAddr(), nonceHex)
	} else {
		// Met target locally but the node refused (usually a race with a new
		// head). Not the miner's fault, so it does not count toward a ban.
		s.stRejected.Add(1)
		c.sendError(id, -1, "rejected by node")
		log.Printf("reject (node refused) job=%s worker=%s nonce=%s", p.JobID, c.nc.RemoteAddr(), nonceHex)
	}
	return false
}

// markInvalid records an invalid (miner-fault) submit against the IP and, if the
// ban threshold is reached within the window, bans the IP and closes its live
// connections. Returns true if this connection's IP was just banned.
func (s *Server) markInvalid(c *conn, reason string) (banned bool) {
	s.stRejected.Add(1)
	log.Printf("invalid submit (%s) from %s", reason, c.nc.RemoteAddr())

	s.mu.Lock()
	now := time.Now()
	cutoff := now.Add(-s.cfg.BanWindow)
	times := s.invalids[c.ip][:0]
	for _, t := range s.invalids[c.ip] {
		if t.After(cutoff) {
			times = append(times, t)
		}
	}
	times = append(times, now)
	s.invalids[c.ip] = times
	if len(times) < s.cfg.BanThreshold {
		s.mu.Unlock()
		return false
	}
	// Ban: record expiry, drop the invalid history, collect this IP's conns.
	s.bans[c.ip] = now.Add(s.cfg.BanDuration)
	delete(s.invalids, c.ip)
	var victims []*conn
	for cc := range s.conns {
		if cc.ip == c.ip {
			victims = append(victims, cc)
		}
	}
	s.mu.Unlock()

	s.stBans.Add(1)
	log.Printf("BANNED %s for %s after %d invalid submits", c.ip, s.cfg.BanDuration, s.cfg.BanThreshold)
	for _, cc := range victims {
		cc.nc.Close()
	}
	return true
}

// send marshals v as a single line (JSON + '\n') under the write lock, with a
// write deadline so a stuck client cannot block job pushes to others.
func (c *conn) send(v interface{}) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	b = append(b, '\n')
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_ = c.nc.SetWriteDeadline(time.Now().Add(c.writeTO))
	_, _ = c.nc.Write(b)
}

func (c *conn) sendResult(id json.RawMessage, result interface{}) {
	c.send(response{JSONRPC: "2.0", ID: id, Result: result})
}

func (c *conn) sendError(id json.RawMessage, code int, msg string) {
	c.send(response{JSONRPC: "2.0", ID: id, Error: &rpcErr{Code: code, Message: msg}})
}

// WorkerStat describes one connected, logged-in worker for the stats endpoint.
type WorkerStat struct {
	IP             string    `json:"ip"`
	RigID          string    `json:"rig_id"`
	Login          string    `json:"login"`
	ConnectedSince time.Time `json:"connected_since"`
}

// Stats is a point-in-time snapshot served by the optional stats endpoint.
type Stats struct {
	Workers         []WorkerStat `json:"workers"`
	SubmitsAccepted uint64       `json:"submits_accepted"`
	SubmitsRejected uint64       `json:"submits_rejected"`
	Bans            uint64       `json:"bans"`
	BlocksAccepted  uint64       `json:"blocks_accepted"`
	JobHeight       uint64       `json:"job_height"`
}

// Snapshot returns the current server stats (safe to call concurrently).
func (s *Server) Snapshot() Stats {
	s.mu.Lock()
	workers := make([]WorkerStat, 0, len(s.conns))
	for c := range s.conns {
		if !c.loggedIn {
			continue
		}
		workers = append(workers, WorkerStat{
			IP:             c.ip,
			RigID:          c.rigID,
			Login:          c.login,
			ConnectedSince: c.connectedAt,
		})
	}
	var height uint64
	if s.current != nil {
		height = s.current.Height
	}
	s.mu.Unlock()
	return Stats{
		Workers:         workers,
		SubmitsAccepted: s.stAccepted.Load(),
		SubmitsRejected: s.stRejected.Load(),
		Bans:            s.stBans.Load(),
		BlocksAccepted:  s.stBlocks.Load(),
		JobHeight:       height,
	}
}
