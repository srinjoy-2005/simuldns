package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"time"
)

type topology struct {
	Entry string `json:"entry"`
	Nodes []node `json:"nodes"`
}

type node struct {
	ID        string          `json:"id"`
	Label     string          `json:"label"`
	Port      int             `json:"port"`
	Upstream  string          `json:"upstream"`
	Address   string          `json:"address"`
	Cache     bool            `json:"cache"`
	DelayMS   int             `json:"delayMs"`
	TimeoutMS int             `json:"timeoutMs"`
	Records   json.RawMessage `json:"records"`
}

func (n node) endpoint() string {
	if n.Address != "" {
		return n.Address
	}
	return net.JoinHostPort("127.0.0.1", strconv.Itoa(n.Port))
}

func (t topology) validate() (map[string]node, error) {
	if len(t.Nodes) < 1 || len(t.Nodes) > 20 {
		return nil, fmt.Errorf("add between 1 and 20 nodes")
	}
	nodes := make(map[string]node)
	ports := make(map[int]bool)
	for _, n := range t.Nodes {
		if n.ID == "" || n.ID == "host" || len(n.ID) > 64 || strings.Contains(n.ID, "|") || nodes[n.ID].ID != "" {
			return nil, fmt.Errorf("invalid or duplicate node ID %q", n.ID)
		}
		nodes[n.ID] = n
		if n.Address != "" {
			endpoint, err := netip.ParseAddrPort(n.Address)
			if err != nil || endpoint.Port() == 0 || endpoint.Addr().IsUnspecified() {
				return nil, fmt.Errorf("%s: external endpoint must be an IP and port", n.Label)
			}
			if n.Upstream != "" {
				return nil, fmt.Errorf("%s: external endpoints cannot have an outgoing link", n.Label)
			}
			continue
		}
		if n.Port < 1 || n.Port > 65535 || ports[n.Port] {
			return nil, fmt.Errorf("%s: invalid or duplicate UDP port %d", n.Label, n.Port)
		}
		ports[n.Port] = true
		if n.DelayMS < 0 || n.DelayMS > 120000 || n.TimeoutMS < 1 || n.TimeoutMS > 120000 {
			return nil, fmt.Errorf("%s: delay or timeout outside supported range (up to two minutes)", n.Label)
		}
		var records map[string]struct{ A, AAAA, PTR string }
		if err := json.Unmarshal(n.Records, &records); err != nil || records == nil {
			return nil, fmt.Errorf("%s: records must be a JSON object with A, AAAA or PTR fields", n.Label)
		}
	}
	if entry, ok := nodes[t.Entry]; !ok || entry.Address != "" {
		return nil, fmt.Errorf("connect Host to a local resolver")
	}
	for _, n := range t.Nodes {
		if n.Address != "" {
			endpoint, _ := netip.ParseAddrPort(n.Address)
			if endpoint.Addr().Unmap().IsLoopback() && ports[int(endpoint.Port())] {
				return nil, fmt.Errorf("%s: link to the local resolver node instead of its address", n.Label)
			}
		}
		if n.Upstream == "" {
			continue
		}
		upstream, ok := nodes[n.Upstream]
		if !ok {
			return nil, fmt.Errorf("%s: upstream node is missing", n.Label)
		}
		if n.Cache && upstream.Address != "" {
			return nil, fmt.Errorf("%s: connect the caching resolver to a local resolver", n.Label)
		}
	}
	for _, n := range t.Nodes {
		visited := make(map[string]bool)
		for next := n.ID; next != ""; next = nodes[next].Upstream {
			if visited[next] {
				return nil, fmt.Errorf("%s: forwarding cycle", n.Label)
			}
			visited[next] = true
		}
	}
	return nodes, nil
}

func millis(value int) string { return (time.Duration(value) * time.Millisecond).String() }
