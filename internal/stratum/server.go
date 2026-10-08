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
	"time"
)

// recentWorks bounds how many past job_ids the server remembers, so a worker
// submitting for a job that was just replaced still resolves. Mirrors the
// node's own remoteWorkCacheSize.
const recentWorks = 16

// Submitter is the node side the server relays solutions to.
type Submitter interface {
	SubmitWork(ctx context.Context, nonce, sealHash, mixDigest string) (bool, error)
}

// Server is the stratum TCP front-end: it fans the current Work out to all
// connected XMRig clients and relays their submits to the node.
type Server struct {
	addr      string
	submitter Submitter

	mu       sync.Mutex
	conns    map[*conn]struct{}
	current  *Work
	works    map[string][32]byte // job_id -> sealHash
	workRing []string            // FIFO of job_ids for eviction
	extraCtr uint32
	sessCtr  uint64
}

// conn is one connected miner.
type conn struct {
	nc         net.Conn
	writeMu    sync.Mutex
	sessionID  string
	extranonce [4]byte
}

// NewServer creates a stratum server that relays solutions via submitter.
func NewServer(addr string, submitter Submitter) *Server {
	return &Server{
		addr:      addr,
		submitter: submitter,
		conns:     make(map[*conn]struct{}),
		works:     make(map[string][32]byte),
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

// Serve accepts connections on ln until ctx is done.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	log.Printf("stratum listening on %s (algo rx/0)", ln.Addr())
	go func() { <-ctx.Done(); ln.Close() }()
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
		go s.handleConn(ctx, &conn{nc: nc})
	}
}

// SetWork installs new work and pushes it to every connected worker.
func (s *Server) SetWork(w *Work) {
	s.mu.Lock()
	s.current = w
	s.works[w.JobID] = w.SealHash
	s.workRing = append(s.workRing, w.JobID)
	if len(s.workRing) > recentWorks {
		delete(s.works, s.workRing[0])
		s.workRing = s.workRing[1:]
	}
	conns := make([]*conn, 0, len(s.conns))
	for c := range s.conns {
		conns = append(conns, c)
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

func (s *Server) handleConn(ctx context.Context, c *conn) {
	defer func() {
		s.mu.Lock()
		delete(s.conns, c)
		s.mu.Unlock()
		c.nc.Close()
	}()

	reader := bufio.NewReader(c.nc)
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
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
		switch req.Method {
		case "login":
			var p loginParams
			_ = json.Unmarshal(req.Params, &p)
			s.handleLogin(c, req.ID)
		case "submit":
			var p submitParams
			if err := json.Unmarshal(req.Params, &p); err != nil {
				c.sendError(req.ID, -1, "bad params")
				continue
			}
			s.handleSubmit(ctx, c, req.ID, p)
		case "keepalived":
			c.sendResult(req.ID, statusResult{Status: "KEEPALIVED"})
		default:
			// Unknown methods are ignored (XMRig does not require a reply).
		}
	}
}

func (s *Server) handleLogin(c *conn, id json.RawMessage) {
	s.mu.Lock()
	s.extraCtr++
	c.extranonce = extranonceBytes(s.extraCtr)
	s.sessCtr++
	c.sessionID = fmt.Sprintf("%016x", s.sessCtr)
	s.conns[c] = struct{}{}
	cur := s.current
	s.mu.Unlock()

	var j *job
	if cur != nil {
		j = buildJob(cur, c.extranonce)
	}
	c.sendResult(id, loginResult{ID: c.sessionID, Job: j, Status: "OK", Extensions: []string{}})
	log.Printf("worker login %s extranonce=%s", c.nc.RemoteAddr(), hex.EncodeToString(c.extranonce[:]))
}

func (s *Server) handleSubmit(ctx context.Context, c *conn, id json.RawMessage, p submitParams) {
	s.mu.Lock()
	sealHash, known := s.works[p.JobID]
	s.mu.Unlock()
	if !known {
		c.sendError(id, -1, "job not found")
		log.Printf("reject (stale/unknown job %s) from %s", p.JobID, c.nc.RemoteAddr())
		return
	}
	nonce4, err := parseNonce4(p.Nonce)
	if err != nil {
		c.sendError(id, -1, "bad nonce")
		return
	}
	result := strings.TrimPrefix(p.Result, "0x")
	if len(result) != 64 {
		c.sendError(id, -1, "bad result")
		return
	}

	nonceHex := headerNonce(c.extranonce, nonce4)
	sealHex := "0x" + hex.EncodeToString(sealHash[:])
	mixHex := "0x" + result

	subCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	accepted, err := s.submitter.SubmitWork(subCtx, nonceHex, sealHex, mixHex)
	if err != nil {
		c.sendError(id, -1, "submit error")
		log.Printf("submit error job=%s worker=%s: %v", p.JobID, c.nc.RemoteAddr(), err)
		return
	}
	if accepted {
		c.sendResult(id, statusResult{Status: "OK"})
		log.Printf("BLOCK ACCEPTED job=%s worker=%s nonce=%s", p.JobID, c.nc.RemoteAddr(), nonceHex)
	} else {
		c.sendError(id, -1, "rejected by node")
		log.Printf("reject (node refused) job=%s worker=%s nonce=%s", p.JobID, c.nc.RemoteAddr(), nonceHex)
	}
}

// send marshals v as a single line (JSON + '\n') under the write lock.
func (c *conn) send(v interface{}) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	b = append(b, '\n')
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_, _ = c.nc.Write(b)
}

func (c *conn) sendResult(id json.RawMessage, result interface{}) {
	c.send(response{JSONRPC: "2.0", ID: id, Result: result})
}

func (c *conn) sendError(id json.RawMessage, code int, msg string) {
	c.send(response{JSONRPC: "2.0", ID: id, Error: &rpcErr{Code: code, Message: msg}})
}
