package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path"
)

// Config is the whole gateway config, loaded once at boot from a JSON file.
type Config struct {
	Listen string  `json:"listen"`
	Routes []Route `json:"routes"`
}

// Route is one first-match-wins rule: how to match, where to send, what to change.
type Route struct {
	Match        []string          `json:"match"`         // glob patterns against the request's model
	Upstream     string            `json:"upstream"`      // base URL to forward to; incoming path is appended
	Auth         Auth              `json:"auth"`          // how to authenticate to the upstream
	ModelRewrite string            `json:"model_rewrite"` // if set, replaces body.model
	SetHeaders   map[string]string `json:"set_headers"`   // headers to set on the outbound request (e.g. x-session-affinity)
	Transforms   []string          `json:"transforms"`    // named body mutators, applied in order
}

// Auth controls the credential sent upstream.
//
//	mode: "passthrough" (default) — forward the client's auth headers unchanged
//	mode: "bearer_env"            — set Authorization: Bearer $<bearer_env>, drop x-api-key
type Auth struct {
	Mode      string `json:"mode"`
	BearerEnv string `json:"bearer_env"`
}

// LoadConfig reads and parses the JSON config file.
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
	return &c, nil
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

// applyAuth sets upstream credentials on the outbound request per the route's mode.
func (r *Route) applyAuth(out *http.Request) {
	if r.Auth.Mode == "bearer_env" {
		if tok := os.Getenv(r.Auth.BearerEnv); tok != "" {
			out.Header.Set("Authorization", "Bearer "+tok)
			out.Header.Del("X-Api-Key")
		}
	}
	// "passthrough" (default): leave the client's inbound auth headers untouched.
}
