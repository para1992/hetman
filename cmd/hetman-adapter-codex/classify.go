package main

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"hetman/internal/adapter"
)

// A failed turn carries one message. For an error the server returned it
// is the server's JSON ({"type":"error","error":{"message","type","code"},
// "status":400}); for the client's own errors it is plain text, as are the
// messages of the client's binary that the adapter matches below: the usage
// limit ("You've hit your usage limit. ... try again at Nov 15th, 2026 3:45
// PM."), a request it gave up on after its retries ("exceeded retry limit,
// last status: 429 Too Many Requests"), a connection it could not make
// ("workspace routing discovery failed", "Connection failed: error sending
// request") and a model the account cannot use ("The 'X' model is not
// supported when using Codex with a ChatGPT account.").

// serverError is an error the server returned, as the client passes it on.
type serverError struct {
	raw     string
	Message string
	Type    string
	Code    string
	Status  int
}

// apiErr reads a failure message.
func apiErr(msg string) serverError {
	e := serverError{raw: msg, Message: msg}
	var doc struct {
		Error *struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    any    `json:"code"`
		} `json:"error"`
		Status int `json:"status"`
	}
	if strings.HasPrefix(strings.TrimSpace(msg), "{") && json.Unmarshal([]byte(msg), &doc) == nil && doc.Error != nil {
		e.Message, e.Type, e.Status = doc.Error.Message, doc.Error.Type, doc.Status
		if c, ok := doc.Error.Code.(string); ok {
			e.Code = c
		}
	}
	return e
}

func (e serverError) text() string {
	if e.Message != "" {
		return e.Message
	}
	return e.raw
}

var (
	limitCodes = map[string]bool{"usage_limit_reached": true, "rate_limit_exceeded": true, "insufficient_quota": true,
		"quota_exceeded": true, "workspace_owner_usage_limit_reached": true, "workspace_member_usage_limit_reached": true,
		"workspace_member_credits_depleted": true}
	permanentCodes = map[string]bool{"model_not_found": true, "invalid_api_key": true, "usage_not_included": true,
		"unsupported_model": true, "invalid_request_error": true, "context_length_exceeded": true}

	limitText   = regexp.MustCompile(`(?i)hit your usage limit|usage limit|rate limit|quota exceeded|exceeded retry limit, last status: 429|too many requests|credits depleted`)
	offlineText = regexp.MustCompile(`(?i)error sending request|connection failed|connection refused|connection reset|dns error|failed to lookup address|name or service not known|network is unreachable|no route to host|routing discovery failed|operation timed out|tls handshake`)
	authText    = regexp.MustCompile(`(?i)not logged in|please log in|codex login|unauthorized|authentication|token (?:has )?(?:expired|revoked)|refresh token|login method|upgrade to plus`)
	modelText   = regexp.MustCompile(`(?i)model is not supported|model_not_found|does not exist or you do not have access|unsupported model|not supported when using codex`)
	// The client's own argument errors, printed before it starts.
	argsText = regexp.MustCompile(`(?m)^error: |^Usage: codex|unexpected argument|invalid value '`)
	// The reset time of the usage limit as the client words it, in the
	// local time: "try again at Nov 15th, 2026 3:45 PM" or "try again at
	// 3:45 PM".
	resetDate = regexp.MustCompile(`(?i)try again at ([A-Z][a-z]{2}) (\d{1,2})(?:st|nd|rd|th)?,? (\d{4}),? (\d{1,2}):(\d{2}) ?([AP]M)`)
	resetTime = regexp.MustCompile(`(?i)try again at (\d{1,2}):(\d{2}) ?([AP]M)`)
	resetIn   = regexp.MustCompile(`(?i)(?:try again|resets?) in (\d+) ?(seconds?|minutes?|hours?|days?|s|m|h|d)\b`)
)

// missingSession reports whether the client said it found no thread with
// the id it was given.
func missingSession(stderr string) bool {
	return strings.Contains(stderr, "no rollout found for thread id") || strings.Contains(stderr, "no thread with id")
}

