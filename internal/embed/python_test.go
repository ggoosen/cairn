package embed

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeWorker is a python3 stand-in implementing the line protocol: one JSON
// array of strings in, one JSON array of float arrays out. Each vector
// encodes a stable per-text fingerprint (length of the text in the first
// component) so concurrency tests can prove a caller got ITS OWN vectors
// back, not another caller's.
const fakeWorker = `
import json, sys
for line in sys.stdin:
    texts = json.loads(line)
    sys.stdout.write(json.dumps([[float(len(t)), 1.0] for t in texts]) + "\n")
    sys.stdout.flush()
`

// sleepWorker answers the handshake, then wedges forever.
const sleepWorker = `
import json, sys, time
line = sys.stdin.readline()
texts = json.loads(line)
sys.stdout.write(json.dumps([[1.0] for t in texts]) + "\n")
sys.stdout.flush()
time.sleep(3600)
`

func startFake(t *testing.T, worker string) *Python {
	t.Helper()
	p, err := newPythonWorker("python3", worker)
	if err != nil {
		t.Skipf("python3 unavailable in this environment: %v", err)
	}
	t.Cleanup(func() { p.Close() })
	return p
}

func TestPythonRoundTrip(t *testing.T) {
	p := startFake(t, fakeWorker)
	vecs, err := p.Embed([]string{"a", "abc"})
	if err != nil {
		t.Fatal(err)
	}
	if len(vecs) != 2 || vecs[0][0] != 1 || vecs[1][0] != 3 {
		t.Fatalf("wrong vectors: %v", vecs)
	}
}

// The worker multiplexes one stdin/stdout pair: without serialization,
// concurrent callers interleave request lines and read each other's
// responses. Every caller must get the vector for ITS text.
func TestEmbedConcurrent(t *testing.T) {
	p := startFake(t, fakeWorker)
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			text := strings.Repeat("x", n+1) // len encodes the caller
			for j := 0; j < 25; j++ {
				vecs, err := p.Embed([]string{text})
				if err != nil {
					errs <- err
					return
				}
				if got := int(vecs[0][0]); got != n+1 {
					errs <- fmt.Errorf("caller %d received caller %d's vector", n+1, got)
					return
				}
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}

func TestEmbedTimeoutKillsWorker(t *testing.T) {
	p := startFake(t, sleepWorker)
	start := time.Now()
	_, err := p.embed([]string{"wedge"}, 200*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("want timeout error, got %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("timeout did not fire promptly")
	}
	// The handle is poisoned: later calls fail fast instead of wedging.
	if _, err := p.Embed([]string{"after"}); err == nil {
		t.Fatal("embed succeeded on a killed worker")
	}
}

func TestCloseReapsProcess(t *testing.T) {
	p := startFake(t, fakeWorker)
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if p.cmd.ProcessState == nil {
		t.Fatal("worker not reaped: Close must Wait() or every swap leaks a zombie")
	}
	// Close is idempotent and Embed on a closed worker fails typed.
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Embed([]string{"x"}); err == nil {
		t.Fatal("embed succeeded after Close")
	}
}
