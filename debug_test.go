package main

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMaskTokenDiscriminatesAndHides(t *testing.T) {
	oat := maskToken("Bearer sk-ant-oat01-secretsecretsecret")
	api := maskToken("sk-ant-api03-secretsecretsecret")

	if !strings.Contains(oat, "sk-ant-oat") {
		t.Fatalf("OAuth prefix lost: %q", oat)
	}
	if !strings.Contains(api, "sk-ant-api") {
		t.Fatalf("API-key prefix lost: %q", api)
	}
	if strings.Contains(oat, "secretsecretsecret") || strings.Contains(api, "secretsecretsecret") {
		t.Fatalf("token not masked: %q / %q", oat, api)
	}
}

func TestIsEcho(t *testing.T) {
	if !(&Route{Upstream: "echo"}).IsEcho() || !(&Route{Upstream: "ECHO"}).IsEcho() {
		t.Fatal("echo upstream not recognized")
	}
	if (&Route{Upstream: "https://api.anthropic.com"}).IsEcho() {
		t.Fatal("real upstream wrongly treated as echo")
	}
}

// End-to-end: resolve + echo, asserting model rewrite and cache_control stripping
// are reflected in the echoed transformed body.
func TestEchoEndToEnd(t *testing.T) {
	cfg := &Config{Routes: []Route{{
		Match:        []string{"*"},
		Upstream:     "echo",
		ModelRewrite: "accounts/fireworks/models/glm-4p6",
		Transforms:   []string{"strip_cache_control"},
	}}}
	body := `{"model":"claude-3-5-haiku","system":[{"type":"text","cache_control":{"type":"ephemeral"}}]}`
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
	req.Header.Set("x-api-key", "sk-ant-api03-xyz")

	res, ok := resolve(cfg, req)
	if !ok {
		t.Fatal("no route matched")
	}
	rec := httptest.NewRecorder()
	writeEcho(rec, req, res)

	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("echo body not JSON: %v", err)
	}
	tb, _ := out["transformed_body"].(map[string]any)
	if tb["model"] != "accounts/fireworks/models/glm-4p6" {
		t.Fatalf("model_rewrite not applied: %v", tb["model"])
	}
	if hasKey(tb, "cache_control") {
		t.Fatal("cache_control survived in echoed body")
	}
}
