package main

// transforms maps a config name to a body mutator. Each operates in place on
// the decoded JSON document. Mutators must be pure (no I/O) and tolerant of
// missing or oddly-typed fields.
var transforms = map[string]func(map[string]any){
	"strip_cache_control": stripCacheControl,
	"strip_attribution":   stripAttribution,
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

// stripAttribution replicates CLAUDE_CODE_ATTRIBUTION_HEADER=0 by removing the
// attribution block Claude Code injects into message content (issue URL, CLI
// version, git SHA), which would otherwise vary the cacheable prefix.
//
// TODO(fixture): the exact JSON shape is not yet confirmed. Capture a real
// request body with attribution ON vs. =0, diff them, and implement against
// that fixture. Stubbed as a no-op until then so routing stays correct.
func stripAttribution(doc map[string]any) {
	// no-op pending fixture
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
