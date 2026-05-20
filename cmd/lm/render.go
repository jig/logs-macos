package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
)

const (
	fadeBoostDur = 60 * time.Second // logs < this get a brightness boost
	fadeMaxDur   = 5 * time.Minute
	maxAlpha     = 1.3 // alpha at age=0; boost fades linearly to 1.0 at fadeBoostDur
)

// parseLogTime extracts a timestamp from common structured-log fields.
// Returns (time, rawJSONValue, isStringValue, ok).
// rawJSONValue is the original unquoted string or the raw number token —
// used later to locate and replace the rendered value in the cached line.
func parseLogTime(raw string) (time.Time, string, bool, bool) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &obj); err != nil {
		return time.Time{}, "", false, false
	}
	for _, key := range []string{"ts", "time", "timestamp", "t", "@timestamp"} {
		v, ok := obj[key]
		if !ok {
			continue
		}
		// String: RFC3339 / ISO8601
		var s string
		if json.Unmarshal(v, &s) == nil {
			for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
				if t, err := time.Parse(layout, s); err == nil {
					return t.UTC(), s, true, true
				}
			}
		}
		// Number: Unix timestamp (seconds, possibly fractional)
		var f float64
		if json.Unmarshal(v, &f) == nil && f > 1e8 {
			sec := int64(f)
			nsec := int64((f - float64(sec)) * 1e9)
			return time.Unix(sec, nsec).UTC(), strings.TrimSpace(string(v)), false, true
		}
	}
	return time.Time{}, "", false, false
}

// ageAlpha returns maxAlpha for age=0, decreasing linearly to 1.0 at
// fadeBoostDur, then from 1.0 to 0.0 at fadeMaxDur.
func ageAlpha(logTime, now time.Time) float64 {
	age := now.Sub(logTime)
	if age <= 0 {
		return maxAlpha
	}
	if age <= fadeBoostDur {
		t := float64(age) / float64(fadeBoostDur)
		return maxAlpha - t*(maxAlpha-1.0)
	}
	if age >= fadeMaxDur {
		return 0.0
	}
	span := float64(fadeMaxDur - fadeBoostDur)
	return 1.0 - float64(age-fadeBoostDur)/span
}

// relTimeStr formats a duration as a compact human-readable age string.
func relTimeStr(age time.Duration) string {
	if age < 0 {
		age = 0
	}
	s := int(age.Seconds())
	switch {
	case s < 60:
		return fmt.Sprintf("%ds", s)
	case s < 3600:
		if s%60 == 0 {
			return fmt.Sprintf("%dm", s/60)
		}
		return fmt.Sprintf("%dm%ds", s/60, s%60)
	case s < 86400:
		if (s%3600)/60 == 0 {
			return fmt.Sprintf("%dh", s/3600)
		}
		return fmt.Sprintf("%dh%dm", s/3600, (s%3600)/60)
	default:
		if (s%86400)/3600 == 0 {
			return fmt.Sprintf("%dd", s/86400)
		}
		return fmt.Sprintf("%dd%dh", s/86400, (s%86400)/3600)
	}
}

// relTimeStrFixed formats a duration with a constant 5-char width so the
// time column doesn't jitter as the value crosses unit boundaries:
//
//	" 0m05", " 1m05", "15m05", " 1h05", "23h45", " 1d12", "99d23"
func relTimeStrFixed(age time.Duration) string {
	if age < 0 {
		age = 0
	}
	s := int(age.Seconds())
	switch {
	case s < 3600:
		return fmt.Sprintf("%2dm%02d", s/60, s%60)
	case s < 86400:
		return fmt.Sprintf("%2dh%02d", s/3600, (s%3600)/60)
	default:
		return fmt.Sprintf("%2dd%02d", s/86400, (s%86400)/3600)
	}
}

// timeColWidth is the fixed visible width of the time column rendered by
// relTimeStrFixed (used to pad timeless lines).
const timeColWidth = 5

func isTimeKey(k string) bool {
	switch k {
	case "ts", "time", "timestamp", "t", "@timestamp":
		return true
	}
	return false
}

// skipValue advances the decoder past one full JSON value (used to discard
// the time field when rendering compressed output).
func skipValue(dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	d, ok := tok.(json.Delim)
	if !ok || (d != '{' && d != '[') {
		return nil
	}
	depth := 1
	for depth > 0 {
		t, err := dec.Token()
		if err != nil {
			return err
		}
		if dd, ok := t.(json.Delim); ok {
			switch dd {
			case '{', '[':
				depth++
			case '}', ']':
				depth--
			}
		}
	}
	return nil
}

