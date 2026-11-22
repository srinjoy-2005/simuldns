package main

import (
	"context"
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"mime"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

//go:embed web/*
var assets embed.FS

type controller struct {
	ctx    context.Context
	quit   context.CancelFunc
	binary string
	host   string
	mu     sync.Mutex
	active *run
	last   *run
}

func main() {
	bind := flag.String("listen", "127.0.0.1:8080", "local HTTP address")
	binary := flag.String("resolver", "./bin/dns", "path to the resolver executable")
	flag.Parse()
	host, _, err := net.SplitHostPort(*bind)
	if err != nil || host != "127.0.0.1" {
		log.Fatal("use a 127.0.0.1 listener")
	}
	path, err := filepath.Abs(*binary)
	if err != nil {
		log.Fatal(err)
	}
	if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() {
		log.Fatalf("resolver binary not found at %s; build it first", path)
	}
	signalCtx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	ctx, quit := context.WithCancel(signalCtx)
	defer quit()
	ln, err := net.Listen("tcp4", *bind)
	if err != nil {
		log.Fatal(err)
	}
	c := &controller{ctx: ctx, quit: quit, binary: path, host: ln.Addr().String()}
	server := &http.Server{Handler: c.routes(), ReadHeaderTimeout: 5 * time.Second}
	done := make(chan error, 1)
	go func() { done <- server.Serve(ln) }()
	log.Printf("DNS Lab at http://%s", c.host)
	select {
	case <-ctx.Done():
	case err := <-done:
		log.Printf("HTTP server: %v", err)
	}
	quit()
	c.mu.Lock()
	c.stopLocked()
	c.mu.Unlock()
	shutdown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdown); err != nil {
		_ = server.Close()
	}
}

func (c *controller) routes() http.Handler {
	files, _ := fs.Sub(assets, "web")
	mux := http.NewServeMux()
	mux.Handle("GET /", http.FileServer(http.FS(files)))
	mux.HandleFunc("POST /api/run", c.start)
	mux.HandleFunc("POST /api/remake", c.start)
	mux.HandleFunc("POST /api/stop", func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.stopLocked()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /api/quit", func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		c.stopLocked()
		c.quit()
		c.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /api/query", c.query)
	mux.HandleFunc("GET /api/events", c.stream)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != c.host {
			http.Error(w, "Use the lab's loopback URL", http.StatusForbidden)
			return
		}
		if r.Method == http.MethodPost {
			origin := r.Header.Get("Origin")
			contentType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if (origin != "" && origin != "http://"+c.host) || contentType != "application/json" {
				http.Error(w, "Expected same-origin JSON", http.StatusForbidden)
				return
			}
		}
		mux.ServeHTTP(w, r)
	})
}

func (c *controller) start(w http.ResponseWriter, req *http.Request) {
	var draft topology
	if err := decode(w, req, &draft); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	nodes, err := draft.validate()
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ctx.Err() != nil {
		http.Error(w, "Controller is shutting down", 503)
		return
	}
	if c.active != nil && req.URL.Path == "/api/run" {
		http.Error(w, "Stop or remake the active run first", 409)
		return
	}
	c.stopLocked()
	next, err := startRun(c.ctx, c.binary, draft, nodes)
	if err != nil {
		http.Error(w, err.Error(), 409)
		return
	}
	c.active, c.last = next, next
	w.WriteHeader(http.StatusNoContent)
}

func (c *controller) stopLocked() {
	if c.active != nil {
		c.active.stop()
		c.active = nil
	}
}

func decode(w http.ResponseWriter, r *http.Request, value any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return fmt.Errorf("expected one JSON value")
	}
	return nil
}

func (c *controller) stream(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unavailable", 500)
		return
	}
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	var seen *run
	var revision uint64
	var previousRunning bool
	initial := true
	for {
		c.mu.Lock()
		last, active := c.last, c.active != nil
		c.mu.Unlock()
		reset := initial || last != seen
		if reset {
			seen, revision = last, 0
		}
		batch := struct {
			Running bool `json:"running"`
			Reset   bool `json:"reset"`
			Events  any  `json:"events"`
		}{Running: active, Reset: reset}
		var changed bool
		if seen != nil {
			events, next, lost := seen.events.since(revision)
			changed = revision != next
			revision = next
			batch.Events = events
			batch.Reset = reset || lost
		}
		if changed || reset || previousRunning != active {
			data, err := json.Marshal(batch)
			if err != nil {
				return
			}
			if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
				return
			}
			flusher.Flush()
			previousRunning = active
			initial = false
		}
		select {
		case <-req.Context().Done():
			return
		case <-c.ctx.Done():
			return
		case <-tick.C:
		}
	}
}
