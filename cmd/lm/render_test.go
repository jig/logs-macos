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
	cases := []time.Duration{
		0, time.Second, 5 * time.Second, 59 * time.Second,
		time.Minute, 90 * time.Second, 15 * time.Minute, 59*time.Minute + 59*time.Second,
		time.Hour, 23 * time.Hour, 24 * time.Hour, 7 * 24 * time.Hour, 99 * 24 * time.Hour,
	}
	for _, d := range cases {
		s := relTimeStrFixed(d)
		if len(s) != 5 {
			t.Errorf("relTimeStrFixed(%v) = %q (len=%d), want width 5", d, s, len(s))
		}
	}
	// Spot checks.
	if got := relTimeStrFixed(time.Minute); got != " 1m00" {
		t.Errorf("1m -> %q, want \" 1m00\"", got)
	}
	if got := relTimeStrFixed(59*time.Minute + 59*time.Second); got != "59m59" {
		t.Errorf("59m59s -> %q, want \"59m59\"", got)
	}
	if got := relTimeStrFixed(time.Hour); got != " 1h00" {
		t.Errorf("1h -> %q, want \" 1h00\"", got)
	}
}

func TestColorizeCompressedStripsTimeAndBraces(t *testing.T) {
	raw := `{"ts":"2026-05-18T10:00:00Z","level":"info","msg":"hello world"}`
	got := stripANSI(colorizeCompressed(raw))
	want := `level="info" msg="hello world"`
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
// flattening only applies to the outermost pairs.
func TestColorizeCompressedKeepsNestedStructure(t *testing.T) {
	raw := `{"level":"info","data":{"a":1,"b":2},"tags":["x","y"]}`
	got := stripANSI(colorizeCompressed(raw))
	want := `level="info" data={"a": 1, "b": 2} tags=["x", "y"]`
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
