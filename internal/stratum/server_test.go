package stratum

import (
	"bufio"
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// mockSubmitter captures the eth_submitWork arguments the server produces and
// counts how many times it was called (so tests can assert the node was never
// contacted for a submission the server should have rejected locally).
type mockSubmitter struct {
	ch    chan [3]string
	ret   bool
	calls atomic.Int32
}

func (m *mockSubmitter) SubmitWork(ctx context.Context, nonce, sealHash, mixDigest string) (bool, error) {
	m.calls.Add(1)
	select {
	case m.ch <- [3]string{nonce, sealHash, mixDigest}:
	default:
	}
	return m.ret, nil
}

// TestLoginJobSubmit drives the server like a real XMRig: login, receive a job,
// submit a solution, and assert the exact arguments relayed to the node.
func TestLoginJobSubmit(t *testing.T) {
	ms := &mockSubmitter{ch: make(chan [3]string, 1), ret: true}
	srv := NewServer("", ms)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Serve(ctx, ln)

	var sealHash [32]byte
	for i := range sealHash {
		sealHash[i] = byte(i + 1)
	}
	nodeTarget := "0x" + strings.Repeat("00", 24) + "00000000000003ff"
	seed := "0x" + strings.Repeat("ab", 32)
	w, err := NewWork("1", "0x"+hex.EncodeToString(sealHash[:]), seed, nodeTarget, 100)
	if err != nil {
		t.Fatal(err)
	}
	srv.SetWork(w)

	nc, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	r := bufio.NewReader(nc)

	// login
	fmt.Fprint(nc, `{"id":1,"method":"login","params":{"login":"solo","pass":"x","agent":"test","algo":["rx/0"]}}`+"\n")
	line, err := r.ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var loginResp struct {
		Result loginResult `json:"result"`
	}
	if err := json.Unmarshal(line, &loginResp); err != nil {
		t.Fatalf("login resp: %v (%s)", err, line)
	}
	j := loginResp.Result.Job
	if j == nil {
		t.Fatal("login returned no job")
	}
	if j.Algo != "rx/0" {
		t.Errorf("algo = %q", j.Algo)
	}
	if j.SeedHash != strings.Repeat("ab", 32) {
		t.Errorf("seed_hash = %q", j.SeedHash)
	}
	if j.Target != "ff03000000000000" {
		t.Errorf("target = %q", j.Target)
	}
	blob, err := hex.DecodeString(j.Blob)
	if err != nil || len(blob) != 43 {
		t.Fatalf("blob decode err=%v len=%d", err, len(blob))
	}
	if !bytes.Equal(blob[0:32], sealHash[:]) {
		t.Error("blob sealHash mismatch")
	}
	if !bytes.Equal(blob[39:43], []byte{0, 0, 0, 0}) {
		t.Error("blob nonce field not zero")
	}
	extranonce := blob[35:39]

	// submit a solution. The job target is 0x3ff, and the server now verifies
	// locally that the result meets it (last 8 bytes, little-endian, <= target)
	// before relaying — so the result's last 8 bytes are 0x...0001 (LE value 1).
	submitNonce := "deadbeef"
	result := strings.Repeat("11", 24) + "0100000000000000"
	fmt.Fprintf(nc, `{"id":2,"method":"submit","params":{"id":"%s","job_id":"%s","nonce":"%s","result":"%s"}}`+"\n",
		loginResp.Result.ID, j.JobID, submitNonce, result)

	select {
	case got := <-ms.ch:
		wantNonce := "0x" + hex.EncodeToString(extranonce) + submitNonce
		if got[0] != wantNonce {
			t.Errorf("nonce = %s, want %s", got[0], wantNonce)
		}
		if want := "0x" + hex.EncodeToString(sealHash[:]); got[1] != want {
			t.Errorf("sealHash = %s, want %s", got[1], want)
		}
		if want := "0x" + result; got[2] != want {
			t.Errorf("mixDigest = %s, want %s", got[2], want)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("submitter was never called")
	}

	resp, err := r.ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(resp), "OK") {
		t.Errorf("submit response = %s", resp)
	}
}

// --- hardening test helpers ---

// serveTest starts srv on a loopback listener and returns its address plus a
// stop func.
func serveTest(t *testing.T, srv *Server) (addr string, stop func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go srv.Serve(ctx, ln)
	return ln.Addr().String(), cancel
}

// makeWork builds a Work with the given job id and last-8-bytes-of-target hex
// (16 chars, big-endian as in eth_getWork).
func makeWork(t *testing.T, jobID, targetLast8 string) *Work {
	t.Helper()
	var sealHash [32]byte
	for i := range sealHash {
		sealHash[i] = byte(i + 1)
	}
	nodeTarget := "0x" + strings.Repeat("00", 24) + targetLast8
	seed := "0x" + strings.Repeat("ab", 32)
	w, err := NewWork(jobID, "0x"+hex.EncodeToString(sealHash[:]), seed, nodeTarget, 100)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

// dialLogin dials addr, performs a login, and returns the conn, a buffered
// reader positioned after the login response, and the session id.
func dialLogin(t *testing.T, addr string) (net.Conn, *bufio.Reader, string) {
	t.Helper()
	nc, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	r := bufio.NewReader(nc)
	fmt.Fprint(nc, `{"id":1,"method":"login","params":{"login":"solo","pass":"x","rigid":"rig1","agent":"test","algo":["rx/0"]}}`+"\n")
	line, err := r.ReadBytes('\n')
	if err != nil {
		t.Fatalf("login read: %v", err)
	}
	var resp struct {
		Result loginResult `json:"result"`
	}
	if err := json.Unmarshal(line, &resp); err != nil {
		t.Fatalf("login resp: %v (%s)", err, line)
	}
	if resp.Result.Status != "OK" {
		t.Fatalf("login status = %q", resp.Result.Status)
	}
	return nc, r, resp.Result.ID
}

func submit(nc net.Conn, id, jobID, nonce, result string) {
	fmt.Fprintf(nc, `{"id":2,"method":"submit","params":{"id":"%s","job_id":"%s","nonce":"%s","result":"%s"}}`+"\n",
		id, jobID, nonce, result)
}

// isClosed reports whether the peer closed nc (a read returns an error) within
// the timeout.
func isClosed(nc net.Conn, timeout time.Duration) bool {
	_ = nc.SetReadDeadline(time.Now().Add(timeout))
	buf := make([]byte, 1)
	_, err := nc.Read(buf)
	return err != nil
}

// TestOversizedLineDisconnects: a line past the 4 KB cap closes the connection
// instead of being read unbounded.
func TestOversizedLineDisconnects(t *testing.T) {
	ms := &mockSubmitter{ch: make(chan [3]string, 1)}
	srv := NewServer("", ms)
	addr, stop := serveTest(t, srv)
	defer stop()

	nc, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	// 5 KB with no newline — well past maxLineLen (4 KB).
	if _, err := nc.Write(bytes.Repeat([]byte("a"), 5000)); err != nil {
		t.Fatal(err)
	}
	if !isClosed(nc, 2*time.Second) {
		t.Fatal("expected server to close the connection on an oversized line")
	}
}

// TestIdleDisconnect: after login, an idle connection is closed once the idle
// read deadline elapses.
func TestIdleDisconnect(t *testing.T) {
	cfg := DefaultConfig()
	cfg.LoginTimeout = 2 * time.Second
	cfg.IdleTimeout = 200 * time.Millisecond
	ms := &mockSubmitter{ch: make(chan [3]string, 1)}
	srv := NewServerWithConfig("", ms, cfg)
	addr, stop := serveTest(t, srv)
	defer stop()

	nc, r, _ := dialLogin(t, addr)
	defer nc.Close()
	// Stay idle; the next read should hit EOF when the server times us out.
	_ = nc.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := r.ReadBytes('\n'); err == nil {
		t.Fatal("expected server to close the idle connection")
	}
}

// TestPerIPCap: the per-IP connection limit refuses a connection beyond the cap.
func TestPerIPCap(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxPerIP = 2
	ms := &mockSubmitter{ch: make(chan [3]string, 1)}
	srv := NewServerWithConfig("", ms, cfg)
	addr, stop := serveTest(t, srv)
	defer stop()

	// Two logged-in connections occupy the cap (login confirms registration).
	nc1, _, _ := dialLogin(t, addr)
	defer nc1.Close()
	nc2, _, _ := dialLogin(t, addr)
	defer nc2.Close()

	// The third from the same IP must be refused (closed on accept).
	nc3, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer nc3.Close()
	if !isClosed(nc3, 2*time.Second) {
		t.Fatal("expected the over-cap connection to be refused")
	}
}

// TestDuplicateSubmitRejected: a repeated (job_id, nonce) is rejected without a
// second call to the node.
func TestDuplicateSubmitRejected(t *testing.T) {
	ms := &mockSubmitter{ch: make(chan [3]string, 4), ret: true}
	srv := NewServer("", ms)
	addr, stop := serveTest(t, srv)
	defer stop()
	srv.SetWork(makeWork(t, "job1", "00000000000003ff"))

	nc, r, id := dialLogin(t, addr)
	defer nc.Close()

	meets := strings.Repeat("11", 24) + "0100000000000000" // last 8 bytes LE = 1 <= 0x3ff
	// First submit: valid, reaches the node (accepted).
	submit(nc, id, "job1", "deadbeef", meets)
	if line, err := r.ReadBytes('\n'); err != nil || !strings.Contains(string(line), "OK") {
		t.Fatalf("first submit response = %q err=%v", line, err)
	}
	if got := ms.calls.Load(); got != 1 {
		t.Fatalf("node calls after first submit = %d, want 1", got)
	}
	// Second, identical submit: duplicate, must not reach the node again.
	submit(nc, id, "job1", "deadbeef", meets)
	line, err := r.ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(line), "duplicate") {
		t.Errorf("duplicate submit response = %s", line)
	}
	if got := ms.calls.Load(); got != 1 {
		t.Errorf("node calls after duplicate submit = %d, want 1 (node must not be re-called)", got)
	}
}

// TestBelowTargetRejected: a result that cannot meet the network target is
// rejected locally, never reaching the node (no RandomX triggered).
func TestBelowTargetRejected(t *testing.T) {
	ms := &mockSubmitter{ch: make(chan [3]string, 1), ret: true}
	srv := NewServer("", ms)
	addr, stop := serveTest(t, srv)
	defer stop()
	srv.SetWork(makeWork(t, "job1", "00000000000003ff"))

	nc, r, id := dialLogin(t, addr)
	defer nc.Close()

	// Last 8 bytes LE = 0xffffffffffffffff, far above the 0x3ff target.
	tooHigh := strings.Repeat("ff", 32)
	submit(nc, id, "job1", "deadbeef", tooHigh)
	line, err := r.ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(line), "low difficulty") {
		t.Errorf("below-target response = %s", line)
	}
	if got := ms.calls.Load(); got != 0 {
		t.Errorf("node calls for below-target submit = %d, want 0", got)
	}
}

