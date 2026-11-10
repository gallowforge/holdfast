package fact

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestParseDuration(t *testing.T) {
	day := 24 * time.Hour
	good := []struct {
		in   string
		want time.Duration
	}{
		{"90s", 90 * time.Second},
		{"15m", 15 * time.Minute},
		{"26h", 26 * time.Hour},
		{"30d", 30 * day},
		{"2w", 14 * day},
		{"1s", time.Second},
		{" 7D ", 7 * day},
		{"007h", 7 * time.Hour},
		{"3650d", maxDuration},
		{"315360000s", maxDuration},
	}
	for _, tt := range good {
		got, err := ParseDuration(tt.in)
		if err != nil || got != tt.want {
			t.Errorf("ParseDuration(%q) = %v, %v; want %v", tt.in, got, err, tt.want)
		}
	}
	bad := []struct{ in, want string }{
		{"", "write a number and a unit"},
		{"5", "write a number and a unit"},
		{"h", "write a number and a unit"},
		{"hh", "write a whole number"},
		{"5y", "the unit must be s, m, h, d or w"},
		{"5ms", "write a whole number"},
		{"5 h", "write a whole number"},
		{"1.5h", "write a whole number"},
		{"-5h", "write a whole number"},
		{"+5h", "write a whole number"},
		{"1h30m", "write a whole number"},
		{"५h", "write a whole number"}, // digits of other scripts are not numbers here
		{"0s", "must be more than zero"},
		{"000d", "must be more than zero"},
		{"3651d", "too long"},
		{"522w", "too long"},
		{"315360001s", "too long"},
		{"99999999999999999999999999999999h", "too long"}, // must not wrap around
		{"9223372036854775807s", "too long"},
		{"18446744073709551617s", "too long"}, // 2^64+1 would read as 1 if it wrapped
	}
	for _, tt := range bad {
		got, err := ParseDuration(tt.in)
		if err == nil || !strings.Contains(err.Error(), tt.want) || got != 0 {
			t.Errorf("ParseDuration(%q) = %v, %v; want 0 and an error containing %q", tt.in, got, err, tt.want)
		}
	}
}

func TestFormatDuration(t *testing.T) {
	day := 24 * time.Hour
	tests := []struct {
		d    time.Duration
		want string
	}{
		{0, "0s"},
		{time.Second, "1s"},
		{90 * time.Second, "90s"},
		{60 * time.Second, "1m"},
		{90 * time.Minute, "90m"},
		{time.Hour, "1h"},
		{26 * time.Hour, "26h"},
		{day, "1d"},
		{48 * time.Hour, "2d"},
		{30 * day, "30d"},
		{7 * day, "1w"},
		{14 * day, "2w"},
		{maxDuration, "3650d"},
		{1500 * time.Millisecond, "1s"},
		{day + time.Second, "86401s"},
	}
	for _, tt := range tests {
		if got := FormatDuration(tt.d); got != tt.want {
			t.Errorf("FormatDuration(%v) = %s, want %s", tt.d, got, tt.want)
		}
	}
}

func TestDurationRoundTrip(t *testing.T) {
	units := []time.Duration{time.Second, time.Minute, time.Hour, 24 * time.Hour, 7 * 24 * time.Hour}
	for _, u := range units {
		for _, n := range []int64{1, 2, 7, 24, 59, 60, 61, 90, 168, 365, 1000} {
			d := time.Duration(n) * u
			if d > maxDuration {
				continue
			}
			s := FormatDuration(d)
			got, err := ParseDuration(s)
			if err != nil || got != d {
				t.Errorf("ParseDuration(FormatDuration(%v) = %s) = %v, %v", d, s, got, err)
			}
		}
	}
}