// isSimpleLetterString reports whether s is non-empty and made only of ASCII
// letters [a-zA-Z]. Such values can be rendered unquoted in compressed mode
// — the green color already marks them as strings.
func isSimpleLetterString(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')) {
			return false
		}
	}
	return true
}

// renderCompressedValue reads one JSON value from dec and writes it to buf
// using compressed-mode rules: simple letter-only string values drop their
// surrounding quotes. Nested objects and arrays delegate to the regular
// compact tokenizer so they keep braces, brackets, and commas.
func renderCompressedValue(dec *json.Decoder, buf *bytes.Buffer) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	switch v := tok.(type) {
	case string:
		if isSimpleLetterString(v) {
			buf.WriteString(styleStr.Render(v))
		} else {
			buf.WriteString(styleStr.Render(fmt.Sprintf("%q", v)))
		}
	case json.Number:
		buf.WriteString(styleNum.Render(v.String()))
	case bool:
		buf.WriteString(styleBool.Render(fmt.Sprintf("%t", v)))
	case nil:
		buf.WriteString(styleNull.Render("null"))
	case json.Delim:
		switch v {
		case '{':
			return tokenizeObjectC(dec, buf)
		case '[':
			return tokenizeArrayC(dec, buf)
		}
	}
	return nil
}

// colorizeCompressed renders a top-level JSON object as `key=value, key=value`
// with unquoted keys, no surrounding braces, and time-like fields stripped
// (they appear in the time column instead). Falls back to the raw text when
// the input isn't a JSON object.
func colorizeCompressed(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	tok, err := dec.Token()
	if err != nil {
		return raw
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return raw
	}
	var buf bytes.Buffer
	first := true
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return raw
		}
		key, ok := keyTok.(string)
		if !ok {
			return raw
		}
		if isTimeKey(key) {
			if err := skipValue(dec); err != nil {
				return raw
			}
			continue
		}
		if !first {
			buf.WriteByte(' ')
		}
		first = false
		buf.WriteString(styleKey.Render(key))
		buf.WriteString(stylePunct.Render("="))
		if err := renderCompressedValue(dec, &buf); err != nil {
			return raw
		}
	}
	if _, err := dec.Token(); err != nil {
		return raw
	}
	return buf.String()
}

// replaceRenderedTime substitutes the cached rendered timestamp value in line
// with the current relative age string. Works by matching the exact ANSI-colored
// token that was written during initial JSON rendering.
func replaceRenderedTime(line, rawTimeVal string, rawTimeIsStr bool, logTime, now time.Time) string {
	if rawTimeVal == "" {
		return line
	}
	var oldR string
	if rawTimeIsStr {
		oldR = styleStr.Render(fmt.Sprintf("%q", rawTimeVal))
	} else {
		oldR = styleNum.Render(rawTimeVal)
	}
	newR := styleTime.Render(relTimeStr(now.Sub(logTime)))
	return strings.Replace(line, oldR, newR, 1)
}

var (
	styleKey   = lipgloss.NewStyle().Foreground(lipgloss.Color("#61AFEF"))
	styleStr   = lipgloss.NewStyle().Foreground(lipgloss.Color("#98C379"))
	styleNum   = lipgloss.NewStyle().Foreground(lipgloss.Color("#D19A66"))
	styleBool  = lipgloss.NewStyle().Foreground(lipgloss.Color("#C678DD"))
	styleNull  = lipgloss.NewStyle().Foreground(lipgloss.Color("#5C6370"))
	stylePunct = lipgloss.NewStyle().Foreground(lipgloss.Color("#ABB2BF"))
	styleTime  = lipgloss.NewStyle().Foreground(lipgloss.Color("#56B6C2")) // cyan — relative age
)

// ── multiline pretty-print ────────────────────────────────────────────────────

func colorizeJSON(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	var buf bytes.Buffer
	if err := tokenizeValue(dec, &buf, 0); err != nil {
		return raw
	}
	return buf.String()
}

