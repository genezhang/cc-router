package main

import (
	"strings"
	"testing"
)

func TestStripCacheControl(t *testing.T) {
	doc := map[string]any{
		"model": "x",
		"system": []any{
			map[string]any{"type": "text", "text": "hi", "cache_control": map[string]any{"type": "ephemeral"}},
		},
		"messages": []any{
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{"type": "text", "text": "yo", "cache_control": map[string]any{"type": "ephemeral"}},
				},
			},
		},
	}
	stripCacheControl(doc)
	if hasKey(doc, "cache_control") {
		t.Fatal("cache_control survived somewhere in the document")
	}
	// content must be otherwise intact
	if doc["model"] != "x" {
		t.Fatalf("model mutated: %v", doc["model"])
	}
}

// Fixture shape captured from a real Claude Code request (attribution ON):
// system[0] is the billing-header block; =0 omits it.
func TestStripAttribution(t *testing.T) {
	doc := map[string]any{
		"system": []any{
			map[string]any{"type": "text", "text": "x-anthropic-billing-header: cc_version=2.1.160.bca; cc_entrypoint=cli; cch=45c9a;"},
			map[string]any{"type": "text", "text": "You are Claude Code, Anthropic's official CLI for Claude."},
			map[string]any{"type": "text", "text": "You are an interactive agent..."},
		},
	}
	stripAttribution(doc)

	sys := doc["system"].([]any)
	if len(sys) != 2 {
		t.Fatalf("want 2 system blocks after strip, got %d", len(sys))
	}
	first := sys[0].(map[string]any)["text"].(string)
	if !strings.HasPrefix(first, "You are Claude Code") {
		t.Fatalf("billing-header block not removed; first block now: %q", first)
	}
}

// With attribution already off (=0 shape), the transform is a no-op.
func TestStripAttributionAbsent(t *testing.T) {
	doc := map[string]any{"system": []any{
		map[string]any{"type": "text", "text": "You are Claude Code, Anthropic's official CLI for Claude."},
	}}
	stripAttribution(doc)
	if len(doc["system"].([]any)) != 1 {
		t.Fatal("system unexpectedly changed when no billing header present")
	}
}

// hasKey reports whether key appears in any nested map of the document.
func hasKey(v any, key string) bool {
	found := false
	walk(v, func(m map[string]any) {
		if _, ok := m[key]; ok {
			found = true
		}
	})
	return found
}
