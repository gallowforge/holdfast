package mcp

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/gallowforge/holdfast/internal/fact"
)

// The 2026-07-28 revision has no handshake: every request says which revision
// it speaks, and who is asking, in its own metadata.

// speaking is the metadata of a request in the 2026-07-28 revision.
func speaking(client string) map[string]any {
	meta := map[string]any{metaVersion: Modern}
	if client != "" {
		meta[metaClient] = map[string]any{"name": client, "version": "3.0"}
	}
	return meta
}

// every revision the server speaks, newest first.
var spoken = []any{"2026-07-28", "2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05"}

func TestTheKeysOfTheRequestMetadata(t *testing.T) {
	// These are the protocol's own names; a typo here and no client is understood.
	if metaVersion != "io.modelcontextprotocol/protocolVersion" || metaClient != "io.modelcontextprotocol/clientInfo" ||
		metaServer != "io.modelcontextprotocol/serverInfo" {
		t.Errorf("the metadata keys are %q, %q and %q", metaVersion, metaClient, metaServer)
	}
	if Modern != "2026-07-28" || LatestLegacy != "2025-11-25" {
		t.Errorf("the revisions are %q and %q", Modern, LatestLegacy)
	}
}

func TestDiscoverSaysWhatTheServerSpeaksWithoutAnyHandshake(t *testing.T) {
	for _, tc := range []struct {
		name   string
		params any
	}{
		{"with the revision in the request", map[string]any{"_meta": speaking("cursor")}},
		{"with empty parameters", map[string]any{}},
		{"with no parameters", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := open(t)
			got := s.result("server/discover", tc.params)
			if !reflect.DeepEqual(got["supportedVersions"], spoken) {
				t.Errorf("supportedVersions = %v, want %v", got["supportedVersions"], spoken)
			}
			caps, _ := got["capabilities"].(map[string]any)
			if _, ok := caps["tools"].(map[string]any); !ok || len(caps) != 1 {
				t.Errorf("capabilities = %v, want tools and nothing else", got["capabilities"])
			}
			if got["instructions"] != Instructions {
				t.Errorf("instructions = %v, want the manual", got["instructions"])
			}
			if got["ttlMs"] != float64(3600_000) || got["cacheScope"] != "public" {
				t.Errorf("ttlMs %v and cacheScope %v, want an hour and public: the answer is the same for everyone", got["ttlMs"], got["cacheScope"])
			}
			if got["resultType"] != "complete" {
				t.Errorf("resultType = %v, want complete", got["resultType"])
			}
			meta, _ := got["_meta"].(map[string]any)
			info, _ := meta[metaServer].(map[string]any)
			if info["name"] != "holdfast" || info["title"] != "Holdfast" || info["version"] != "1.2.3-test" {
				t.Errorf("the server describes itself as %v, want holdfast, Holdfast, 1.2.3-test", info)
			}
			for _, member := range []string{"description", "websiteUrl"} {
				if text, _ := info[member].(string); text == "" {
					t.Errorf("the server's description of itself has no %s", member)
				}
			}
		})
	}
}

func TestAModernClientWorksWithoutAHandshake(t *testing.T) {
	s := open(t)
	meta := speaking("cursor")

	o := s.toolWith(meta, "remember", map[string]any{"claim": "The API listens on port 8080", "check": "port localhost:8080"})
	if o.Failed {
		t.Fatalf("remember was turned down: %s", o.Text)
	}
	v := view(t, o.Data["fact"])
	if v.Status != fact.Holds || v.By != "cursor" {
		t.Errorf("the fact is %s by %q, want holds by cursor", v.Status, v.By)
	}
	o = s.toolWith(meta, "recall", map[string]any{"query": "port"})
	if got := views(t, o.Data); o.Failed || len(got) != 1 || got[0].ID != v.ID {
		t.Errorf("recall returned %+v (%s)", got, o.Text)
	}
	o = s.toolWith(meta, "verify", nil)
	if got := views(t, o.Data); o.Failed || len(got) != 1 {
		t.Errorf("verify returned %+v (%s)", got, o.Text)
	}
	o = s.toolWith(meta, "retract", map[string]any{"id": v.ID, "reason": "only a test"})
	if o.Failed {
		t.Errorf("retract was turned down: %s", o.Text)
	}
	s.silence()
}

