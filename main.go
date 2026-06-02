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
)

type ctxKey int

const routeCtxKey ctxKey = 0

// resolved is the routing decision plus the (possibly transformed) request body,
// handed from the outer handler to the ReverseProxy via the request context.
type resolved struct {
	route *Route
	body  []byte
}

func main() {
	cfgPath := getenv("CC_ROUTER_CONFIG", "config.json")
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		log.Fatalf("cc-router: config: %v", err)
	}

	proxy := &httputil.ReverseProxy{
		FlushInterval: -1, // stream SSE responses straight through, unbuffered
		Rewrite: func(pr *httputil.ProxyRequest) {
			res := pr.In.Context().Value(routeCtxKey).(resolved)

			target, _ := url.Parse(res.route.Upstream)
			pr.SetURL(target)
			pr.Out.Host = target.Host
			pr.SetXForwarded()

			// Send the bytes we resolved (original or transformed).
			pr.Out.Body = io.NopCloser(bytes.NewReader(res.body))
			pr.Out.ContentLength = int64(len(res.body))

			res.route.applyAuth(pr.Out)
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
	return resolved{route: route, body: body}, true
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