func tokenizeValue(dec *json.Decoder, buf *bytes.Buffer, indent int) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	switch v := tok.(type) {
	case json.Delim:
		switch v {
		case '{':
			return tokenizeObject(dec, buf, indent)
		case '[':
			return tokenizeArray(dec, buf, indent)
		default:
			buf.WriteString(stylePunct.Render(string(v)))
		}
	case string:
		buf.WriteString(styleStr.Render(fmt.Sprintf("%q", v)))
	case json.Number:
		buf.WriteString(styleNum.Render(v.String()))
	case bool:
		buf.WriteString(styleBool.Render(fmt.Sprintf("%t", v)))
	case nil:
		buf.WriteString(styleNull.Render("null"))
	}
	return nil
}

func tokenizeObject(dec *json.Decoder, buf *bytes.Buffer, indent int) error {
	buf.WriteString(stylePunct.Render("{"))
	pad := strings.Repeat("  ", indent+1)
	first := true
	for dec.More() {
		if !first {
			buf.WriteString(stylePunct.Render(","))
		}
		first = false
		buf.WriteByte('\n')
		buf.WriteString(pad)
		keyTok, err := dec.Token()
		if err != nil {
			return err
		}
		key, ok := keyTok.(string)
		if !ok {
			return fmt.Errorf("expected string key")
		}
		buf.WriteString(styleKey.Render(fmt.Sprintf("%q", key)))
		buf.WriteString(stylePunct.Render(": "))
		if err := tokenizeValue(dec, buf, indent+1); err != nil {
			return err
		}
	}
	if _, err := dec.Token(); err != nil {
		return err
	}
	if !first {
		buf.WriteByte('\n')
		buf.WriteString(strings.Repeat("  ", indent))
	}
	buf.WriteString(stylePunct.Render("}"))
	return nil
}

func tokenizeArray(dec *json.Decoder, buf *bytes.Buffer, indent int) error {
	buf.WriteString(stylePunct.Render("["))
	pad := strings.Repeat("  ", indent+1)
	first := true
	for dec.More() {
		if !first {
			buf.WriteString(stylePunct.Render(","))
		}
		first = false
		buf.WriteByte('\n')
		buf.WriteString(pad)
		if err := tokenizeValue(dec, buf, indent+1); err != nil {
			return err
		}
	}
	if _, err := dec.Token(); err != nil {
		return err
	}
	if !first {
		buf.WriteByte('\n')
		buf.WriteString(strings.Repeat("  ", indent))
	}
	buf.WriteString(stylePunct.Render("]"))
	return nil
}

// ── compact single-line ───────────────────────────────────────────────────────

func colorizeJSONCompact(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	var buf bytes.Buffer
	if err := tokenizeValueC(dec, &buf); err != nil {
		return raw
	}
	return buf.String()
}

func tokenizeValueC(dec *json.Decoder, buf *bytes.Buffer) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	switch v := tok.(type) {
	case json.Delim:
		switch v {
		case '{':
			return tokenizeObjectC(dec, buf)
		case '[':
			return tokenizeArrayC(dec, buf)
		default:
			buf.WriteString(stylePunct.Render(string(v)))
		}
	case string:
		buf.WriteString(styleStr.Render(fmt.Sprintf("%q", v)))
	case json.Number:
		buf.WriteString(styleNum.Render(v.String()))
	case bool:
		buf.WriteString(styleBool.Render(fmt.Sprintf("%t", v)))
	case nil:
		buf.WriteString(styleNull.Render("null"))
	}
	return nil
}

func tokenizeObjectC(dec *json.Decoder, buf *bytes.Buffer) error {
	buf.WriteString(stylePunct.Render("{"))
	first := true
	for dec.More() {
		if !first {
			buf.WriteString(stylePunct.Render(", "))
		}
		first = false
		keyTok, err := dec.Token()
		if err != nil {
			return err
		}
		key, ok := keyTok.(string)
		if !ok {
			return fmt.Errorf("expected string key")
		}
		buf.WriteString(styleKey.Render(fmt.Sprintf("%q", key)))
		buf.WriteString(stylePunct.Render(": "))
		if err := tokenizeValueC(dec, buf); err != nil {
			return err
		}
	}
	if _, err := dec.Token(); err != nil {
		return err
	}
	buf.WriteString(stylePunct.Render("}"))
	return nil
}

