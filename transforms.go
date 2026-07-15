package main

import "strings"

// transforms maps a config name to a body mutator. Each operates in place on
// the decoded JSON document. Mutators must be pure (no I/O) and tolerant of
// missing or oddly-typed fields.
var transforms = map[string]func(map[string]any){
	"strip_cache_control": stripCacheControl,
	"strip_attribution":   stripAttribution,
	"strip_metadata":      stripMetadata,
	"strip_thinking":      stripThinking,
}

// stripMetadata removes the top-level "metadata" object. On Claude Code requests
// that carries metadata.user_id — a JSON blob of {device_id, account_uuid,
// session_id} identifying the user's machine and account. A third-party upstream
// (Fireworks, a local box) doesn't need it, so dropping it minimizes the PII
// sent off to providers. session_id, the only field cc-router itself uses, is
// lifted into the x-session-affinity header before transforms run, so affinity
// is unaffected. Use this only on non-Anthropic routes — Anthropic uses user_id
// for abuse signals.
func stripMetadata(doc map[string]any) {
	delete(doc, "metadata")
}

// stripCacheControl removes every "cache_control" key anywhere in the document,
// replicating DISABLE_PROMPT_CACHING=1. Anthropic-style ephemeral cache markers
// are dropped so a non-Anthropic upstream's own automatic prefix cache sees a
// stable body instead of markers it doesn't understand.
func stripCacheControl(doc map[string]any) {
	walk(doc, func(m map[string]any) {
		delete(m, "cache_control")
	})
}

// billingHeaderPrefix marks the attribution block Claude Code injects as the
// FIRST system block when CLAUDE_CODE_ATTRIBUTION_HEADER is not 0, e.g.:
//
//	x-anthropic-billing-header: cc_version=2.1.160.bca; cc_entrypoint=cli; cch=45c9a;
//
// The cch hash changes every request, so this block poisons the cacheable
// prefix on every turn. (Verified by capturing the same session with the header
// on vs =0: only this block differs, and cch varied 45c9a -> b0712 turn-to-turn.)
const billingHeaderPrefix = "x-anthropic-billing-header:"

// stripAttribution replicates CLAUDE_CODE_ATTRIBUTION_HEADER=0 by removing that
// billing-header system block, restoring a stable prefix.
func stripAttribution(doc map[string]any) {
	sys, ok := doc["system"].([]any)
	if !ok {
		return
	}
	kept := make([]any, 0, len(sys))
	for _, el := range sys {
		if !isBillingHeaderBlock(el) {
			kept = append(kept, el)
		}
	}
	doc["system"] = kept
}

// isBillingHeaderBlock reports whether a system element is the billing-header
// attribution block.
func isBillingHeaderBlock(el any) bool {
	m, ok := el.(map[string]any)
	if !ok {
		return false
	}
	text, _ := m["text"].(string)
	return strings.HasPrefix(strings.TrimSpace(text), billingHeaderPrefix)
}

// stripThinking removes "thinking" and "redacted_thinking" content blocks from
// every message. Anthropic returns these blocks (with cryptographic signatures)
// when extended thinking is on, and Claude Code stores them in the transcript.
// On a later request, they are sent back as input - but Anthropic rejects any
// whose signature no longer validates (in case of switching models to a different
// provider). So we need to stip them. But here our goal is to reduce the context
// by removing thinking blocks, as they constitute the largest portion of the
// context window.
func stripThinking(doc map[string]any) {
	msgs, ok := doc["messages"].([]any)
	if !ok {
		return
	}
	for _, m := range msgs {
		mm, ok := m.(map[string]any)
		if !ok {
			continue
		}
		content, ok := mm["content"].([]any)
		if !ok {
			continue // string content, or absent - nothing to filter
		}
		filtered := make([]any, 0, len(content))
		for _, blk := range content {
			if b, ok := blk.(map[string]any); ok {
				t, _ := b["type"].(string)
 				if t == "thinking" || t == "redacted_thinking" {
					continue
				}
			}
			filtered = append(filtered, blk)
		}
		mm["content"] = filtered
	}
}

// walk visits every nested map in a decoded JSON document — objects within
// objects and within arrays — calling fn on each.
func walk(v any, fn func(map[string]any)) {
	switch t := v.(type) {
	case map[string]any:
		fn(t)
		for _, child := range t {
			walk(child, fn)
		}
	case []any:
		for _, child := range t {
			walk(child, fn)
		}
	}
}
