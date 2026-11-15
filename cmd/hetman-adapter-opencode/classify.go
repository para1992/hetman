package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"hetman/internal/adapter"
)

var (
	// A provider's usage, rate or plan limit, as the client and the
	// providers word it.
	limitText = regexp.MustCompile(`(?i)usage limit|limit (?:has been )?reached|rate.limit|too many requests|quota exceeded|exceeded your (?:current )?quota|FreeUsageLimitError|GoUsageLimitError|free usage exceeded`)
	// No connection to the provider.
	offlineText = regexp.MustCompile(`(?i)unable to connect|cannot connect to api|fetch failed|failed to fetch|ECONNREFUSED|ENOTFOUND|EAI_AGAIN|ETIMEDOUT|ENETUNREACH|EHOSTUNREACH|getaddrinfo|socket connection was closed|network error`)
	// Missing or refused credentials.
	authText = regexp.MustCompile(`(?i)no api key|invalid api key|incorrect api key|api key (?:is )?(?:missing|invalid|not valid)|invalid x-api-key|unauthorized|authentication (?:failed|error)|providers login|not logged in`)
	// A model or provider the client does not know.
	modelText = regexp.MustCompile(`ProviderModelNotFoundError|Model not found|ProviderNotFound`)
	// The client's own argument checks print its usage or one of these
	// messages before it starts.
	argsText = regexp.MustCompile(`(?m)^opencode run \[message\.\.\]|You must provide a message|requires --continue or --session|Unknown argument|Invalid values?:`)
	// The reset time of a limit as the client words it: "It will reset in
	// 2 days 3 hours", "in 1 hour 5 minutes", "in less than a minute".
	resetText = regexp.MustCompile(`(?i)reset in ((?:\d+ (?:day|hour|minute)s? ?)+|less than a minute)`)
	resetPart = regexp.MustCompile(`(\d+) (day|hour|minute)`)
	// A field of the client's error log: error.error="...", error="..." or
	// cause="...".
	logField = regexp.MustCompile(`\b(error\.error|error|cause)="((?:[^"\\]|\\.)*)"`)
	// The session in the client's log: "message=created id=ses_..." for a
	// new one, "session.id=ses_..." on the lines about a session's work.
	sessionLog = regexp.MustCompile(`(?:message=created id=|session\.id=)(ses_[A-Za-z0-9]+)`)
	ansi       = regexp.MustCompile("\x1b\\[[0-9;]*[A-Za-z]")
)

// unknownMessage is what the client's event says for an error it names
// only in its log.
const unknownMessage = "Unexpected server error"

func stripANSI(text string) string { return ansi.ReplaceAllString(text, "") }

// missingSession reports whether the client said it found no session with
// the id it was given.
func missingSession(stderr string) bool {
	return strings.Contains(stripANSI(stderr), "Session not found")
}

// routineLog reports whether a line is one of the client's informational
// log lines.
func routineLog(line string) bool {
	return strings.Contains(line, " level=INFO ") || strings.Contains(line, " level=DEBUG ")
}

// logSession returns the session id a line of the client's log names, or "".
func logSession(line string) string {
	if !strings.HasPrefix(line, "timestamp=") {
		return ""
	}
	if g := sessionLog.FindStringSubmatch(line); g != nil {
		return g[1]
	}
	return ""
}

// logError returns the error a line of the client's error log names, or ""
// for any other line.
func logError(line string) string {
	if !strings.Contains(line, "level=ERROR") {
		return ""
	}
	found := map[string]string{}
	for _, g := range logField.FindAllStringSubmatch(line, -1) {
		if _, ok := found[g[1]]; !ok {
			v, err := strconv.Unquote(`"` + g[2] + `"`)
			if err != nil {
				v = g[2]
			}
			found[g[1]] = firstLineOf(v)
		}
	}
	for _, k := range []string{"error.error", "error", "cause"} {
		if v := found[k]; v != "" {
			return v
		}
	}
	return ""
}

func firstLineOf(text string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
	return strings.TrimSpace(line)
}

// lastLogError returns the last error the client's error log names.
func lastLogError(stderr string) string {
	last := ""
	for _, line := range strings.Split(stderr, "\n") {
		if m := logError(line); m != "" {
			last = m
		}
	}
	return last
}