func tokenizeArrayC(dec *json.Decoder, buf *bytes.Buffer) error {
	buf.WriteString(stylePunct.Render("["))
	first := true
	for dec.More() {
		if !first {
			buf.WriteString(stylePunct.Render(", "))
		}
		first = false
		if err := tokenizeValueC(dec, buf); err != nil {
			return err
		}
	}
	if _, err := dec.Token(); err != nil {
		return err
	}
	buf.WriteString(stylePunct.Render("]"))
	return nil
}

// ── ANSI utilities ────────────────────────────────────────────────────────────

var (
	ansiRE    = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)
	ansiRGBRE = regexp.MustCompile(`\x1b\[38;2;(\d+);(\d+);(\d+)m`)
)

func visWidth(s string) int {
	return runewidth.StringWidth(ansiRE.ReplaceAllString(s, ""))
}

// hClip returns the visible portion of line in the range [hOffset, hOffset+viewWidth).
// It accumulates ANSI SGR sequences seen before the first visible character and
// re-emits them at the start of the output so colors are preserved.
func hClip(line string, hOffset, viewWidth int) string {
	if hOffset <= 0 && viewWidth <= 0 {
		return line
	}
	if hOffset < 0 {
		hOffset = 0
	}

	var pre strings.Builder // SGR sequences before visible region
	var out strings.Builder
	vis := 0
	started := false

	for i := 0; i < len(line); {
		// CSI: ESC [ <params> <final>
		if line[i] == '\033' && i+1 < len(line) && line[i+1] == '[' {
			j := i + 2
			for j < len(line) && !(line[j] >= 0x40 && line[j] <= 0x7E) {
				j++
			}
			if j < len(line) {
				j++
			}
			if !started {
				pre.WriteString(line[i:j])
			} else {
				out.WriteString(line[i:j])
			}
			i = j
			continue
		}

		r, size := utf8.DecodeRuneInString(line[i:])
		w := runewidth.RuneWidth(r)

		if vis >= hOffset {
			if !started {
				started = true
				out.WriteString(pre.String())
			}
			if viewWidth > 0 && vis-hOffset+w > viewWidth {
				break
			}
			out.WriteString(line[i : i+size])
		}
		vis += w
		i += size
	}

	return out.String()
}

// ── fading & search ───────────────────────────────────────────────────────────

// fadeString adjusts every true-color foreground RGB in line based on alpha:
//   alpha > 1.0 → boost (multiply toward white, clamp 255)
//   alpha = 1.0 → unchanged
//   alpha < 1.0 → blend toward pale warm brown
func fadeString(line string, alpha float64) string {
	if alpha > 0.98 && alpha < 1.02 {
		return line
	}
	const tR, tG, tB = 160.0, 120.0, 75.0 // fade target: pale sepia
	return ansiRGBRE.ReplaceAllStringFunc(line, func(match string) string {
		parts := strings.SplitN(match[7:len(match)-1], ";", 3)
		if len(parts) != 3 {
			return match
		}
		r, er := strconv.Atoi(parts[0])
		g, eg := strconv.Atoi(parts[1])
		b, eb := strconv.Atoi(parts[2])
		if er != nil || eg != nil || eb != nil {
			return match
		}
		var nr, ng, nb int
		if alpha > 1.0 {
			nr = min(255, int(float64(r)*alpha))
			ng = min(255, int(float64(g)*alpha))
			nb = min(255, int(float64(b)*alpha))
		} else {
			nr = int(float64(r)*alpha + tR*(1-alpha))
			ng = int(float64(g)*alpha + tG*(1-alpha))
			nb = int(float64(b)*alpha + tB*(1-alpha))
		}
		return fmt.Sprintf("\033[38;2;%d;%d;%dm", nr, ng, nb)
	})
}

func highlightSearch(rendered, raw, query string) string {
	if query == "" || !strings.Contains(strings.ToLower(raw), strings.ToLower(query)) {
		return rendered
	}
	lower := strings.ToLower(rendered)
	lq := strings.ToLower(query)
	var sb strings.Builder
	pos := 0
	for {
		idx := strings.Index(lower[pos:], lq)
		if idx < 0 {
			sb.WriteString(rendered[pos:])
			break
		}
		abs := pos + idx
		sb.WriteString(rendered[pos:abs])
		sb.WriteString("\033[7m")
		sb.WriteString(rendered[abs : abs+len(query)])
		sb.WriteString("\033[27m")
		pos = abs + len(query)
	}
	return sb.String()
}
