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
	"testing"
	"time"
)

// mockSubmitter captures the eth_submitWork arguments the server produces.
type mockSubmitter struct {
	ch  chan [3]string
	ret bool
}

func (m *mockSubmitter) SubmitWork(ctx context.Context, nonce, sealHash, mixDigest string) (bool, error) {
	m.ch <- [3]string{nonce, sealHash, mixDigest}
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

	// submit a solution
	submitNonce := "deadbeef"
	result := strings.Repeat("11", 32)
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
