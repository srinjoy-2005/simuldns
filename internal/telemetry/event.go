package telemetry

import "time"

// Key identifies one UDP exchange within a run, across its sending and
// receiving processes. DNS transaction IDs are scoped to an exchange.
type Key struct {
	Source string `json:"source"`
	Target string `json:"target"`
	ID     uint16 `json:"id"`
}

// Event opens, describes, or closes one span.
type Event struct {
	Kind    string    `json:"kind"`
	Node    string    `json:"node"`
	Span    string    `json:"span,omitempty"`
	Parent  string    `json:"parent,omitempty"`
	At      time.Time `json:"at"`
	Wire    *Key      `json:"wire,omitempty"`
	Name    string    `json:"name,omitempty"`
	Type    uint16    `json:"type,omitempty"`
	Status  string    `json:"status,omitempty"`
	Detail  string    `json:"detail,omitempty"`
	Answers []Answer  `json:"answers,omitempty"`
	RCode   *uint8    `json:"rcode,omitempty"`
}

type Answer struct {
	Name  string `json:"name"`
	Type  uint16 `json:"type"`
	TTL   uint32 `json:"ttl"`
	Value string `json:"value"`
}