// TestBanAfterThreshold: enough invalid submits from one IP ban it — its live
// connection is dropped and a fresh connection from that IP is refused.
func TestBanAfterThreshold(t *testing.T) {
	cfg := DefaultConfig()
	cfg.BanThreshold = 3
	ms := &mockSubmitter{ch: make(chan [3]string, 1), ret: true}
	srv := NewServerWithConfig("", ms, cfg)
	addr, stop := serveTest(t, srv)
	defer stop()
	srv.SetWork(makeWork(t, "job1", "00000000000003ff"))

	nc, r, id := dialLogin(t, addr)
	defer nc.Close()

	tooHigh := strings.Repeat("ff", 32)
	for i := 0; i < cfg.BanThreshold; i++ {
		submit(nc, id, "job1", fmt.Sprintf("0000000%d", i), tooHigh)
		if _, err := r.ReadBytes('\n'); err != nil {
			// The final strike may close us before the response is fully read.
			break
		}
	}
	if got := ms.calls.Load(); got != 0 {
		t.Errorf("node was called %d times for below-target submits, want 0", got)
	}
	// Draining until EOF confirms the ban was recorded before our conn closed
	// (markInvalid records the ban before closing the IP's connections).
	_ = nc.SetReadDeadline(time.Now().Add(2 * time.Second))
	for {
		if _, err := r.ReadBytes('\n'); err != nil {
			break
		}
	}
	// A fresh connection from the same IP must now be refused.
	nc2, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer nc2.Close()
	if !isClosed(nc2, 2*time.Second) {
		t.Fatal("expected a banned IP's new connection to be refused")
	}
}

// TestSubmitBeforeLoginIgnored: submits before login are ignored and the
// connection is closed after three such messages, never reaching the node.
func TestSubmitBeforeLoginIgnored(t *testing.T) {
	ms := &mockSubmitter{ch: make(chan [3]string, 1), ret: true}
	srv := NewServer("", ms)
	addr, stop := serveTest(t, srv)
	defer stop()
	srv.SetWork(makeWork(t, "job1", "ffffffffffffffff"))

	nc, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	for i := 0; i < 3; i++ {
		submit(nc, "", "job1", "deadbeef", strings.Repeat("11", 32))
	}
	if !isClosed(nc, 2*time.Second) {
		t.Fatal("expected connection to be closed after 3 pre-login messages")
	}
	if got := ms.calls.Load(); got != 0 {
		t.Errorf("node was called %d times for pre-login submits, want 0", got)
	}
}
