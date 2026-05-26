package main

import (
	"regexp"
	"testing"
	"time"
)

func stripANSI(s string) string {
	re := regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)
	return re.ReplaceAllString(s, "")
}

func TestRelTimeStrFixedWidth(t *testing.T) {
	cases := []struct {
		dur      time.Duration
		expected string
	}{
		{0, " 0m00"},
		{time.Minute, " 1m00"},
		{59*time.Minute + 59*time.Second, "59m59"},
		{time.Hour, " 1h00"},
		{23 * time.Hour, "23h00"},
		{24 * time.Hour, " 1d00h"},
		{4*24*time.Hour + 22*time.Hour, " 4d22h"},
		{99*24*time.Hour + 23*time.Hour, "99d23h"},
	}
	for _, tc := range cases {
		got := relTimeStrFixed(tc.dur)
		if got != tc.expected {
			t.Errorf("relTimeStrFixed(%v) = %q, want %q", tc.dur, got, tc.expected)
		}
	}
}

func TestColorizeCompressedStripsTimeAndBraces(t *testing.T) {
	raw := `{"ts":"2026-05-18T10:00:00Z","level":"info","msg":"hello world"}`
	got := stripANSI(colorizeCompressed(raw))
	// "info" is letters-only → unquoted. "hello world" has a space → quoted.
	want := `level=info msg="hello world"`
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestColorizeCompressedNoBraces(t *testing.T) {
	raw := `{"a":1,"b":true,"c":null}`
	got := stripANSI(colorizeCompressed(raw))
	want := `a=1 b=true c=null`
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// Nested objects and arrays keep their braces and commas — the comma-less
// flattening only applies to the outermost pairs. Nested string values are
// also kept quoted (the unquoting rule is top-level only).
func TestColorizeCompressedKeepsNestedStructure(t *testing.T) {
	raw := `{"level":"info","data":{"a":1,"b":2},"tags":["x","y"]}`
	got := stripANSI(colorizeCompressed(raw))
	want := `level=info data={"a": 1, "b": 2} tags=["x", "y"]`
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// Anything with non-letters (digits, punctuation, spaces, unicode) keeps quotes.
func TestColorizeCompressedQuotesNonLetterValues(t *testing.T) {
	raw := `{"id":"abc-123","path":"/etc/passwd","name":"señor","empty":""}`
	got := stripANSI(colorizeCompressed(raw))
	want := `id="abc-123" path="/etc/passwd" name="señor" empty=""`
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestColorizeCompressedNonObjectFallback(t *testing.T) {
	got := stripANSI(colorizeCompressed("plain text"))
	if got != "plain text" {
		t.Errorf("got %q, want %q", got, "plain text")
	}
}
