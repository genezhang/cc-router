package main

import (
	"encoding/json"
	"strings"
)

// hasTemplate reports whether any header value carries a {{token}} that must be
// expanded against the request body.
func hasTemplate(h map[string]string) bool {
	for _, v := range h {
		if strings.Contains(v, "{{") {
			return true
		}
	}
	return false
}

// expandHeaders resolves {{token}} placeholders in a route's set_headers against
// the decoded request body. Returns nil when there is nothing to expand, so the
// caller keeps the static map untouched.
func expandHeaders(h map[string]string, doc map[string]any) map[string]string {
	if !hasTemplate(h) {
		return nil
	}
	out := make(map[string]string, len(h))
	for k, v := range h {
		out[k] = expandTemplate(v, doc)
	}
	return out
}

// expandTemplate substitutes the supported {{token}} placeholders. An
// unresolvable token collapses to "", and a value that ends up empty is skipped
// by the header-apply step rather than sent blank.
func expandTemplate(v string, doc map[string]any) string {
	if strings.Contains(v, "{{session_id}}") {
		v = strings.ReplaceAll(v, "{{session_id}}", sanitizeHeaderToken(sessionID(doc)))
	}
	return v
}

// sanitizeHeaderToken makes a body-derived value safe to place in an HTTP
// header: it keeps only visible ASCII (no CR/LF or control bytes, which would
// otherwise enable header/request-splitting injection from a crafted request
// body) and bounds the length. A legitimate session_id is a UUID, so this is a
// no-op for real traffic and only bites adversarial input.
func sanitizeHeaderToken(s string) string {
	const max = 128
	var b strings.Builder
	for i := 0; i < len(s) && b.Len() < max; i++ {
		if c := s[i]; c > 0x20 && c < 0x7f {
			b.WriteByte(c)
		}
	}
	return b.String()
}

// sessionID extracts the per-session identifier Claude Code embeds in
// metadata.user_id. That field is itself a JSON string, e.g.
//
//	{"device_id":"...","account_uuid":"...","session_id":"abc-123"}
//
// session_id is stable for every turn of one CC session — main loop, subagents,
// and Haiku background calls all carry the same value — and changes only when a
// new session starts. That makes it the foolproof, structured affinity signal:
// requests that share a growing cacheable prefix share a key, so they pin to one
// warm backend; a different session gets a different key and spreads. (The cwd
// would key per-project, but it only appears as free text inside the system
// prompt and prompt-cache TTLs are short enough that cross-session warmth rarely
// survives anyway — so session_id captures the benefit that actually exists.)
// Returns "" when absent (bodyless preflights, the "quota" probe), which yields
// no affinity header at all.
func sessionID(doc map[string]any) string {
	meta, ok := doc["metadata"].(map[string]any)
	if !ok {
		return ""
	}
	uid, ok := meta["user_id"].(string)
	if !ok {
		return ""
	}
	var u struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal([]byte(uid), &u); err != nil {
		return ""
	}
	return u.SessionID
}