func TestCleanClaim(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"plain", "the API listens on 8080", "the API listens on 8080"},
		{"trimmed", "  \t spaced out \n", "spaced out"},
		{"inner whitespace collapsed", "a  \t b\n\nc", "a b c"},
		{"newlines joined", "line one\r\nline two", "line one line two"},
		{"control characters dropped", "a\x00b\x1b[31mc\x7fd\u009be", "ab[31mcde"},
		{"unicode line breaks become a space", "a\u2028b\u2029c", "a b c"},
		{"byte order mark", "\ufeffhello\ufeff", "hello"},
		{"other unicode spaces", "a\u00a0b\u3000c\u2003d", "a b c d"},
		{"next line is a space", "a\u0085b", "a b"},
		{"kept as written", "naïve café ✓ 日本語", "naïve café ✓ 日本語"},
		{"dropped characters do not leave a space", "a \x00 b", "a b"},
	}
	for _, tt := range tests {
		got, err := CleanClaim(tt.in)
		if err != nil || got != tt.want {
			t.Errorf("%s: CleanClaim(%q) = %q, %v; want %q", tt.name, tt.in, got, err, tt.want)
		}
	}
	for _, empty := range []string{"", "   ", "\n\t\r", "\x00\x1b", "\u2028\ufeff"} {
		if got, err := CleanClaim(empty); err == nil || !strings.Contains(err.Error(), "the claim is empty") {
			t.Errorf("CleanClaim(%q) = %q, %v; want an empty-claim error", empty, got, err)
		}
	}
}

// The limit counts characters, not bytes: 400 two- or three-byte letters fit.
func TestCleanClaimLength(t *testing.T) {
	for _, letter := range []string{"a", "é", "日", "🙂"} {
		fits := strings.Repeat(letter, MaxClaim)
		if got, err := CleanClaim(fits); err != nil || got != fits {
			t.Errorf("a claim of %d × %q was refused: %v", MaxClaim, letter, err)
		}
		_, err := CleanClaim(fits + letter)
		if err == nil || !strings.Contains(err.Error(), "401 characters") {
			t.Errorf("a claim of %d × %q: error = %v, want it to say 401 characters", MaxClaim+1, letter, err)
		}
	}
	// Whitespace that is collapsed away does not count.
	if _, err := CleanClaim(strings.Repeat("a", MaxClaim-1) + strings.Repeat(" ", 50) + "b"); err == nil {
		t.Error("401 characters after collapsing were accepted")
	}
	if _, err := CleanClaim(strings.Repeat(" ", 500) + "a" + strings.Repeat("\n", 500)); err != nil {
		t.Errorf("padding counted towards the limit: %v", err)
	}
}

func TestCleanLine(t *testing.T) {
	tests := []struct {
		name, in string
		max      int
		want     string
	}{
		{"short", "hello", 10, "hello"},
		{"exactly the limit", "0123456789", 10, "0123456789"},
		{"one over", "0123456789a", 10, "012345678…"},
		{"limit of one", "hello", 1, "…"},
		{"cut lands after a space", "abcd efgh", 6, "abcd…"},
		{"runes not bytes", "ééééé", 5, "ééééé"},
		{"runes cut whole", "日本語日本語", 4, "日本語…"},
		{"one line", "a\nb\r\nc\td", 20, "a b c d"},
		{"controls dropped", "a\x1b[2Jb\x00c\u2028d\ufeffe", 20, "a[2Jbc de"},
		{"empty", "", 10, ""},
		{"only whitespace", " \n\t ", 10, ""},
	}
	for _, tt := range tests {
		got := CleanLine(tt.in, tt.max)
		if got != tt.want {
			t.Errorf("%s: CleanLine(%q, %d) = %q, want %q", tt.name, tt.in, tt.max, got, tt.want)
		}
	}
}

// Whatever goes in, what comes out is one line, within the limit, valid text,
// and says so with an ellipsis when it was cut.
func TestCleanLineNeverExceeds(t *testing.T) {
	inputs := []string{
		strings.Repeat("a", 1000),
		strings.Repeat("word ", 300),
		strings.Repeat("日本", 400),
		strings.Repeat("a\n", 500),
		strings.Repeat(" ", 1000) + "x",
		strings.Repeat("🙂 ", 300),
		strings.Repeat("\x1b[31m", 200),
		"a" + strings.Repeat(" ", 400) + "b",
	}
	for _, in := range inputs {
		whole := oneLine(in)
		for _, max := range []int{1, 2, 3, 60, 120, MaxDetail, MaxClaim} {
			got := CleanLine(in, max)
			n := utf8.RuneCountInString(got)
			if n > max {
				t.Errorf("CleanLine(%.20q…, %d) is %d characters", in, max, n)
			}
			if !utf8.ValidString(got) {
				t.Errorf("CleanLine(%.20q…, %d) is not valid UTF-8", in, max)
			}
			if strings.ContainsAny(got, "\n\r\t\x00\x1b\u2028\u2029\ufeff") {
				t.Errorf("CleanLine(%.20q…, %d) = %q still has a control character", in, max, got)
			}
			if cut := utf8.RuneCountInString(whole) > max; cut != strings.HasSuffix(got, "…") {
				t.Errorf("CleanLine(%.20q…, %d) = %q: cut = %v but the ellipsis says otherwise", in, max, got, cut)
			}
		}
	}
}

