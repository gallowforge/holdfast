package fact

import (
	"reflect"
	"testing"
	"time"
)

var t0 = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

func TestStatusAtWithoutCheck(t *testing.T) {
	day := 24 * time.Hour
	tests := []struct {
		name    string
		ttl     Seconds
		vouched time.Duration // after creation; 0 for never
		age     time.Duration
		want    Status
	}{
		{"new note", 0, 0, 0, Unproven},
		{"default ttl, one day short", 0, 0, 29 * day, Unproven},
		{"default ttl, to the second", 0, 0, DefaultTTL, Unproven},
		{"default ttl, one second over", 0, 0, DefaultTTL + time.Second, Stale},
		{"own ttl, inside", 3600, 0, 59 * time.Minute, Unproven},
		{"own ttl, to the second", 3600, 0, time.Hour, Unproven},
		{"own ttl, over", 3600, 0, time.Hour + time.Second, Stale},
		{"own ttl longer than the default", Seconds(90 * 24 * 3600), 0, 60 * day, Unproven},
		{"negative ttl means the default", -5, 0, day, Unproven},
		{"vouched: age counts from the vouch", 3600, 2 * time.Hour, 2*time.Hour + 30*time.Minute, Unproven},
		{"vouched, then stale again", 3600, 2 * time.Hour, 3*time.Hour + time.Second, Stale},
		{"read before it was written", 0, 0, -time.Hour, Unproven},
	}
	for _, tt := range tests {
		f := &Fact{Claim: "x", Created: t0, TTL: tt.ttl}
		if tt.vouched != 0 {
			f.Vouched = t0.Add(tt.vouched)
		}
		if got := f.StatusAt(t0.Add(tt.age)); got != tt.want {
			t.Errorf("%s: StatusAt = %s, want %s", tt.name, got, tt.want)
		}
	}
}

func TestNotedAt(t *testing.T) {
	f := &Fact{Created: t0}
	if !f.NotedAt().Equal(t0) {
		t.Errorf("NotedAt() = %v, want the creation time", f.NotedAt())
	}
	f.Vouched = t0.Add(time.Hour)
	if !f.NotedAt().Equal(t0.Add(time.Hour)) {
		t.Errorf("NotedAt() = %v, want the vouch time", f.NotedAt())
	}
	// A vouch dated before the fact was written cannot make it look older.
	f.Vouched = t0.Add(-time.Hour)
	if !f.NotedAt().Equal(t0) {
		t.Errorf("NotedAt() = %v, want the creation time for an earlier vouch", f.NotedAt())
	}
}

func TestStatusAtWithCheck(t *testing.T) {
	c := &Check{Kind: "file", Path: "go.mod"}
	f := &Fact{Claim: "x", Created: t0, Check: c}
	// Never run: neither proved nor disproved, however old it is.
	for _, age := range []time.Duration{0, time.Hour, 10 * 365 * 24 * time.Hour} {
		if got := f.StatusAt(t0.Add(age)); got != Blocked {
			t.Errorf("a check that never ran, %v on: StatusAt = %s, want blocked", age, got)
		}
	}
	// With evidence the status is the evidence, and a time to live plays no part.
	for _, s := range []Status{Holds, Broken, Blocked} {
		f.Last = &Evidence{Status: s, At: t0}
		f.TTL = 1
		if got := f.StatusAt(t0.Add(365 * 24 * time.Hour)); got != s {
			t.Errorf("last evidence %s: StatusAt = %s", s, got)
		}
	}
}

func TestTimeToLiveAndLive(t *testing.T) {
	f := &Fact{}
	if f.TimeToLive() != DefaultTTL {
		t.Errorf("TimeToLive() = %v, want the default", f.TimeToLive())
	}
	f.TTL = 90
	if f.TimeToLive() != 90*time.Second || f.TTL.Duration() != 90*time.Second {
		t.Errorf("TimeToLive() = %v, want 90s", f.TimeToLive())
	}
	if !f.Live() {
		t.Error("a new fact is not live")
	}
	f.Retracted = &Retraction{At: t0}
	if f.Live() {
		t.Error("a retracted fact is live")
	}
	f.Retracted, f.ReplacedBy = nil, "f-234567"
	if f.Live() {
		t.Error("a replaced fact is live")
	}
}

