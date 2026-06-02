package main

import "testing"

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
