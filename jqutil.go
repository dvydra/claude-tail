package main

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// These helpers reproduce the jq fragments the bash version relied on
// (JQ_HELPERS and the per-agent summary extractors), so the Go adapters can be
// checked against the original behavior line-for-line.

var tsFracRe = regexp.MustCompile(`\..*Z$`)

// formatTS reproduces jq's:
//
//	sub("\\..*Z$"; "Z") | fromdateiso8601 | strflocaltime("%Y-%m-%d %H:%M:%S")
//
// It strips a fractional-seconds component before a trailing Z, parses the
// ISO-8601 instant, and renders it in loc. Unlike the jq pipeline (which aborts
// on a malformed timestamp) it returns "" on failure, matching the live path's
// tolerant `.ts // ""`.
func formatTS(iso string, loc *time.Location) string {
	if iso == "" {
		return ""
	}
	s := tsFracRe.ReplaceAllString(iso, "Z")
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		// Fall back to the fractional form for sources that don't match the
		// stripped shape (e.g. an explicit offset rather than Z).
		if t, err = time.Parse(time.RFC3339, iso); err != nil {
			return ""
		}
	}
	return t.In(loc).Format("2006-01-02 15:04:05")
}

// collapseBody reproduces jq's collapse_body($t): when t > 0 and the body has
// more than t lines, keep the first t lines plus an italic "N more lines"
// marker. If the kept head ends inside an unclosed ``` fence, a closing fence is
// appended so the renderer doesn't mis-highlight the marker.
func collapseBody(body string, t int) string {
	if t <= 0 {
		return body
	}
	lines := strings.Split(body, "\n")
	if len(lines) <= t {
		return body
	}
	head := lines[:t]
	fences := 0
	for _, l := range head {
		if strings.HasPrefix(l, "```") {
			fences++
		}
	}
	rest := len(lines) - t
	var b strings.Builder
	b.WriteString(strings.Join(head, "\n"))
	if fences%2 == 1 {
		b.WriteString("\n```")
	}
	b.WriteString("\n\n*… ")
	b.WriteString(strconv.Itoa(rest))
	if rest == 1 {
		b.WriteString(" more line")
	} else {
		b.WriteString(" more lines")
	}
	b.WriteString(" — re-run with --no-collapse to expand*")
	return b.String()
}

// jqToStringRaw reproduces jq's `tostring` for a raw JSON value: strings pass
// through unquoted; everything else becomes compact JSON with source key order
// preserved (via json.Compact, matching jq).
func jqToStringRaw(raw json.RawMessage) string {
	t := bytes.TrimSpace(raw)
	if len(t) == 0 {
		return ""
	}
	if t[0] == '"' {
		var s string
		if json.Unmarshal(t, &s) == nil {
			return s
		}
		return string(t)
	}
	var buf bytes.Buffer
	if json.Compact(&buf, t) == nil {
		return buf.String()
	}
	return string(t)
}

// jqToStringValue is jq's `tostring` for an already-decoded value.
func jqToStringValue(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case string:
		return x
	case bool:
		if x {
			return "true"
		}
		return "false"
	default:
		// Numbers, arrays, objects → compact JSON. (Object key order follows
		// Go's map sort rather than jq's insertion order; only the unknown-tool
		// fallback summary hits this, and it's truncated downstream.)
		b, err := json.Marshal(v)
		if err != nil {
			return ""
		}
		return string(b)
	}
}

// unqRaw reproduces jq's `unq`: tostring | (try fromjson catch .) | tostring.
// It unwraps one level of JSON-string encoding from agy's pre-stringified
// tool-call arguments.
func unqRaw(raw json.RawMessage) string {
	s := jqToStringRaw(raw)
	return unqString(s)
}

func unqString(s string) string {
	var v any
	if err := json.Unmarshal([]byte(s), &v); err == nil {
		return jqToStringValue(v)
	}
	return s
}

func isJSONNull(raw json.RawMessage) bool {
	return string(trimSpace(raw)) == "null"
}

func trimSpace(raw json.RawMessage) json.RawMessage {
	return json.RawMessage(strings.TrimSpace(string(raw)))
}

// firstRaw returns the raw JSON of the first key present and not JSON null,
// reproducing a jq `a // b // c` chain over object fields.
func firstRaw(m map[string]json.RawMessage, keys ...string) (json.RawMessage, bool) {
	for _, k := range keys {
		if raw, ok := m[k]; ok && !isJSONNull(raw) {
			return raw, true
		}
	}
	return nil, false
}