// classify turns a run that ended without an answer into an error message.
func (s *stream) classify(exit int, stderr string) attemptResult {
	msgs := append([]string{}, s.errors...)
	if s.failed != "" {
		msgs = append(msgs, s.failed)
	}
	var last serverError
	codes := map[string]bool{}
	status := 0
	for _, m := range msgs {
		e := apiErr(m)
		last = e
		codes[e.Code], codes[e.Type] = true, true
		if e.Status != 0 {
			status = e.Status
		}
	}
	text := last.text()
	if text == "" {
		text = lastLine(stderr)
	}
	if text == "" {
		text = fmt.Sprintf("the client exited with code %d", exit)
	}
	text = clip(oneLine(text), 500)
	all := text + "\n" + strings.Join(msgs, "\n") + "\n" + stderr

	has := func(set map[string]bool) bool {
		for c := range codes {
			if c != "" && set[c] {
				return true
			}
		}
		return false
	}
	switch {
	case codes["invalid_json_schema"]:
		return attemptResult{badSchema: true, final: failure(adapter.ErrPermanent, "the client refused the schema: "+text)}
	case has(limitCodes) || status == 429 || limitText.MatchString(text):
		return attemptResult{final: s.limit(text, all)}
	case status == 401 || status == 403 || authText.MatchString(text):
		return attemptResult{final: failure(adapter.ErrPermanent, text)}
	case modelText.MatchString(all) || has(permanentCodes):
		return attemptResult{final: failure(adapter.ErrPermanent, text)}
	case offlineText.MatchString(all):
		return attemptResult{final: failure(adapter.ErrOffline, text)}
	case s.session == "" && exit != 0 && argsText.MatchString(stderr):
		return attemptResult{final: failure(adapter.ErrPermanent, text)}
	case limitText.MatchString(all):
		return attemptResult{final: s.limit(text, all)}
	}
	return attemptResult{final: failure(adapter.ErrTransient, text)}
}

// limit makes a limit error, with the reset time when the client gave one
// that is still ahead.
func (s *stream) limit(text, all string) adapter.Message {
	m := failure(adapter.ErrLimit, text)
	if at := resetAt(all, time.Now()); !at.IsZero() {
		m.RetryAt = at.UTC().Format(time.RFC3339)
	}
	return m
}

// resetAt reads the reset time of a limit message, or the zero time.
func resetAt(text string, now time.Time) time.Time {
	var at time.Time
	if g := resetDate.FindStringSubmatch(text); g != nil {
		t, err := time.ParseInLocation("Jan 2 2006 3:04 PM", fmt.Sprintf("%s %s %s %s:%s %s", g[1], g[2], g[3], g[4], g[5], strings.ToUpper(g[6])), now.Location())
		if err == nil {
			at = t
		}
	} else if g := resetTime.FindStringSubmatch(text); g != nil {
		t, err := time.ParseInLocation("3:04 PM", fmt.Sprintf("%s:%s %s", g[1], g[2], strings.ToUpper(g[3])), now.Location())
		if err == nil {
			at = time.Date(now.Year(), now.Month(), now.Day(), t.Hour(), t.Minute(), 0, 0, now.Location())
			if !at.After(now) {
				at = at.Add(24 * time.Hour)
			}
		}
	} else if g := resetIn.FindStringSubmatch(text); g != nil {
		n, _ := strconv.Atoi(g[1])
		unit := map[byte]time.Duration{'s': time.Second, 'm': time.Minute, 'h': time.Hour, 'd': 24 * time.Hour}[strings.ToLower(g[2])[0]]
		at = now.Add(time.Duration(n) * unit)
	}
	if at.After(now) {
		return at
	}
	return time.Time{}
}

func lastLine(text string) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

func isObject(raw json.RawMessage) bool {
	var v map[string]any
	return json.Unmarshal(raw, &v) == nil && v != nil
}

// wholeObject returns the answer when it is a JSON object as a whole, as
// the client's structured output is.
func wholeObject(text string) (json.RawMessage, bool) {
	raw := json.RawMessage(strings.TrimSpace(text))
	if isObject(raw) {
		return raw, true
	}
	return nil, false
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
