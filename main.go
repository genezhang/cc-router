package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
)

type ctxKey int

const routeCtxKey ctxKey = 0

// resolved is the routing decision plus bodies, handed from the outer handler
// to the ReverseProxy (and to debug/echo) via the request context.
type resolved struct {
	route *Route
	raw   []byte // inbound body as received from Claude Code
	body  []byte // outbound body (== raw when nothing was mutated)
}

func main() {
	cfgPath := getenv("CC_ROUTER_CONFIG", "config.json")
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		log.Fatalf("cc-router: config: %v", err)
	}

	var dbg *debugCapture
	if cfg.Debug || truthy(os.Getenv("CC_ROUTER_DEBUG")) {
		dbg = newDebugCapture(os.Getenv("CC_ROUTER_CAPTURE_DIR"))
	}

	proxy := &httputil.ReverseProxy{
		FlushInterval: -1, // stream SSE responses straight through, unbuffered
		Rewrite: func(pr *httputil.ProxyRequest) {
			res := pr.In.Context().Value(routeCtxKey).(resolved)

			target, _ := url.Parse(res.route.Upstream)
			pr.SetURL(target)
			pr.Out.Host = target.Host
			pr.SetXForwarded()

			pr.Out.Body = io.NopCloser(bytes.NewReader(res.body))
			pr.Out.ContentLength = int64(len(res.body))

			res.route.applyAuth(pr.Out.Header)
			for k, v := range res.route.SetHeaders {
				pr.Out.Header.Set(k, v)
			}
		},
		ErrorLog: log.Default(),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		res, ok := resolve(cfg, r)
		if !ok {
			http.Error(w, "cc-router: no route for model", http.StatusNotFound)
			return
		}
		if dbg != nil {
			dbg.capture(r, res)
		}
		if res.route.IsEcho() {
			writeEcho(w, r, res)
			return
		}
		ctx := context.WithValue(r.Context(), routeCtxKey, res)
		proxy.ServeHTTP(w, r.WithContext(ctx))
	})

	log.Printf("cc-router listening on %s", cfg.Listen)
	log.Fatal(http.ListenAndServe(cfg.Listen, mux))
}

// resolve reads the request body, picks a route by model, and applies the
// route's model rewrite + transforms. The original bytes are forwarded
// unchanged when a route mutates nothing, keeping passthrough byte-identical.
func resolve(cfg *Config, r *http.Request) (resolved, bool) {
	raw, _ := io.ReadAll(r.Body)
	_ = r.Body.Close()

	route := cfg.Match(extractModel(raw))
	if route == nil {
		return resolved{}, false
	}

	body := raw
	if route.ModelRewrite != "" || len(route.Transforms) > 0 {
		var doc map[string]any
		if err := json.Unmarshal(raw, &doc); err == nil {
			if route.ModelRewrite != "" {
				doc["model"] = route.ModelRewrite
			}
			for _, name := range route.Transforms {
				if t := transforms[name]; t != nil {
					t(doc)
				}
			}
			if b, err := json.Marshal(doc); err == nil {
				body = b
			}
		}
	}
	return resolved{route: route, raw: raw, body: body}, true
}

// extractModel pulls just the top-level "model" field; returns "" if absent.
func extractModel(raw []byte) string {
	var probe struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(raw, &probe)
	return probe.Model
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// truthy interprets an env flag; "", "0", "false", "no", "off" are false.
func truthy(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "0", "false", "no", "off":
		return false
	}
	return true
}
