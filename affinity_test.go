package main

import "testing"

func TestSessionID(t *testing.T) {
	doc := map[string]any{
		"metadata": map[string]any{
			"user_id": `{"device_id":"d1","account_uuid":"a1","session_id":"sess-42"}`,
		},
	}
	if got := sessionID(doc); got != "sess-42" {
		t.Fatalf("session_id = %q, want sess-42", got)
	}
}

func TestSessionIDAbsent(t *testing.T) {
	cases := map[string]map[string]any{
		"no metadata":      {},
		"no user_id":       {"metadata": map[string]any{}},
		"user_id not json": {"metadata": map[string]any{"user_id": "not json"}},
		"user_id not str":  {"metadata": map[string]any{"user_id": 5}},
	}
	for name, doc := range cases {
		if got := sessionID(doc); got != "" {
			t.Fatalf("%s: session_id = %q, want empty", name, got)
		}
	}
}

func TestExpandHeaders(t *testing.T) {
	doc := map[string]any{
		"metadata": map[string]any{"user_id": `{"session_id":"sess-42"}`},
	}
	h := map[string]string{"x-session-affinity": "{{session_id}}", "x-static": "fixed"}
	got := expandHeaders(h, doc)
	if got["x-session-affinity"] != "sess-42" {
		t.Fatalf("affinity = %q, want sess-42", got["x-session-affinity"])
	}
	if got["x-static"] != "fixed" {
		t.Fatalf("static header mutated: %q", got["x-static"])
	}
	// the original map must be untouched (no in-place expansion)
	if h["x-session-affinity"] != "{{session_id}}" {
		t.Fatalf("source map mutated: %q", h["x-session-affinity"])
	}
}

func TestExpandHeadersNoTemplate(t *testing.T) {
	h := map[string]string{"x-static": "fixed"}
	if expandHeaders(h, map[string]any{}) != nil {
		t.Fatal("want nil when no template present (caller keeps static map)")
	}
}

// An unresolvable token collapses to "" so the apply step skips it rather than
// sending a blank affinity header.
func TestExpandTemplateUnresolved(t *testing.T) {
	if got := expandTemplate("{{session_id}}", map[string]any{}); got != "" {
		t.Fatalf("want empty for unresolved token, got %q", got)
	}
}
