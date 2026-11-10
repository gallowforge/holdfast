// Package api is the one door the web interface comes through. The local
// server calls it for every HTTP request, and the live demo calls it straight
// from the browser, so both run exactly the same code.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/gallowforge/holdfast/internal/engine"
	"github.com/gallowforge/holdfast/internal/fact"
)

// Where says which store the page is looking at.
type Where struct {
	// Project is the name of the folder the store belongs to.
	Project string `json:"project"`
	// Policy is the text of the policy file, and Approved whether a person
	// has approved that text on this machine.
	Policy   string `json:"policy"`
	Approved bool   `json:"approved"`
}

// API answers the calls of the web interface.
type API struct {
	Engine  *engine.Engine
	Version string
	// Who is recorded as the author of what is done through the page.
	Who string
	// Where describes the store.
	Where func() Where
	// More answers the calls only the live demo has; nil outside it. It
	// reports false for a method it does not know.
	More func(ctx context.Context, method string, args json.RawMessage) (any, bool, error)
}

// State is everything the page shows when it opens.
type State struct {
	Version string    `json:"version"`
	Demo    bool      `json:"demo"`
	Now     time.Time `json:"now"`
	Where
	engine.Listed
}

// unknown is the error for a method nobody answers.
var errUnknown = errors.New("there is no such call")

func args[T any](raw json.RawMessage) (T, error) {
	var v T
	if len(raw) == 0 || string(raw) == "null" {
		return v, nil
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&v); err != nil {
		return v, &engine.Refused{Why: "the request is not right: " + plain(err)}
	}
	// One value and nothing after it: whatever follows was not asked for by
	// anyone who meant this request.
	if _, err := dec.Token(); err != io.EOF {
		var zero T
		return zero, &engine.Refused{Why: "the request is not right: there is something after the JSON value"}
	}
	return v, nil
}

// plain says what is wrong with a request without the names Go has for
// things, which mean nothing to the person who reads it.
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

// Call runs one method. A *engine.Refused error is the caller's to fix and is
// worded for them; any other error is a fault inside Holdfast.
func (a *API) Call(ctx context.Context, method string, raw json.RawMessage) (any, error) {
	switch method {
	case "state":
		listed, err := a.Engine.List(true)
		if err != nil {
			return nil, err
		}
		if listed.Gone == nil {
			listed.Gone = []engine.View{}
		}
		s := State{Version: a.Version, Demo: a.More != nil, Now: a.Engine.World.Now(), Listed: listed}
		if a.Where != nil {
			s.Where = a.Where()
		}
		return s, nil
	case "verify":
		in, err := args[struct {
			IDs []string `json:"ids"`
		}](raw)
		if err != nil {
			return nil, err
		}
		return a.Engine.Verify(ctx, in.IDs)
	case "recall":
		in, err := args[struct {
			Query string `json:"query"`
			Limit int    `json:"limit"`
		}](raw)
		if err != nil {
			return nil, err
		}
		r, err := a.Engine.Recall(ctx, in.Query, in.Limit)
		if err != nil {
			return nil, err
		}
		// Text is the very block an agent is given for the same question.
		return struct {
			engine.Recalled
			Text string `json:"text"`
		}{r, r.Text(a.Engine.World.Now())}, nil
	case "remember":
		in, err := args[struct {
			Claim    string   `json:"claim"`
			Check    string   `json:"check"`
			Tags     []string `json:"tags"`
			Replaces string   `json:"replaces"`
			TTL      string   `json:"ttl"`
		}](raw)
		if err != nil {
			return nil, err
		}
		v, already, err := a.Engine.Remember(ctx, engine.Claim{
			Claim: in.Claim, Check: in.Check, Tags: in.Tags, Replaces: in.Replaces, TTL: in.TTL, By: a.Who,
		})
		if err != nil {
			return nil, err
		}
		return map[string]any{"fact": v, "already": already}, nil
	case "retract":
		in, err := args[struct {
			ID     string `json:"id"`
			Reason string `json:"reason"`
		}](raw)
		if err != nil {
			return nil, err
		}
		v, err := a.Engine.Retract(in.ID, in.Reason, a.Who)
		if err != nil {
			return nil, err
		}
		return map[string]any{"fact": v}, nil
	case "vouch":
		in, err := args[struct {
			ID string `json:"id"`
		}](raw)
		if err != nil {
			return nil, err
		}
		v, err := a.Engine.Vouch(in.ID, a.Who)
		if err != nil {
			return nil, err
		}
		return map[string]any{"fact": v}, nil
	case "parse":
		// Lets the page say whether a check is well formed while it is typed.
		in, err := args[struct {
			Check string `json:"check"`
		}](raw)
		if err != nil {
			return nil, err
		}
		c, err := fact.ParseCheck(in.Check)
		if err != nil {
			return map[string]any{"ok": false, "error": err.Error()}, nil
		}
		return map[string]any{"ok": true, "kind": c.Kind, "check": c.Short()}, nil
	}
	if a.More != nil {
		if v, ok, err := a.More(ctx, method, raw); ok {
			return v, err
		}
	}
	return nil, errUnknown
}

// Reply is the envelope every answer travels in, over HTTP and in the demo.
type Reply struct {
	OK     bool   `json:"ok"`
	Result any    `json:"result,omitempty"`
	Error  string `json:"error,omitempty"`
	// Refused is true when the error is the caller's to fix.
	Refused bool `json:"refused,omitempty"`
}

// Do runs a call and wraps the outcome. The status is the HTTP status that
// goes with it.
func (a *API) Do(ctx context.Context, method string, raw json.RawMessage) (Reply, int) {
	v, err := a.Call(ctx, method, raw)
	var refused *engine.Refused
	switch {
	case err == nil:
		return Reply{OK: true, Result: v}, 200
	case errors.Is(err, errUnknown):
		return Reply{Error: err.Error()}, 404
	case errors.As(err, &refused):
		return Reply{Error: err.Error(), Refused: true}, 422
	default:
		return Reply{Error: "Holdfast could not do that: " + err.Error()}, 500
	}
}