func TestCleanTags(t *testing.T) {
	good := []struct {
		name string
		in   []string
		want string // joined with spaces
	}{
		{"nothing", nil, ""},
		{"plain", []string{"deploy", "db"}, "deploy db"},
		{"lowercased", []string{"Deploy", "DB"}, "deploy db"},
		{"hash dropped", []string{"#deploy"}, "deploy"},
		{"split on commas and spaces", []string{"a,b c", " d ,, e\tf\ng"}, "a b c d e f g"},
		{"duplicates", []string{"a", "A", "#a", "b,a"}, "a b"},
		{"allowed punctuation", []string{"api/v2", "go-1.24", "snake_case"}, "api/v2 go-1.24 snake_case"},
		{"empty pieces", []string{"", " ", ",", "#"}, ""},
		{"longest tag", []string{strings.Repeat("a", MaxTag)}, strings.Repeat("a", MaxTag)},
		{"most tags", []string{"1 2 3 4 5 6 7 8"}, "1 2 3 4 5 6 7 8"},
		{"duplicates do not count", []string{"1 2 3 4 5 6 7 8 8 7 6 5 4 3 2 1"}, "1 2 3 4 5 6 7 8"},
	}
	for _, tt := range good {
		got, err := CleanTags(tt.in)
		if err != nil || strings.Join(got, " ") != tt.want {
			t.Errorf("%s: CleanTags(%q) = %q, %v; want %q", tt.name, tt.in, got, err, tt.want)
		}
	}
	bad := []struct {
		name string
		in   []string
		want string
	}{
		{"too long", []string{strings.Repeat("a", MaxTag+1)}, "longer than 32"},
		{"too many", []string{"1 2 3 4 5 6 7 8 9"}, "9 tags; the limit is 8"},
		{"bad punctuation", []string{"a!b"}, "use letters, digits"},
		{"quote", []string{`a"b`}, "use letters, digits"},
		{"non-ascii", []string{"café"}, "use letters, digits"},
		{"control character", []string{"a\x1bb"}, "use letters, digits"},
		{"markdown", []string{"[x](y)"}, "use letters, digits"},
		{"second hash", []string{"##a"}, "use letters, digits"},
	}
	for _, tt := range bad {
		got, err := CleanTags(tt.in)
		if err == nil || got != nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: CleanTags(%q) = %q, %v; want an error containing %q", tt.name, tt.in, got, err, tt.want)
		}
	}
}

func TestValidID(t *testing.T) {
	for _, id := range []string{"f-234567", "f-abcdef", "f-zzzzzz", "f-9wxyz2"} {
		if !ValidID(id) {
			t.Errorf("ValidID(%q) = false", id)
		}
	}
	for _, id := range []string{
		"", "f-", "f-23456", "f-2345678", "234567", "g-234567", "F-234567", "f_234567",
		"f-23456o", "f-23456l", "f-23456i", "f-23456u", "f-234560", "f-234561",
		"f-ABCDEF", "f-23 567", "f-2345é", "f-23456\n", " f-234567", "f-234567 ", "f-../../",
	} {
		if ValidID(id) {
			t.Errorf("ValidID(%q) = true", id)
		}
	}
}

func TestNewID(t *testing.T) {
	if got := NewID([]byte{0, 1, 2, 3, 4, 5}); got != "f-234567" {
		t.Errorf("NewID(0..5) = %s, want f-234567", got)
	}
	if got := NewID([]byte{29, 30, 255, 0, 0, 0, 99, 99}); got != "f-z2h222" {
		t.Errorf("NewID = %s, want f-z2h222", got)
	}
	// Every byte value gives a valid id.
	for b := 0; b < 256; b++ {
		id := NewID([]byte{byte(b), byte(b), byte(b), byte(b), byte(b), byte(b)})
		if !ValidID(id) {
			t.Fatalf("NewID with byte %d = %q, which is not a valid id", b, id)
		}
	}
}
