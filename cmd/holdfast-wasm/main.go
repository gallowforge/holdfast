//go:build js && wasm

// Command holdfast-wasm is the engine behind the live demo, built for a
// browser. The page runs it instead of a server: the same code that proves
// facts on a real machine proves them here against a pretend one, so what the
// demo shows is what the tool does.
package main

import (
	"context"
	"encoding/json"
	"syscall/js"
	"time"

	"github.com/gallowforge/holdfast/internal/demo"
)

// version is set by the build.
var version = "dev"

func main() {
	door := demo.New(time.Now).API(version)
	promise := js.Global().Get("Promise")

	// call(method, body) answers as the server's POST /api/<method> would:
	// a promise of the reply, as JSON text.
	call := js.FuncOf(func(_ js.Value, args []js.Value) any {
		method, body := "", "{}"
		if len(args) > 0 {
			method = args[0].String()
		}
		if len(args) > 1 && args[1].Type() == js.TypeString {
			body = args[1].String()
		}
		var run js.Func
		run = js.FuncOf(func(_ js.Value, settle []js.Value) any {
			resolve := settle[0]
			go func() {
				defer run.Release()
				reply, _ := door.Do(context.Background(), method, json.RawMessage(body))
				data, err := json.Marshal(reply)
				if err != nil {
					data = []byte(`{"ok":false,"error":"the reply could not be written"}`)
				}
				resolve.Invoke(string(data))
			}()
			return nil
		})
		return promise.New(run)
	})

	js.Global().Set("holdfastDemo", js.ValueOf(map[string]any{"call": call, "version": version}))
	if ready := js.Global().Get("holdfastReady"); ready.Type() == js.TypeFunction {
		ready.Invoke()
	}
	select {} // stay loaded: the page calls in for as long as it is open
}