// classify turns a run that ended without an answer into an error message.
func (s *stream) classify(exit int, stderr string) adapter.Message {
	text, status, name := "", 0, ""
	for _, e := range s.errors {
		if m := errText(e); m != "" && !strings.HasPrefix(m, unknownMessage) {
			text = m
		}
		if e.Data.StatusCode != 0 {
			status = e.Data.StatusCode
		}
		name = e.Name
	}
	if text == "" {
		text = lastLogError(stderr)
	}
	if text == "" {
		text = lastLine(stripANSI(stderr))
	}
	if text == "" && len(s.errors) > 0 {
		text = errText(s.errors[len(s.errors)-1])
	}
	if text == "" {
		text = fmt.Sprintf("the client exited with code %d", exit)
	}
	text = clip(oneLine(text), 500)
	all := text + "\n" + stderr

	switch {
	case status == 429 || limitText.MatchString(text):
		return s.limit(text, all)
	case name == "ProviderAuthError" || status == 401 || status == 403 || authText.MatchString(text):
		return failure(adapter.ErrPermanent, text)
	case modelText.MatchString(all):
		return failure(adapter.ErrPermanent, text)
	case offlineText.MatchString(text):
		return failure(adapter.ErrOffline, text)
	case s.session == "" && exit != 0 && argsText.MatchString(stderr):
		if strings.HasPrefix(text, "--") || strings.HasPrefix(text, "[") {
			text = "the client rejected its arguments and printed its usage"
		}
		return failure(adapter.ErrPermanent, text)
	case limitText.MatchString(all):
		return s.limit(text, all)
	}
	return failure(adapter.ErrTransient, text)
}

// limit makes a limit error, with the reset time when the provider gave
// one that is still ahead: a retry-after header of an API error, or the
// client's "It will reset in ..." text.
func (s *stream) limit(text, all string) adapter.Message {
	m := failure(adapter.ErrLimit, text)
	now := time.Now()
	var at time.Time
	for _, e := range s.errors {
		if t := retryAfter(e.Data.ResponseHeaders, now); !t.IsZero() {
			at = t
		}
	}
	if at.IsZero() {
		if g := resetText.FindStringSubmatch(all); g != nil {
			d := time.Minute
			for _, p := range resetPart.FindAllStringSubmatch(g[1], -1) {
				n, _ := strconv.Atoi(p[1])
				switch p[2] {
				case "day":
					d += time.Duration(n) * 24 * time.Hour
				case "hour":
					d += time.Duration(n) * time.Hour
				case "minute":
					d += time.Duration(n) * time.Minute
				}
			}
			at = now.Add(d)
		}
	}
	if at.After(now) {
		m.RetryAt = at.UTC().Format(time.RFC3339)
	}
	return m
}

// retryAfter reads the retry-after-ms or retry-after header of a response.
func retryAfter(h map[string]string, now time.Time) time.Time {
	get := func(name string) string {
		for k, v := range h {
			if strings.EqualFold(k, name) {
				return strings.TrimSpace(v)
			}
		}
		return ""
	}
	if v := get("retry-after-ms"); v != "" {
		if ms, err := strconv.ParseFloat(v, 64); err == nil && ms > 0 {
			return now.Add(time.Duration(ms) * time.Millisecond)
		}
	}
	if v := get("retry-after"); v != "" {
		if sec, err := strconv.ParseFloat(v, 64); err == nil && sec > 0 {
			return now.Add(time.Duration(sec * float64(time.Second)))
		}
		if t, err := http.ParseTime(v); err == nil {
			return t
		}
	}
	return time.Time{}
}

func lastLine(text string) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// extractObject finds the JSON object in a text answer: the last complete
// object in it, so that an answer that quotes an example before its final
// object gives the final one.
func extractObject(text string) (json.RawMessage, bool) {
	var last json.RawMessage
	for i := 0; i < len(text); i++ {
		if text[i] != '{' {
			continue
		}
		dec := json.NewDecoder(strings.NewReader(text[i:]))
		var raw json.RawMessage
		if dec.Decode(&raw) != nil {
			continue
		}
		if isObject(raw) {
			last = raw
			i += int(dec.InputOffset()) - 1
		}
	}
	return last, last != nil
}
