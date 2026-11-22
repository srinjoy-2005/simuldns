package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"dns/internal/telemetry"
)

type run struct {
	ctx      context.Context
	cancel   context.CancelFunc
	dir      string
	entry    string
	children []child
	events   eventLog
}

type child struct {
	cmd  *exec.Cmd
	done chan struct{}
}

type eventLog struct {
	mu       sync.Mutex
	events   []telemetry.Event
	revision uint64
}

func (l *eventLog) add(event telemetry.Event) {
	l.mu.Lock()
	defer l.mu.Unlock()
	const maxEvents = 4096
	if len(l.events) == maxEvents {
		copy(l.events, l.events[maxEvents/2:])
		l.events = l.events[:maxEvents/2]
	}
	l.events = append(l.events, event)
	l.revision++
}

func (l *eventLog) since(revision uint64) ([]telemetry.Event, uint64, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	first := l.revision - uint64(len(l.events))
	reset := revision < first
	if reset {
		revision = first
	}
	return append([]telemetry.Event(nil), l.events[int(revision-first):]...), l.revision, reset
}

func startRun(ctx context.Context, binary string, t topology, nodes map[string]node) (*run, error) {
	dir, err := os.MkdirTemp("", "dns-lab-")
	if err != nil {
		return nil, err
	}
	runCtx, cancel := context.WithCancel(ctx)
	r := &run{ctx: runCtx, cancel: cancel, dir: dir, entry: nodes[t.Entry].endpoint()}
	for i, n := range t.Nodes {
		if n.Address != "" {
			continue
		}
		if err := r.startChild(binary, n, nodes[n.Upstream], i); err != nil {
			r.stop()
			return nil, fmt.Errorf("%s: %w", n.Label, err)
		}
	}
	return r, nil
}

func (r *run) startChild(binary string, n, upstream node, index int) error {
	path := filepath.Join(r.dir, strconv.Itoa(index)+".json")
	if err := os.WriteFile(path, n.Records, 0600); err != nil {
		return err
	}
	target := "none"
	if upstream.ID != "" {
		target = upstream.endpoint()
	}
	args := []string{"-port", strconv.Itoa(n.Port), "-records", path, "-upstream", target,
		"-delay", millis(n.DelayMS), "-timeout", millis(n.TimeoutMS), "-trace-node", n.ID}
	if n.Cache {
		args = append(args, "-main")
	}
	cmd := exec.CommandContext(r.ctx, binary, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr := &outputTail{}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan struct{})
	ready := make(chan struct{})
	r.children = append(r.children, child{cmd: cmd, done: done})
	go func() {
		defer close(done)
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 4096), 256*1024)
		for scanner.Scan() {
			var event telemetry.Event
			if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
				continue
			}
			event.Node = n.ID
			r.events.add(event)
			if event.Kind == "ready" {
				close(ready)
			}
		}
		stdout.Close()
		err := cmd.Wait()
		if r.ctx.Err() == nil {
			r.events.add(telemetry.Event{Kind: "exit", Node: n.ID, At: time.Now(), Detail: fmt.Sprint(err)})
		}
	}()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case <-ready:
		return nil
	case <-done:
		return fmt.Errorf("resolver exited during startup: %s", stderr.String())
	case <-timer.C:
		return fmt.Errorf("resolver did not report readiness within 5s")
	case <-r.ctx.Done():
		return r.ctx.Err()
	}
}

func (r *run) stop() {
	// Cancel queries and signal every child before waiting on any one child.
	r.cancel()
	for _, child := range r.children {
		_ = child.cmd.Process.Kill()
	}
	for _, child := range r.children {
		<-child.done
	}
	_ = os.RemoveAll(r.dir)
}

type outputTail struct{ text string }

func (o *outputTail) Write(p []byte) (int, error) {
	o.text += string(p)
	if len(o.text) > 4096 {
		o.text = o.text[len(o.text)-4096:]
	}
	return len(p), nil
}

func (o *outputTail) String() string { return o.text }