func TestRank(t *testing.T) {
	order := []Status{Broken, Blocked, Stale, Unproven, Holds}
	for i, s := range order {
		if got := s.Rank(); got != i {
			t.Errorf("%s.Rank() = %d, want %d", s, got, i)
		}
	}
	if got := Status("nonsense").Rank(); got != Holds.Rank() {
		t.Errorf("an unknown status ranks %d, want it last with holds", got)
	}
}

func TestAgo(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{-time.Hour, "just now"},
		{0, "just now"},
		{4 * time.Second, "just now"},
		{5 * time.Second, "5s ago"},
		{59 * time.Second, "59s ago"},
		{59*time.Second + 999*time.Millisecond, "59s ago"},
		{time.Minute, "1m ago"},
		{59 * time.Minute, "59m ago"},
		{time.Hour, "1h ago"},
		{47*time.Hour + 59*time.Minute, "47h ago"},
		{48 * time.Hour, "2d ago"},
		{40 * 24 * time.Hour, "40d ago"},
	}
	for _, tt := range tests {
		if got := Ago(t0, t0.Add(-tt.d)); got != tt.want {
			t.Errorf("Ago(%v) = %q, want %q", tt.d, got, tt.want)
		}
	}
}

func fullFact() *Fact {
	return &Fact{
		ID:      "f-234567",
		Claim:   "the API listens on 8080",
		Tags:    []string{"api", "ports"},
		Check:   &Check{Kind: "says", Text: "active", Argv: []string{"systemctl", "is-active", "api"}},
		By:      "agent",
		Created: t0,
		TTL:     3600,
		Last:    &Evidence{Status: Holds, Detail: "first", At: t0, Ms: 3},
		Since:   t0,
		Vouched: t0, VouchedBy: "alex",
		Retracted: &Retraction{At: t0, By: "alex", Reason: "wrong"},
		History:   []Evidence{{Status: Broken, Detail: "was down"}, {Status: Holds, Detail: "first"}},
	}
}

func TestCloneSharesNothing(t *testing.T) {
	orig := fullFact()
	c := orig.Clone()
	if !reflect.DeepEqual(orig, c) {
		t.Fatalf("the clone differs:\n got %+v\nwant %+v", c, orig)
	}
	if c == orig || c.Check == orig.Check || c.Last == orig.Last || c.Retracted == orig.Retracted {
		t.Fatal("the clone shares a pointer with the original")
	}

	// Change everything reachable from the clone; the original must not move.
	c.Tags[0] = "changed"
	c.Check.Argv[0] = "rm"
	c.Check.Not = true
	c.Last.Detail = "changed"
	c.Retracted.Reason = "changed"
	c.History[0].Detail = "changed"
	c.Tags = append(c.Tags, "more")
	c.History = append(c.History, Evidence{})
	if want := fullFact(); !reflect.DeepEqual(orig, want) {
		t.Errorf("changing the clone changed the original:\n got %+v\nwant %+v", orig, want)
	}

	// And the other way round.
	c = orig.Clone()
	orig.Tags[1] = "changed"
	orig.Check.Argv[2] = "changed"
	orig.Last.Status = Broken
	orig.History[1].Status = Broken
	if want := fullFact(); !reflect.DeepEqual(c, want) {
		t.Errorf("changing the original changed the clone:\n got %+v\nwant %+v", c, want)
	}
}

func TestCloneOfLittle(t *testing.T) {
	var none *Fact
	if none.Clone() != nil {
		t.Error("the clone of no fact is a fact")
	}
	var noCheck *Check
	if noCheck.Clone() != nil {
		t.Error("the clone of no check is a check")
	}
	bare := &Fact{ID: "f-234567", Claim: "x", Created: t0}
	c := bare.Clone()
	if c.Check != nil || c.Last != nil || c.Retracted != nil || len(c.Tags) != 0 || len(c.History) != 0 {
		t.Errorf("the clone of a bare fact grew something: %+v", c)
	}
	// A check's clone does not share its arguments.
	k := &Check{Kind: "cmd", Argv: []string{"a", "b"}}
	d := k.Clone()
	d.Argv[1] = "changed"
	if k.Argv[1] != "b" {
		t.Error("the check's clone shares its arguments")
	}
}
