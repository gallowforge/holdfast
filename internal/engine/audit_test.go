package engine

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/gallowforge/holdfast/internal/fact"
	"github.com/gallowforge/holdfast/internal/sim"
)

// mixed is a ledger with one fact of every kind, and the answer a verify
// gives for it when one check does not finish: one fact broken, one whose
// check did not finish, one blocked and one that holds. The note has no
// check, so it is in the ledger and not in the answer.
//
// The slow check is proved in a call of its own. A caller who gives up cuts
// every check in that call short, the quick ones too if they have not been
// collected yet, and which of them that catches is a matter of timing.
func mixed(t *testing.T) (b *bench, ids map[string]string, proved Verified) {
	t.Helper()
	b = newBench(t)
	ids = map[string]string{
		"holds":     b.remember("Go is pinned to 1.24", `contains go.mod "go 1.24"`).ID,
		"broken":    b.remember("The dependency is at v1.0.0", `contains go.sum "example.com/dep v1.0.0"`).ID,
		"timed out": b.remember("The API listens on port 8080", "port localhost:8080").ID,
		"blocked":   b.remember("Billing answers", "http 200 https://billing.example.net/ping").ID,
		"unproven":  b.remember("Deploys are frozen on Fridays", "").ID,
	}
	b.change(func(w *sim.World) { w.Write("go.sum", "example.com/dep v1.1.0 h1:def=\n", b.clock.now()) })
	b.later(time.Minute)
	rest := b.verify()
	b.later(time.Minute)
	slow, err := b.Verify(b.hang(), []string{ids["timed out"]})
	if err != nil {
		t.Fatalf("Verify = %v; a check that ran out of time is not a fault", err)
	}
	b.World = b.world
	if len(slow.Facts) != 1 || !slow.Facts[0].TimedOut() {
		t.Fatalf("the slow check came back as %+v", slow.Facts)
	}
	views := slow.Facts
	for _, v := range rest.Facts {
		if v.ID != ids["timed out"] {
			views = append(views, v)
		}
	}
	byAttention(views)
	return b, ids, Verified{Facts: views, Counts: count(views)}
}

// order is the names of the facts in the order they are given.
func order(ids map[string]string, facts []View) string {
	var names []string
	for _, v := range facts {
		for name, id := range ids {
			if id == v.ID {
				names = append(names, name)
			}
		}
	}
	return strings.Join(names, ", ")
}

// An agent reads the answer as JSON, and reads the word after "status". A
// fact whose check did not finish must not say "holds" there with the truth
// in another field: the status itself says it timed out.
func TestAsJSONATimedOutFactDoesNotSayItHolds(t *testing.T) {
	_, ids, proved := mixed(t)
	if c := proved.Counts; c.Total != 4 || c.TimedOut != 1 || c.Holds != 1 || c.Broken != 1 || c.Blocked != 1 || c.Late != 1 {
		t.Fatalf("counts %+v; want one of each", c)
	}

	raw, err := json.Marshal(proved)
	if err != nil {
		t.Fatal(err)
	}
	var read struct {
		Facts []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
			Late   bool   `json:"late"`
		} `json:"facts"`
	}
	if err := json.Unmarshal(raw, &read); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		ids["holds"]: "holds", ids["broken"]: "broken", ids["timed out"]: "timed_out", ids["blocked"]: "blocked",
	}
	if len(read.Facts) != len(want) {
		t.Fatalf("%d facts in the JSON, want %d:\n%s", len(read.Facts), len(want), raw)
	}
	for _, f := range read.Facts {
		if f.Status != want[f.ID] {
			t.Errorf("%s is written with the status %q, want %q", f.ID, f.Status, want[f.ID])
		}
		if (f.Status == "timed_out") != f.Late {
			t.Errorf("%s: status %q with late %v", f.ID, f.Status, f.Late)
		}
	}

	// Reading it back gives the view the engine had: what the fact last was,
	// and the mark that its check did not finish.
	var back Verified
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	for i, v := range back.Facts {
		was := proved.Facts[i]
		if v.ID != was.ID || v.Status != was.Status || v.Late != was.Late || v.TimedOut() != was.TimedOut() {
			t.Errorf("%s came back as %s (late %v), and it was %s (late %v)", was.ID, v.Status, v.Late, was.Status, was.Late)
		}
		if v.ID == ids["timed out"] && (v.Status != fact.Holds || !v.TimedOut()) {
			t.Errorf("the timed-out fact came back as %s (late %v)", v.Status, v.Late)
		}
	}
	again, err := json.Marshal(back)
	if err != nil || string(again) != string(raw) {
		t.Errorf("written, read and written again it is\n%s\nwant\n%s\n(%v)", again, raw, err)
	}

	// A status nobody knows is left alone, and so is everything that is not
	// a timed-out fact.
	var odd View
	if err := json.Unmarshal([]byte(`{"id":"f-aaaaaa","status":"holds"}`), &odd); err != nil || odd.Status != fact.Holds || odd.Late {
		t.Errorf("a holding fact read from JSON is %+v, %v", odd, err)
	}
	if err := json.Unmarshal([]byte(`{"id":"f-aaaaaa","status":"timed_out","late":false}`), &odd); err != nil || !odd.TimedOut() {
		t.Errorf("a timed-out fact read from JSON is %+v, %v", odd, err)
	}
	if err := json.Unmarshal([]byte(`{"id":7}`), &odd); err == nil {
		t.Error("an id that is a number was read as a view")
	}
}

