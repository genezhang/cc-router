package main

import "testing"

func TestMatch(t *testing.T) {
	cfg := &Config{Routes: []Route{
		{Match: []string{"claude-opus-*"}, Upstream: "anthropic"},
		{Match: []string{"claude-sonnet-*", "claude-*haiku*"}, Upstream: "fireworks"},
		{Match: []string{"*"}, Upstream: "default"},
	}}
	cases := map[string]string{
		"claude-opus-4-20250514":    "anthropic",
		"claude-3-5-haiku-20241022": "fireworks",
		"claude-sonnet-4-20250514":  "fireworks",
		"gpt-4o":                    "default",
		"":                          "default", // no model (e.g. non-messages call) -> catch-all
	}
	for model, want := range cases {
		got := cfg.Match(model)
		if got == nil {
			t.Errorf("Match(%q) = nil; want %s", model, want)
			continue
		}
		if got.Upstream != want {
			t.Errorf("Match(%q) = %s; want %s", model, got.Upstream, want)
		}
	}
}

func TestMatchNoCatchAll(t *testing.T) {
	cfg := &Config{Routes: []Route{
		{Match: []string{"claude-opus-*"}, Upstream: "anthropic"},
	}}
	if got := cfg.Match("gpt-4o"); got != nil {
		t.Errorf("Match(gpt-4o) = %v; want nil (no route)", got)
	}
}
