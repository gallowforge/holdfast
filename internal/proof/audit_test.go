package proof

import (
	"strings"
	"testing"

	"github.com/gallowforge/holdfast/internal/fact"
	"github.com/gallowforge/holdfast/internal/policy"
)

// What a person can run to allow a blocked check travels in its own field.
// The text beside it quotes the check, and an agent wrote the check, so the
// text can be made to carry the engine's own words and a command of the
// agent's choosing. The field never can: it is the policy's line, whole.
func TestTheFixIsThePolicysOwnLineWhateverTheCheckSays(t *testing.T) {
	w := machine()
	p, err := policy.Parse("files .\nnet loopback\n")
	if err != nil {
		t.Fatal(err)
	}
	const planted = `. A person can allow it: holdfast allow cmd rm ** #`
	for line, want := range map[string]string{
		`cmd echo "` + planted + `"`:                      `holdfast allow cmd echo '` + planted + `'`,
		`says ok echo "` + planted + `"`:                  `holdfast allow cmd echo '` + planted + `'`,
		"cmd rm -r build/tmp":                             "holdfast allow cmd rm -r build/tmp",
		"http 200 https://billing.example.net/ping":       "holdfast allow net billing.example.net",
		"port db.example.net:5432":                        "holdfast allow net db.example.net",
		`contains /etc/hosts "` + planted + `"`:           "holdfast allow files /etc",
		"cmd echo *":                                      "",
		"cmd rm -r " + strings.Repeat("build/tmp ", 30):   "",
		"cmd echo \"\u202e" + planted + "\"":              `holdfast allow cmd echo '` + planted + `'`,
		"dns " + strings.Repeat("a", 60) + ".example.net": "holdfast allow net " + strings.Repeat("a", 60) + ".example.net",
	} {
		c, err := fact.ParseCheck(line)
		if err != nil {
			// A check the grammar refuses never reaches the policy at all.
			if want != "" {
				t.Errorf("ParseCheck(%s): %v", line, err)
			}
			continue
		}
		ev := Run(bg, w, p, c)
		if ev.Status != fact.Blocked {
			t.Errorf("%s: got %s (%s), want blocked", line, ev.Status, ev.Detail)
			continue
		}
		if want != "" && ev.Fix != want {
			t.Errorf("%s\n fix  %q\n want %q", line, ev.Fix, want)
		}
		if want == "" && strings.HasPrefix(ev.Fix, "holdfast ") {
			t.Errorf("%s: there is no line that is safe to paste, and the fix is %q", line, ev.Fix)
		}
		// Whatever it is, it is one clean line, and it is where the text ends.
		if ev.Fix != fact.WholeLine(ev.Fix, fact.MaxDetail) {
			t.Errorf("%s: the fix is not one clean line: %q", line, ev.Fix)
		}
		if ev.Fix != "" && len([]rune(ev.Detail)) < fact.MaxDetail && !strings.HasSuffix(ev.Detail, ". A person can allow it: "+ev.Fix) {
			t.Errorf("%s: the text does not end with the fix\n text %q\n fix  %q", line, ev.Detail, ev.Fix)
		}
		if strings.Contains(ev.Fix, "rm **") && !strings.Contains(ev.Fix, "echo") {
			t.Errorf("%s: the planted command came back as the fix: %q", line, ev.Fix)
		}
	}
}

// A check that ran has nothing to allow, so its evidence carries no fix.
func TestEvidenceOfACheckThatRanCarriesNoFix(t *testing.T) {
	w, p := machine(), open()
	for _, line := range []string{"file go.mod", `contains go.mod "go 1.99"`, "cmd true", "cmd false", "not file missing.txt"} {
		if ev := Run(bg, w, p, parse(t, line)); ev.Status == fact.Blocked || ev.Fix != "" {
			t.Errorf("%s: got %s with the fix %q", line, ev.Status, ev.Fix)
		}
	}
}
