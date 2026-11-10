package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/gallowforge/holdfast/internal/engine"
	"github.com/gallowforge/holdfast/internal/fact"
)

// Instructions is sent to every client when it connects. It is the whole
// manual an agent gets, so it is short and says when to use each tool.
const Instructions = `Holdfast is this project's memory, and every fact in it carries its own proof.

Before you rely on something you believe about this project or machine (a port, a path, a version, a command, where something runs), call recall. Each fact with a check is proved again before it is returned, so what comes back marked HOLDS is true right now, and what comes back BROKEN used to be true and is not any more.

When you learn something a later session will need, call remember, and give it a check whenever the fact can be tested. A fact with a check cannot quietly go out of date. A fact without one is kept as a note and expires.

When a fact is wrong, call retract; when it has changed, call remember with replaces.`

const checkHelp = "The check language, one check per fact:\n" + fact.Grammar + "\n" +
	"Paths are relative to the project folder. Checks only touch what the project's policy allows: files inside the project and services on this machine by default. " +
	"A check outside the policy is stored as BLOCKED until a person allows it; you cannot widen the policy yourself, so do not try other spellings."

type tool struct {
	Name        string         `json:"name"`
	Title       string         `json:"title"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	Annotations map[string]any `json:"annotations"`
}

func object(required []string, props map[string]any) map[string]any {
	s := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

func str(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}

var tools = []tool{
	{
		Name:  "remember",
		Title: "Remember a fact",
		Description: "Store a fact about this project or machine for later sessions. Give a check whenever the fact can be tested: " +
			"the check runs now (a fact whose check fails is refused) and again every time the fact is recalled, so it can never quietly go out of date. " +
			"Without a check the fact is kept as an unproven note that expires.\n\n" + checkHelp,
		InputSchema: object([]string{"claim"}, map[string]any{
			"claim": str("The fact, as one plain sentence. Example: The API listens on port 8080."),
			"check": str("How to prove the claim, in the check language. Examples: port localhost:8080 · contains go.mod \"go 1.24\" · http 200 http://localhost:8080/healthz · not file legacy/config.yml"),
			"tags": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "maxItems": fact.MaxTags,
				"description": "A few lower-case words to find the fact by, such as build, deploy, database."},
			"replaces": str("The id of a fact this one supersedes, when something has changed. The old fact is withdrawn."),
			"ttl":      str("Only for a fact without a check: how long to believe it, such as 12h, 7d or 4w. The default is 30d."),
		}),
		Annotations: map[string]any{"readOnlyHint": false, "destructiveHint": false, "idempotentHint": true, "openWorldHint": true},
	},
	{
		Name:  "recall",
		Title: "Recall what holds",
		Description: "Ask what is known before relying on memory. Returns the stored facts that match the query, and proves every fact that has a check again before returning it. " +
			"HOLDS: the check passed just now. BROKEN: it was true once and the check now fails; do not rely on it. " +
			"TIMED OUT: the check did not finish, so the fact is as last known and is not proved. " +
			"BLOCKED: the check was not allowed to run. UNPROVEN and STALE: notes with no check. " +
			"An empty query lists everything, most urgent first.",
		InputSchema: object(nil, map[string]any{
			"query": str("A few words about what you need to know, such as: api port, or: how is the release built."),
			"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": engine.MaxLimit,
				"description": fmt.Sprintf("The most facts to return. The default is %d.", engine.DefaultLimit)},
		}),
		// Not read-only: proving a fact runs its check (a request, a command a
		// person allowed) and the result is written down as evidence.
		Annotations: map[string]any{"readOnlyHint": false, "destructiveHint": false, "idempotentHint": true, "openWorldHint": true},
	},
	{
		Name:        "verify",
		Title:       "Prove facts again",
		Description: "Run the checks again now: for the facts named, or for every fact when no ids are given. Use it after changing something, to see which remembered facts your change broke.",
		InputSchema: object(nil, map[string]any{
			"ids": map[string]any{"type": "array", "items": map[string]any{"type": "string"},
				"description": "Fact ids such as f-abc234. Leave out to prove everything."},
		}),
		// Not read-only: proving a fact runs its check (a request, a command a
		// person allowed) and the result is written down as evidence.
		Annotations: map[string]any{"readOnlyHint": false, "destructiveHint": false, "idempotentHint": true, "openWorldHint": true},
	},
	{
		Name:        "retract",
		Title:       "Withdraw a fact",
		Description: "Withdraw a fact that is wrong or no longer matters. It is kept in the history with your reason and is never recalled again. If the fact has changed rather than gone, use remember with replaces instead.",
		InputSchema: object([]string{"id", "reason"}, map[string]any{
			"id":     str("The id of the fact, such as f-abc234."),
			"reason": str("Why, in a few words. Example: the service was removed in the move to containers."),
		}),
		Annotations: map[string]any{"readOnlyHint": false, "destructiveHint": false, "idempotentHint": true, "openWorldHint": false},
	},
}

// badArgs marks a problem with what the caller sent, which the caller can fix.
type badArgs struct{ msg string }

func (b *badArgs) Error() string { return b.msg }

// decode reads tool arguments strictly: an unknown or mistyped field is an
// error the agent sees, not something silently dropped.
func decode(raw json.RawMessage, into any) error {
	if len(raw) == 0 || string(raw) == "null" {
		raw = json.RawMessage("{}")
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		return &badArgs{"the arguments are not right: " + plain(err)}
	}
	return nil
}

// plain says what is wrong with the arguments without the names Go has for
// things, which mean nothing to the agent that reads it.
func plain(err error) string {
	var wrong *json.UnmarshalTypeError
	if errors.As(err, &wrong) {
		if wrong.Field == "" {
			return "expected an object, got " + wrong.Value
		}
		return fmt.Sprintf("%q is the wrong kind of value (%s)", wrong.Field, wrong.Value)
	}
	return strings.TrimPrefix(err.Error(), "json: ")
}

// call runs one tool and returns the text for the agent, the same answer as
// structured data, and whether it is an error the agent should read.
func (s *Server) call(ctx context.Context, name string, raw json.RawMessage, who string) (text string, data any, failed bool, err error) {
	now := s.Engine.World.Now()
	fail := func(e error) (string, any, bool, error) {
		var refused *engine.Refused
		var bad *badArgs
		if errors.As(e, &refused) || errors.As(e, &bad) {
			return e.Error(), nil, true, nil
		}
		return "", nil, false, e
	}
	switch name {
	case "remember":
		var a struct {
			Claim    string   `json:"claim"`
			Check    string   `json:"check"`
			Tags     []string `json:"tags"`
			Replaces string   `json:"replaces"`
			TTL      string   `json:"ttl"`
		}
		if e := decode(raw, &a); e != nil {
			return fail(e)
		}
		v, already, e := s.Engine.Remember(ctx, engine.Claim{
			Claim: a.Claim, Check: a.Check, Tags: a.Tags, Replaces: a.Replaces, TTL: a.TTL, By: who,
		})
		if e != nil {
			return fail(e)
		}
		head := "Stored, and proved just now."
		switch {
		case already:
			head = "This was already remembered; nothing new was stored."
		case v.Status == fact.Blocked:
			head = "Stored, but not proved: the check may not run until a person allows it. Tell the person; until then treat this as unproven."
		case v.Check == "":
			head = "Stored as a note. It has no check, so nothing proves it and it will be marked stale when it is " +
				fact.FormatDuration(v.StaleAt.Sub(*v.NotedAt)) + " old. Give facts a check whenever one is possible."
		}
		if v.Replaces != "" && !already {
			head += " " + v.Replaces + " was withdrawn in its favour."
		}
		return head + "\n\n" + v.Text(now), map[string]any{"fact": v, "already": already}, false, nil
	case "recall":
		var a struct {
			Query string `json:"query"`
			Limit int    `json:"limit"`
		}
		if e := decode(raw, &a); e != nil {
			return fail(e)
		}
		r, e := s.Engine.Recall(ctx, a.Query, a.Limit)
		if e != nil {
			return fail(e)
		}
		if r.Facts == nil {
			r.Facts = []engine.View{}
		}
		return r.Text(s.Engine.World.Now()), r, false, nil
	case "verify":
		var a struct {
			IDs []string `json:"ids"`
		}
		if e := decode(raw, &a); e != nil {
			return fail(e)
		}
		r, e := s.Engine.Verify(ctx, a.IDs)
		if e != nil {
			return fail(e)
		}
		if r.Facts == nil {
			r.Facts = []engine.View{}
		}
		return r.Text(s.Engine.World.Now()), r, false, nil
	case "retract":
		var a struct {
			ID     string `json:"id"`
			Reason string `json:"reason"`
		}
		if e := decode(raw, &a); e != nil {
			return fail(e)
		}
		v, e := s.Engine.Retract(a.ID, a.Reason, who)
		if e != nil {
			return fail(e)
		}
		return "Withdrawn. It will not be recalled again.\n\n" + v.Text(now), map[string]any{"fact": v}, false, nil
	}
	return "", nil, false, errUnknownTool
}

var errUnknownTool = errors.New("unknown tool")
