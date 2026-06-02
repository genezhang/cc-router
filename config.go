package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
)

// Config is the whole gateway config, loaded once at boot from a JSON file.
type Config struct {
	Listen string  `json:"listen"`
	Debug  bool    `json:"debug"` // log auth + dump bodies (also enabled by CC_ROUTER_DEBUG)
	Routes []Route `json:"routes"`
}

// Route is one first-match-wins rule: how to match, where to send, what to change.
type Route struct {
	Match        []string          `json:"match"`         // glob patterns against the request's model
	Upstream     string            `json:"upstream"`      // base URL to forward to; "echo" responds instead of forwarding
	Auth         Auth              `json:"auth"`          // how to authenticate to the upstream
	ModelRewrite string            `json:"model_rewrite"` // if set, replaces body.model
	SetHeaders   map[string]string `json:"set_headers"`   // headers to set on the outbound request (e.g. x-session-affinity)
	Transforms   []string          `json:"transforms"`    // named body mutators, applied in order
}

// Auth controls the credential sent upstream.
//
//	mode: "passthrough" (default) — forward the client's auth headers unchanged.
//	                                Only allowed to Anthropic (enforced in Validate),
//	                                so the subscription token never reaches a third party.
//	mode: "bearer_env"            — strip the client's auth, then set
//	                                Authorization: Bearer $<bearer_env>.
//	mode: "none"                  — strip the client's auth, send nothing
//	                                (e.g. a local box that needs no credential).
type Auth struct {
	Mode      string `json:"mode"`
	BearerEnv string `json:"bearer_env"`
}

// LoadConfig reads, parses, and validates the JSON config file.
func LoadConfig(p string) (*Config, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	if c.Listen == "" {
		c.Listen = "127.0.0.1:8787"
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

// Validate enforces the security invariants that keep credentials and PII from
// leaking, so a misconfiguration fails at boot rather than at request time:
//   - the client credential (e.g. an Anthropic subscription OAuth token) is only
//     ever forwarded to Anthropic — any other upstream must strip it (bearer_env
//     or none);
//   - credentials/bodies never travel in cleartext — non-loopback upstreams must
//     be https;
//   - a bearer_env route actually names an env var (otherwise it would send no
//     credential at all).
func (c *Config) Validate() error {
	for i := range c.Routes {
		r := &c.Routes[i]
		if r.IsEcho() {
			continue
		}
		u, err := url.Parse(r.Upstream)
		if err != nil || u.Host == "" {
			return fmt.Errorf("route %d %v: invalid upstream %q", i, r.Match, r.Upstream)
		}
		host := u.Hostname()
		if u.Scheme != "https" && !isLocalHost(host) {
			return fmt.Errorf("route %d %v: upstream %q must use https (http is allowed only to localhost)", i, r.Match, r.Upstream)
		}
		switch r.Auth.Mode {
		case "", "passthrough":
			if !isAnthropicHost(host) {
				return fmt.Errorf("route %d %v: auth.mode \"passthrough\" forwards the client's credential and is only allowed to *.anthropic.com, not %q — use \"bearer_env\" or \"none\" for third-party/local upstreams", i, r.Match, host)
			}
		case "bearer_env":
			if r.Auth.BearerEnv == "" {
				return fmt.Errorf("route %d %v: auth.mode \"bearer_env\" requires a \"bearer_env\": \"<ENV_VAR>\" naming the key env var", i, r.Match)
			}
		case "none":
			// strips all auth; safe to any host
		default:
			return fmt.Errorf("route %d %v: unknown auth.mode %q (want passthrough|bearer_env|none)", i, r.Match, r.Auth.Mode)
		}
	}
	return nil
}

// isAnthropicHost reports whether h is Anthropic's API host — the only upstream
// allowed to receive a passed-through client credential.
func isAnthropicHost(h string) bool {
	h = strings.ToLower(h)
	return h == "anthropic.com" || strings.HasSuffix(h, ".anthropic.com")
}

// isLocalHost reports whether h is the loopback interface, where plaintext http
// can't be sniffed off the wire.
func isLocalHost(h string) bool {
	if strings.EqualFold(h, "localhost") {
		return true
	}
	if ip := net.ParseIP(h); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// Match returns the first route whose patterns match the model, or nil.
// An empty model still matches a "*" catch-all route.
func (c *Config) Match(model string) *Route {
	for i := range c.Routes {
		for _, pat := range c.Routes[i].Match {
			if ok, _ := path.Match(pat, model); ok {
				return &c.Routes[i]
			}
		}
	}
	return nil
}

// IsEcho reports whether this route should be echoed instead of forwarded.
func (r *Route) IsEcho() bool { return strings.EqualFold(r.Upstream, "echo") }

// applyAuth sets upstream credentials on the outbound headers per the route's mode.
func (r *Route) applyAuth(h http.Header) {
	switch r.Auth.Mode {
	case "bearer_env", "none":
		// Never let the client's credential (e.g. an Anthropic subscription
		// token) ride along to a non-Anthropic upstream: strip it
		// unconditionally first. A missing bearer_env value then yields an
		// unauthenticated request the provider rejects — never a token leak.
		h.Del("Authorization")
		h.Del("X-Api-Key")
		if r.Auth.Mode == "bearer_env" {
			if tok := os.Getenv(r.Auth.BearerEnv); tok != "" {
				h.Set("Authorization", "Bearer "+tok)
			}
		}
	}
	// "passthrough" (default): leave the client's inbound auth headers untouched.
	// Validate guarantees this mode only targets Anthropic.
}
