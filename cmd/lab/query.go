package main

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"dns/internal/telemetry"
	"dns/internal/wire"
)

func (c *controller) query(w http.ResponseWriter, req *http.Request) {
	var input struct {
		Name      string `json:"name"`
		Type      uint16 `json:"type"`
		TimeoutMS int    `json:"timeoutMs"`
	}
	if err := decode(w, req, &input); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if input.TimeoutMS < 1 || input.TimeoutMS > 600000 || (input.Type != 1 && input.Type != 12 && input.Type != 28) {
		http.Error(w, "Choose A, AAAA or PTR and a deadline between 1 ms and 10 minutes", 400)
		return
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		http.Error(w, "Enter a DNS name", 400)
		return
	}
	if !strings.HasSuffix(name, ".") {
		name += "."
	}
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	span := hex.EncodeToString(random)
	question := wire.Question{Name: name, Type: input.Type, Class: 1}
	id := binary.BigEndian.Uint16(random)
	request := wire.Message{Header: wire.Header{ID: id, Flags: wire.Flags{RD: true}}, Questions: []wire.Question{question}}
	packet, err := request.Marshal()
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}

	c.mu.Lock()
	r := c.active
	c.mu.Unlock()
	if r == nil {
		http.Error(w, "Run the lab first", 409)
		return
	}
	ctx, cancel := context.WithTimeout(req.Context(), time.Duration(input.TimeoutMS)*time.Millisecond)
	defer cancel()
	stop := context.AfterFunc(r.ctx, cancel)
	defer stop()
	conn, err := (&net.Dialer{}).DialContext(ctx, "udp4", r.entry)
	if err != nil {
		http.Error(w, err.Error(), 502)
		return
	}
	defer conn.Close()
	interrupt := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer interrupt()
	deadline, _ := ctx.Deadline()
	_ = conn.SetDeadline(deadline)
	key := telemetry.Key{Source: conn.LocalAddr().String(), Target: conn.RemoteAddr().String(), ID: id}
	r.events.add(telemetry.Event{Kind: "host", Node: "host", Span: span, At: time.Now(), Wire: &key, Name: name, Type: input.Type})
	result := telemetry.Event{Kind: "finish", Node: "host", Span: span, Status: "reply"}
	if _, err = conn.Write(packet); err == nil {
		buf := make([]byte, 65535)
		for {
			var n int
			n, err = conn.Read(buf)
			if err != nil {
				break
			}
			response, parseErr := wire.ParseMessage(buf[:n])
			if parseErr != nil || response.Header.ID != id || !response.Header.Flags.QR ||
				response.Header.Flags.Opcode != 0 || len(response.Questions) != 1 {
				continue
			}
			got := response.Questions[0]
			if !strings.EqualFold(got.Name, name) || got.Type != question.Type || got.Class != question.Class {
				continue
			}
			code := response.Header.Flags.RCode
			result.RCode = &code
			result.Answers = summarizeAnswers(response, buf[:n])
			break
		}
	}
	if err != nil {
		result.Status, result.Detail = "error", err.Error()
		var netErr net.Error
		if errors.Is(ctx.Err(), context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) {
			result.Status, result.Detail = "timeout", "Host deadline expired"
		} else if ctx.Err() != nil {
			result.Status, result.Detail = "canceled", "Query canceled"
		}
	}
	result.At = time.Now()
	r.events.add(result)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}

func summarizeAnswers(message *wire.Message, packet []byte) []telemetry.Answer {
	answers := make([]telemetry.Answer, 0, len(message.Answers))
	for _, rr := range message.Answers {
		value := hex.EncodeToString(rr.Data)
		switch rr.Type {
		case 1, 28:
			if len(rr.Data) == 4 || len(rr.Data) == 16 {
				value = net.IP(rr.Data).String()
			}
		case 5, 12:
			if target, err := rr.DomainDataName(packet); err == nil {
				value = target
			}
		}
		answers = append(answers, telemetry.Answer{Name: rr.Name, Type: rr.Type, TTL: rr.TTL, Value: value})
	}
	return answers
}