// Every result in the 2026-07-28 revision says that it is complete, the ones
// that carry an error for the model to read included. Older clients never see
// the member.
func TestEveryModernResultIsMarkedComplete(t *testing.T) {
	calls := []struct {
		name, method string
		params       map[string]any
	}{
		{"ping", "ping", nil},
		{"tools/list", "tools/list", nil},
		{"a tool call that works", "tools/call", map[string]any{"name": "recall", "arguments": map[string]any{"query": "port"}}},
		{"a tool call that stores", "tools/call", map[string]any{"name": "remember", "arguments": map[string]any{"claim": "The API listens on port 8080", "check": "port localhost:8080"}}},
		{"a tool call that is turned down", "tools/call", map[string]any{"name": "retract", "arguments": map[string]any{"id": "f-zzzzzz", "reason": "wrong"}}},
		{"a tool call with bad arguments", "tools/call", map[string]any{"name": "verify", "arguments": map[string]any{"ids": "all"}}},
		{"server/discover", "server/discover", nil},
	}
	for _, tc := range calls {
		t.Run(tc.name, func(t *testing.T) {
			s := open(t)
			params := map[string]any{"_meta": speaking("cursor")}
			for k, v := range tc.params {
				params[k] = v
			}
			if got := s.result(tc.method, params); got["resultType"] != "complete" {
				t.Errorf("resultType = %v, want complete; the result has %v", got["resultType"], keys(got))
			}

			// The same call from a client of an older revision.
			old := open(t)
			old.handshake("claude-code")
			var plain any
			if tc.params != nil {
				plain = tc.params
			}
			got := old.result(tc.method, plain)
			if _, ok := got["resultType"]; ok && tc.method != "server/discover" {
				t.Errorf("a client of an older revision was sent resultType, which it does not know")
			}
		})
	}
}

// A request that carries the revision is served the new way and one that does
// not is served the old way, whatever came before on the same connection.
func TestBothKindsOfClientAreServedOnOneConnection(t *testing.T) {
	s := open(t)
	s.handshake("claude-code")
	if got := s.result("tools/list", map[string]any{"_meta": speaking("")}); got["resultType"] != "complete" {
		t.Errorf("a request with the revision in it, after a handshake, was answered the old way: %v", keys(got))
	}
	if got := s.result("tools/list", nil); len(got) != 1 {
		t.Errorf("a request without the revision was answered the new way: %v", keys(got))
	}
	// Metadata that says nothing about the revision does not make a request modern.
	if got := s.result("tools/list", map[string]any{"_meta": map[string]any{"progressToken": "p1"}}); len(got) != 1 {
		t.Errorf("a request whose metadata names no revision was answered the new way: %v", keys(got))
	}
}

func TestModernToolsListSaysHowLongItMayBeKept(t *testing.T) {
	s := open(t)
	got := s.result("tools/list", map[string]any{"_meta": speaking("cursor")})
	if got["ttlMs"] != float64(3600_000) || got["cacheScope"] != "public" {
		t.Errorf("ttlMs %v and cacheScope %v, want an hour and public", got["ttlMs"], got["cacheScope"])
	}
	_, names := listed(t, got)
	if want := []string{"remember", "recall", "verify", "retract"}; !reflect.DeepEqual(names, want) {
		t.Errorf("the tools are %v, want %v", names, want)
	}
	// The tools themselves are the same for both kinds of client.
	old := open(t)
	if oldTools := old.result("tools/list", nil)["tools"]; !reflect.DeepEqual(oldTools, got["tools"]) {
		t.Error("a modern client and an older one are shown different tools")
	}
}

func TestARevisionTheServerDoesNotSpeakIsTurnedDownWithTheOnesItDoes(t *testing.T) {
	for _, tc := range []struct {
		name      string
		version   any
		requested string
	}{
		{"a later revision", "2027-01-15", "2027-01-15"},
		{"the day before", "2026-07-27", "2026-07-27"},
		{"a draft", "DRAFT-2026-v1", "DRAFT-2026-v1"},
		{"an empty revision", "", ""},
		{"a revision that is not text", 20260728, ""},
		{"no revision at all", nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := open(t)
			meta := map[string]any{metaVersion: tc.version, metaClient: map[string]any{"name": "cursor"}}
			for _, call := range []struct {
				method string
				params map[string]any
			}{
				{"ping", map[string]any{}},
				{"tools/list", map[string]any{}},
				{"server/discover", map[string]any{}},
				{"tools/call", map[string]any{"name": "remember", "arguments": map[string]any{"claim": "The API listens on port 8080"}}},
				{"no/such/method", map[string]any{}},
			} {
				call.params["_meta"] = meta
				got := s.fault(call.method, call.params, -32022)
				if got.Message != "unsupported protocol version" {
					t.Errorf("%s: the message is %q", call.method, got.Message)
				}
				var data map[string]any
				if err := json.Unmarshal(got.Data, &data); err != nil {
					t.Fatalf("%s: the error carries no data to go by: %s", call.method, got.Data)
				}
				if !reflect.DeepEqual(data["supported"], spoken) {
					t.Errorf("%s: supported = %v, want %v", call.method, data["supported"], spoken)
				}
				if data["requested"] != tc.requested {
					t.Errorf("%s: requested = %v, want %q", call.method, data["requested"], tc.requested)
				}
			}
			if got := s.stored(); len(got) != 0 {
				t.Errorf("a call in a revision the server does not speak stored something: %v", got)
			}
			s.alive()
		})
	}
}
