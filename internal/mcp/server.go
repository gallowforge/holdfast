// Package mcp serves Holdfast to AI agents over the Model Context Protocol,
// on standard input and output.
//
// The protocol changed shape in its 2026-07-28 revision: there is no longer a
// handshake, and every request says which revision it speaks. Clients from
// both sides of that change are in use, so this server answers both: a
// request that carries the per-request version is served the new way, and a
// client that opens with "initialize" is served the old way.
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"sync"

	"github.com/gallowforge/holdfast/internal/engine"
)

// Revisions of the protocol this server speaks.
const (
	Modern       = "2026-07-28"
	LatestLegacy = "2025-11-25"
)

var legacy = []string{LatestLegacy, "2025-06-18", "2025-03-26", "2024-11-05"}

// Keys of the per-request metadata in the modern revision.
const (
	metaVersion = "io.modelcontextprotocol/protocolVersion"
	metaClient  = "io.modelcontextprotocol/clientInfo"
	metaServer  = "io.modelcontextprotocol/serverInfo"
)

// JSON-RPC and MCP error codes.
const (
	codeParse       = -32700
	codeInvalid     = -32600
	codeNoMethod    = -32601
	codeBadParams   = -32602
	codeInternal    = -32603
	codeBadRevision = -32022
)

// maxLine caps one incoming message.
const maxLine = 4 << 20

