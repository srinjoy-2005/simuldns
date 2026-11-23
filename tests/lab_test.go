package tests

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"dns/internal/telemetry"
)

func TestLabLifecycle(t *testing.T) {
	dir := t.TempDir()
	resolver := build(t, dir, "dns", ".")
	controller := build(t, dir, "lab", "./cmd/lab")
	ports := freePorts(t, 3)
	first, second, replacement := ports[0], ports[1], ports[2]
	base, wait, _ := launch(t, controller, resolver)
	client := &http.Client{Timeout: 5 * time.Second}
	topology := func(exitPort int, delay int) any {
		return map[string]any{"entry": "front", "nodes": []any{
			map[string]any{"id": "front", "label": "Front", "port": first, "cache": true, "upstream": "back", "timeoutMs": 1000, "delayMs": delay, "records": map[string]any{}},
			map[string]any{"id": "back", "label": "Back", "port": exitPort, "timeoutMs": 1000, "records": map[string]any{
				"second.test":             map[string]string{"AAAA": "2001:db8::20"},
				"20.2.0.192.in-addr.arpa": map[string]string{"PTR": "second.test."},
			}},
		}}
	}
	request(t, client, base, "run", topology(second, 40), 204)
	response, err := client.Get(base)
	if err != nil {
		t.Fatal(err)
	}
	html, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || response.StatusCode != 200 || !bytes.Contains(html, []byte(`id="graph"`)) {
		t.Fatalf("embedded UI unavailable: %d %v", response.StatusCode, err)
	}
	result := query(t, client, base, "second.test", 28)
	if result.RCode == nil || *result.RCode != 0 || len(result.Answers) != 1 || result.Answers[0].Value != "2001:db8::20" {
		t.Fatalf("unexpected forwarded answer: %+v", result)
	}
	cached := query(t, client, base, "second.test", 28)
	if len(cached.Answers) != 1 {
		t.Fatal("repeat query did not return an answer")
	}
	ptr := query(t, client, base, "20.2.0.192.in-addr.arpa", 12)
	if len(ptr.Answers) != 1 || ptr.Answers[0].Value != "second.test." {
		t.Fatalf("PTR: %+v", ptr)
	}

	events := readEvents(t, client, base)
	var front, back, hop string
	var hostKey, outbound, inbound *telemetry.Key
	var cacheHit bool
	for _, event := range events {
		switch {
		case event.Kind == "host" && event.Span == result.Span:
			hostKey = event.Wire
		case event.Kind == "receive" && event.Node == "front" && event.Span == "1":
			front = event.Span
			if hostKey == nil || *hostKey != *event.Wire {
				t.Fatalf("Host wire key did not match front: %+v", event)
			}
		case event.Kind == "sent" && event.Node == "front" && event.Parent == "1":
			outbound = event.Wire
			hop = event.Span
		case event.Kind == "receive" && event.Node == "back" && event.Span == "1":
			inbound = event.Wire
			back = event.Span
		case event.Kind == "cache" && event.Status == "hit":
			cacheHit = true
		}
	}
	if front == "" || back == "" || hop == "" || outbound == nil || inbound == nil || *outbound != *inbound || !cacheHit {
		t.Fatalf("trace correlation or cache miss: front=%s back=%s hop=%s out=%+v in=%+v hit=%v", front, back, hop, outbound, inbound, cacheHit)
	}

	request(t, client, base, "remake", topology(replacement, 0), 204)
	assertFree(t, second)
	if result := query(t, client, base, "second.test", 28); len(result.Answers) != 1 {
		t.Fatalf("remade run: %+v", result)
	}
	request(t, client, base, "stop", struct{}{}, 204)
	assertFree(t, first)
	assertFree(t, replacement)

	// Startup failure must reap resolvers that were already listening.
	occupied, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: second})
	if err != nil {
		t.Fatal(err)
	}
	request(t, client, base, "run", topology(second, 0), 409)
	assertFree(t, first)
	occupied.Close()

	request(t, client, base, "run", topology(second, 30000), 204)
	pending := make(chan error, 1)
	go func() {
		resp, err := client.Post(base+"/api/query", "application/json", strings.NewReader(`{"name":"second.test","type":28,"timeoutMs":20000}`))
		if err == nil {
			_, err = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
		pending <- err
	}()
	waitForForward(t, base)
	request(t, client, base, "quit", struct{}{}, 204)
	wait()
	assertFree(t, first)
	assertFree(t, second)
	if err := <-pending; err != nil {
		t.Errorf("in-flight query teardown: %v", err)
	}

	base, wait, process := launch(t, controller, resolver)
	request(t, client, base, "run", topology(second, 0), 204)
	if err := process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	wait()
	assertFree(t, first)
	assertFree(t, second)
}

