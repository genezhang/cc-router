package main

import (
	"net/http"
	"testing"
)

func cfg(routes ...Route) *Config { return &Config{Listen: "127.0.0.1:1", Routes: routes} }

func TestValidatePassthroughOnlyToAnthropic(t *testing.T) {
	// passthrough to a third party would leak the client's subscription token
	err := cfg(Route{Match: []string{"*"}, Upstream: "https://api.fireworks.ai/inference",
		Auth: Auth{Mode: "passthrough"}}).Validate()
	if err == nil {
		t.Fatal("expected error: passthrough to non-Anthropic upstream")
	}

	// passthrough to Anthropic is fine (default mode == passthrough too)
	if err := cfg(Route{Match: []string{"*"}, Upstream: "https://api.anthropic.com"}).Validate(); err != nil {
		t.Fatalf("passthrough to Anthropic should be allowed: %v", err)
	}
}

func TestValidateThirdPartyNeedsKeyOrNone(t *testing.T) {
	// bearer_env to a third party is allowed...
	if err := cfg(Route{Match: []string{"*"}, Upstream: "https://api.fireworks.ai/inference",
		Auth: Auth{Mode: "bearer_env", BearerEnv: "FIREWORKS_API_KEY"}}).Validate(); err != nil {
		t.Fatalf("bearer_env third party should be allowed: %v", err)
	}
	// ...but bearer_env without a named env var is not (would send no credential)
	if err := cfg(Route{Match: []string{"*"}, Upstream: "https://api.fireworks.ai/inference",
		Auth: Auth{Mode: "bearer_env"}}).Validate(); err == nil {
		t.Fatal("expected error: bearer_env with empty bearer_env name")
	}
	// "none" is allowed to any host (it strips credentials)
	if err := cfg(Route{Match: []string{"*"}, Upstream: "https://example.com",
		Auth: Auth{Mode: "none"}}).Validate(); err != nil {
		t.Fatalf("none mode should be allowed anywhere: %v", err)
	}
}

func TestValidateRequiresHTTPSOffLoopback(t *testing.T) {
	if err := cfg(Route{Match: []string{"*"}, Upstream: "http://api.anthropic.com"}).Validate(); err == nil {
		t.Fatal("expected error: plaintext http to a remote host")
	}
	// http to a local box is allowed
	if err := cfg(Route{Match: []string{"*"}, Upstream: "http://127.0.0.1:11434",
		Auth: Auth{Mode: "none"}}).Validate(); err != nil {
		t.Fatalf("http to localhost should be allowed: %v", err)
	}
}

func TestValidateUnknownMode(t *testing.T) {
	if err := cfg(Route{Match: []string{"*"}, Upstream: "https://api.anthropic.com",
		Auth: Auth{Mode: "weird"}}).Validate(); err == nil {
		t.Fatal("expected error: unknown auth.mode")
	}
}

// A bearer_env route must strip the client's inbound credential even when the
// key env var is unset — otherwise the subscription token would leak upstream.
func TestApplyAuthStripsInboundOnBearerEnv(t *testing.T) {
	r := &Route{Auth: Auth{Mode: "bearer_env", BearerEnv: "DEFINITELY_UNSET_ENV_VAR"}}
	h := http.Header{}
	h.Set("Authorization", "Bearer sk-ant-oat01-secret")
	h.Set("X-Api-Key", "sk-ant-api-secret")
	r.applyAuth(h)
	if h.Get("Authorization") != "" {
		t.Fatalf("inbound Authorization survived: %q", h.Get("Authorization"))
	}
	if h.Get("X-Api-Key") != "" {
		t.Fatalf("inbound X-Api-Key survived: %q", h.Get("X-Api-Key"))
	}
}

func TestApplyAuthNoneStripsAll(t *testing.T) {
	r := &Route{Auth: Auth{Mode: "none"}}
	h := http.Header{}
	h.Set("Authorization", "Bearer sk-ant-oat01-secret")
	r.applyAuth(h)
	if h.Get("Authorization") != "" {
		t.Fatalf("none should strip Authorization, got %q", h.Get("Authorization"))
	}
}

func TestSanitizeHeaderTokenStripsInjection(t *testing.T) {
	got := sanitizeHeaderToken("sess-1\r\nX-Injected: evil")
	if got != "sess-1X-Injected:evil" {
		t.Fatalf("CR/LF/space not stripped: %q", got)
	}
	// a real UUID passes through untouched
	const uuid = "7a163de0-d62c-43ed-8098-e499952be89b"
	if sanitizeHeaderToken(uuid) != uuid {
		t.Fatalf("UUID mangled: %q", sanitizeHeaderToken(uuid))
	}
}