// Server answers one client.
type Server struct {
	Engine  *engine.Engine
	Version string
	// Agent names whoever is on the other end, for the "by" of what it
	// stores. When empty, the name the client gives for itself is used.
	Agent string
	// Log receives one line per problem; nil discards them.
	Log io.Writer

	out      sync.Mutex
	w        io.Writer
	mu       sync.Mutex
	client   string
	inflight map[string]context.CancelFunc
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type clientInfo struct {
	Name string `json:"name"`
}

// Serve reads requests from in, one JSON message per line, and writes
// answers to out, until in ends or ctx is cancelled.
func (s *Server) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	s.w = out
	s.inflight = map[string]context.CancelFunc{}
	ctx, stop := context.WithCancel(ctx)
	defer stop()

	var wg sync.WaitGroup
	defer wg.Wait()

	// Lines are read on the side, so that a cancelled context ends the server
	// even while it waits for a line that never comes.
	type read struct {
		line []byte
		err  error
	}
	lines := make(chan read)
	go func() {
		r := bufio.NewReaderSize(in, 64<<10)
		for {
			line, err := readLine(r)
			select {
			case lines <- read{line, err}:
			case <-ctx.Done():
				return
			}
			if err != nil && err != errTooLong {
				return
			}
		}
	}()
	for {
		var line []byte
		var err error
		select {
		case got := <-lines:
			line, err = got.line, got.err
		case <-ctx.Done():
			return ctx.Err()
		}
		if err == errTooLong {
			s.reply(nil, nil, &rpcError{Code: codeInvalid, Message: "the message is too long"})
			continue
		}
		if len(line) > 0 {
			s.accept(ctx, &wg, line)
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
}

var errTooLong = errors.New("line too long")

// readLine returns the next line without its newline. A line longer than
// maxLine is skipped whole.
func readLine(r *bufio.Reader) ([]byte, error) {
	var line []byte
	over := false
	for {
		part, more, err := r.ReadLine()
		if !over {
			line = append(line, part...)
			if len(line) > maxLine {
				over, line = true, nil
			}
		}
		if err != nil {
			if over {
				return nil, errTooLong
			}
			return line, err
		}
		if !more {
			if over {
				return nil, errTooLong
			}
			return line, nil
		}
	}
}

func (s *Server) accept(ctx context.Context, wg *sync.WaitGroup, line []byte) {
	trimmed := trim(line)
	if len(trimmed) == 0 {
		return
	}
	if trimmed[0] == '[' {
		s.reply(nil, nil, &rpcError{Code: codeInvalid, Message: "batches are not supported; send one message per line"})
		return
	}
	var req request
	if err := json.Unmarshal(trimmed, &req); err != nil {
		if json.Valid(trimmed) {
			// Well-formed JSON that is not shaped like a request: a bare
			// value, or a field of the wrong type.
			s.reply(nil, nil, &rpcError{Code: codeInvalid, Message: "not a JSON-RPC 2.0 request"})
			return
		}
		s.reply(nil, nil, &rpcError{Code: codeParse, Message: "the message is not valid JSON"})
		return
	}
	note := len(req.ID) == 0 || string(req.ID) == "null"
	if req.JSONRPC != "2.0" || req.Method == "" {
		if !note {
			s.reply(req.ID, nil, &rpcError{Code: codeInvalid, Message: "not a JSON-RPC 2.0 request"})
		}
		return
	}
	if note {
		s.notified(req)
		return
	}

	ctx, cancel := context.WithCancel(ctx)
	key := string(req.ID)
	s.mu.Lock()
	s.inflight[key] = cancel
	s.mu.Unlock()
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer func() {
			s.mu.Lock()
			delete(s.inflight, key)
			s.mu.Unlock()
			cancel()
		}()
		defer func() {
			if p := recover(); p != nil {
				s.logf("panic in %s: %v", req.Method, p)
				s.reply(req.ID, nil, &rpcError{Code: codeInternal, Message: "internal error"})
			}
		}()
		result, rerr := s.handle(ctx, req)
		if ctx.Err() != nil && rerr == nil && req.Method == "tools/call" {
			return // cancelled by the client, which no longer wants the answer
		}
		s.reply(req.ID, result, rerr)
	}()
}

func trim(b []byte) []byte {
	for len(b) > 0 && (b[0] == ' ' || b[0] == '\t' || b[0] == '\r' || b[0] == 0xEF || b[0] == 0xBB || b[0] == 0xBF) {
		b = b[1:]
	}
	for len(b) > 0 && (b[len(b)-1] == ' ' || b[len(b)-1] == '\t' || b[len(b)-1] == '\r') {
		b = b[:len(b)-1]
	}
	return b
}

func (s *Server) notified(req request) {
	if req.Method != "notifications/cancelled" {
		return
	}
	var p struct {
		RequestID json.RawMessage `json:"requestId"`
	}
	if json.Unmarshal(req.Params, &p) != nil || len(p.RequestID) == 0 {
		return
	}
	s.mu.Lock()
	cancel := s.inflight[string(p.RequestID)]
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (s *Server) reply(id json.RawMessage, result any, rerr *rpcError) {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	msg := response{JSONRPC: "2.0", ID: id}
	if rerr != nil {
		msg.Error = rerr
	} else {
		msg.Result = result
	}
	data, err := json.Marshal(msg)
	if err != nil {
		s.logf("cannot encode a reply: %v", err)
		data, _ = json.Marshal(response{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: codeInternal, Message: "internal error"}})
	}
	s.out.Lock()
	defer s.out.Unlock()
	s.w.Write(append(data, '\n'))
}

func (s *Server) logf(format string, a ...any) {
	if s.Log != nil {
		fmt.Fprintf(s.Log, "holdfast mcp: "+format+"\n", a...)
	}
}

func (s *Server) info() map[string]any {
	return map[string]any{
		"name": "holdfast", "title": "Holdfast", "version": s.Version,
		"description": "Memory for coding agents in which every fact carries its own proof.",
		"websiteUrl":  "https://github.com/gallowforge/holdfast",
	}
}

// who names the client for the "by" of the facts it stores.
func (s *Server) who(perRequest string) string {
	if s.Agent != "" {
		return s.Agent
	}
	if perRequest != "" {
		return perRequest
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.client
}

func (s *Server) handle(ctx context.Context, req request) (any, *rpcError) {
	var env struct {
		Meta map[string]json.RawMessage `json:"_meta"`
	}
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &env); err != nil {
			return nil, &rpcError{Code: codeBadParams, Message: "params must be an object"}
		}
	}

	// The modern revision: the request says which revision it speaks.
	modern := false
	caller := ""
	if raw, ok := env.Meta[metaVersion]; ok {
		var v string
		if json.Unmarshal(raw, &v) != nil || v != Modern {
			return nil, &rpcError{Code: codeBadRevision, Message: "unsupported protocol version",
				Data: map[string]any{"supported": append([]string{Modern}, legacy...), "requested": v}}
		}
		modern = true
		var ci clientInfo
		if json.Unmarshal(env.Meta[metaClient], &ci) == nil {
			caller = ci.Name
		}
	}
	// done marks a result as complete, which the modern revision requires of
	// every result and the older ones do not know about.
	done := func(m map[string]any) map[string]any {
		if modern {
			m["resultType"] = "complete"
		}
		return m
	}

	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string     `json:"protocolVersion"`
			ClientInfo      clientInfo `json:"clientInfo"`
		}
		json.Unmarshal(req.Params, &p)
		s.mu.Lock()
		s.client = p.ClientInfo.Name
		s.mu.Unlock()
		version := LatestLegacy
		if slices.Contains(legacy, p.ProtocolVersion) {
			version = p.ProtocolVersion
		}
		return map[string]any{
			"protocolVersion": version,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      s.info(),
			"instructions":    Instructions,
		}, nil
	case "server/discover":
		return map[string]any{
			"resultType":        "complete",
			"supportedVersions": append([]string{Modern}, legacy...),
			"capabilities":      map[string]any{"tools": map[string]any{}},
			"instructions":      Instructions,
			"ttlMs":             3600_000,
			"cacheScope":        "public",
			"_meta":             map[string]any{metaServer: s.info()},
		}, nil
	case "ping":
		return done(map[string]any{}), nil
	case "tools/list":
		m := map[string]any{"tools": tools}
		if modern {
			m["ttlMs"], m["cacheScope"] = 3600_000, "public"
		}
		return done(m), nil
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil || p.Name == "" {
			return nil, &rpcError{Code: codeBadParams, Message: "tools/call needs a tool name"}
		}
		text, data, failed, err := s.call(ctx, p.Name, p.Arguments, s.who(caller))
		switch {
		case errors.Is(err, errUnknownTool):
			return nil, &rpcError{Code: codeBadParams, Message: "unknown tool: " + p.Name}
		case err != nil:
			// Something went wrong inside Holdfast (a disk error, say). The
			// agent is told so it does not mistake silence for "no facts".
			// A call its client cancelled is not a problem worth a line.
			if ctx.Err() == nil || !errors.Is(err, ctx.Err()) {
				s.logf("%s: %v", p.Name, err)
			}
			text, data, failed = "Holdfast could not do that: "+err.Error(), nil, true
		}
		m := map[string]any{"content": []map[string]any{{"type": "text", "text": text}}}
		if failed {
			m["isError"] = true
		}
		if data != nil {
			m["structuredContent"] = data
		}
		return done(m), nil
	}
	return nil, &rpcError{Code: codeNoMethod, Message: "method not found: " + req.Method}
}