func build(t *testing.T, dir, name, target string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	cmd := exec.Command("go", "build", "-o", path, target)
	cmd.Dir = ".."
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build %s: %v\n%s", target, err, out)
	}
	return path
}

func freePorts(t *testing.T, n int) []int {
	t.Helper()
	var ports []int
	for range n {
		conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		ports = append(ports, conn.LocalAddr().(*net.UDPAddr).Port)
	}
	return ports
}

func assertFree(t *testing.T, port int) {
	t.Helper()
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port})
	if err != nil {
		t.Fatalf("UDP port %d still held: %v", port, err)
	}
	conn.Close()
}

func launch(t *testing.T, controller, resolver string) (string, func(), *os.Process) {
	t.Helper()
	cmd := exec.Command(controller, "-listen", "127.0.0.1:0", "-resolver", resolver)
	cmd.Env = append(os.Environ(), "TMPDIR="+t.TempDir())
	out, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	ready := make(chan string, 1)
	done := make(chan struct{})
	var exitErr error
	go func() {
		defer close(done)
		scanner := bufio.NewScanner(out)
		for scanner.Scan() {
			if _, base, found := strings.Cut(scanner.Text(), "DNS Lab at "); found {
				ready <- base
			}
		}
		exitErr = cmd.Wait()
	}()
	stop := func() {
		select {
		case <-done:
			if exitErr != nil {
				t.Errorf("controller exit: %v", exitErr)
			}
		case <-time.After(6 * time.Second):
			t.Error("controller failed to exit")
		}
	}
	t.Cleanup(func() {
		select {
		case <-done:
		default:
			_ = cmd.Process.Signal(os.Interrupt)
			select {
			case <-done:
			case <-time.After(6 * time.Second):
				_ = cmd.Process.Kill()
				<-done
			}
		}
	})
	select {
	case base := <-ready:
		return base, stop, cmd.Process
	case <-done:
		t.Fatalf("controller exited: %v", exitErr)
	case <-time.After(5 * time.Second):
		t.Fatal("controller did not start")
	}
	return "", nil, nil
}

func request(t *testing.T, client *http.Client, base, action string, body any, want int) []byte {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Post(base+"/api/"+action, "application/json", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil || response.StatusCode != want {
		t.Fatalf("%s: got %d, want %d: %s (%v)", action, response.StatusCode, want, data, err)
	}
	return data
}
func query(t *testing.T, client *http.Client, base, name string, kind uint16) telemetry.Event {
	t.Helper()
	data := request(t, client, base, "query", map[string]any{"name": name, "type": kind, "timeoutMs": 1500}, 200)
	var result telemetry.Event
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func readEvents(t *testing.T, client *http.Client, base string) []telemetry.Event {
	t.Helper()
	resp, err := client.Get(base + "/api/events")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	if !scanner.Scan() {
		t.Fatalf("trace stream: %v", scanner.Err())
	}
	return unpack(t, scanner.Text())
}

func unpack(t *testing.T, line string) []telemetry.Event {
	t.Helper()
	var batch struct{ Events []telemetry.Event }
	if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &batch); err != nil {
		t.Fatalf("trace event: %v", err)
	}
	return batch.Events
}

func waitForForward(t *testing.T, base string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", base+"/api/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	scan := bufio.NewScanner(resp.Body)
	for scan.Scan() {
		if !strings.HasPrefix(scan.Text(), "data: ") {
			continue
		}
		for _, event := range unpack(t, scan.Text()) {
			if event.Kind == "forward" {
				return
			}
		}
	}
	t.Error(fmt.Errorf("did not see forwarding before quit (HTTP %s): %v", strconv.Itoa(resp.StatusCode), scan.Err()))
}