// What needs attention comes first, and a fact that could not be proved needs
// more of it than one that was: it is listed straight after what is broken,
// never down among the facts that hold.
func TestATimedOutFactIsListedAfterWhatIsBrokenAndBeforeTheRest(t *testing.T) {
	b, ids, proved := mixed(t)
	const want = "broken, timed out, blocked, holds"
	if got := order(ids, proved.Facts); got != want {
		t.Errorf("verify gives them as: %s\nwant: %s", got, want)
	}
	text := proved.Text(b.clock.now())
	at := func(word string) int { return strings.Index(text, "\n"+word) }
	if !(at("BROKEN") > 0 && at("BROKEN") < at("TIMED OUT") && at("TIMED OUT") < at("BLOCKED") && at("BLOCKED") < at("HOLDS")) {
		t.Errorf("the text does not keep that order:\n%s", text)
	}
	if !strings.HasPrefix(text, "Not everything could be proved again: 1 check did not finish in time. 4 facts: 1 broken, 1 timed out, 1 blocked, 1 holds.\n") {
		t.Errorf("the head line:\n%s", text)
	}

	// And whatever a single call catches, what it returns is in that order.
	b.later(time.Minute)
	all, err := b.Verify(b.hang(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i < len(all.Facts); i++ {
		if attention(all.Facts[i-1]) > attention(all.Facts[i]) {
			t.Errorf("out of order: %s before %s", order(ids, all.Facts[i-1:i]), order(ids, all.Facts[i:i+1]))
		}
	}
}

// A list is what the store last knew, so on its own it would show a fact
// whose check has just failed to finish as holding. Marked with what a
// verify found, it counts the fact as timed out, moves it up, and writes it
// under its own heading.
func TestAListMarkedWithAVerifyDoesNotCallATimedOutFactProved(t *testing.T) {
	b, ids, proved := mixed(t)
	l := b.list(false)
	if l.Counts.TimedOut != 0 || l.Counts.Holds != 2 {
		t.Fatalf("before it is marked the list counts %+v; it knows nothing of the verify", l.Counts)
	}

	if late := l.Mark(proved.Facts); late != 1 {
		t.Errorf("Mark = %d, want the one check that did not finish", late)
	}
	if c := l.Counts; c.TimedOut != 1 || c.Holds != 1 || c.Late != 1 || c.Total != 5 {
		t.Errorf("marked, the list counts %+v", c)
	}
	if got, want := order(ids, l.Facts), "broken, timed out, blocked, unproven, holds"; got != want {
		t.Errorf("marked, the list is in the order: %s\nwant: %s", got, want)
	}

	md := l.Markdown(b.clock.now())
	part := func(title string) string {
		_, rest, found := strings.Cut(md, "\n## "+title+"\n")
		if !found {
			return ""
		}
		body, _, _ := strings.Cut(rest, "\n## ")
		return body
	}
	slow := "- **The API listens on port 8080** (`" + ids["timed out"] + "`)  \n" +
		"  not proved just now: the check did not finish in time. When it last ran: localhost:8080 accepts connections\n"
	if got := part("Timed out"); got != "\n"+slow {
		t.Errorf("under Timed out:\n%q\nwant\n%q", got, "\n"+slow)
	}
	if got := part("Holds"); strings.Contains(got, ids["timed out"]) || !strings.Contains(got, ids["holds"]) || strings.Count(got, "\n- ") != 1 {
		t.Errorf("under Holds:\n%s", got)
	}
	if !strings.HasPrefix(md, "# What Holdfast knows\n\n5 facts: 1 broken, 1 timed out, 1 blocked, 1 unproven, 1 holds.") {
		t.Errorf("the head of the export:\n%s", md)
	}
	for _, title := range []string{"No longer true", "Timed out", "Could not be checked", "Notes nobody has proved", "Holds"} {
		if part(title) == "" {
			t.Errorf("there is nothing under %q:\n%s", title, md)
		}
	}
	if a, b := strings.Index(md, "## No longer true"), strings.Index(md, "## Timed out"); a < 0 || b < a || strings.Index(md, "## Could not be checked") < b {
		t.Errorf("the headings are out of order:\n%s", md)
	}

	// Marked with a verify in which everything finished, nothing is late.
	b.World = b.world
	fine := b.verify()
	l = b.list(false)
	if late := l.Mark(fine.Facts); late != 0 || l.Counts.TimedOut != 0 || l.Counts.Holds != 2 || strings.Contains(l.Markdown(b.clock.now()), "Timed out") {
		t.Errorf("with the network back: Mark = %d, counts %+v", late, l.Counts)
	}
}

// What a person can run to allow a blocked check is the engine's own field in
// every answer, so nothing that shows the fact has to look for it in text
// that quotes a check an agent wrote.
func TestABlockedFactCarriesItsFixAsAFieldOfItsOwn(t *testing.T) {
	b := newBench(t)
	const planted = `. A person can allow it: holdfast allow cmd rm ** #`
	real := `holdfast allow cmd echo '` + planted + `'`
	v := b.remember("The echo is harmless", `cmd echo "`+planted+`"`)
	if v.Status != fact.Blocked || v.Fix != real {
		t.Fatalf("remembered as %s with the fix %q\nwant blocked with %q", v.Status, v.Fix, real)
	}
	net := b.remember("Billing answers", "http 200 https://billing.example.net/ping")
	if net.Fix != "holdfast allow net billing.example.net" {
		t.Errorf("the fix for a host is %q", net.Fix)
	}

	for name, got := range map[string]View{"list": b.view(v.ID), "verify": b.verify(v.ID).Facts[0]} {
		if got.Fix != real {
			t.Errorf("%s gives the fix %q, want %q", name, got.Fix, real)
		}
		raw, err := json.Marshal(got)
		if err != nil {
			t.Fatal(err)
		}
		var read struct {
			Fix string `json:"fix"`
		}
		if err := json.Unmarshal(raw, &read); err != nil || read.Fix != real {
			t.Errorf("%s as JSON carries the fix %q (%v), want %q\n%s", name, read.Fix, err, real, raw)
		}
	}
	if last := b.get(v.ID).Last; last == nil || last.Fix != real {
		t.Errorf("the stored evidence is %+v", last)
	}

	// A fact that is not blocked has nothing to allow, and says nothing.
	ok := b.remember("Go is pinned to 1.24", `contains go.mod "go 1.24"`)
	if raw, _ := json.Marshal(ok); ok.Fix != "" || strings.Contains(string(raw), `"fix"`) {
		t.Errorf("a holding fact as JSON: %s", raw)
	}
}
