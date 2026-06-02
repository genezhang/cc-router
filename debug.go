package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"time"
)

// debugCapture logs inbound auth + dumps request bodies when enabled, so a
// single Claude Code request reveals (1) which credential CC sends through a
// custom base URL, (2) the attribution block's JSON shape (diff ON vs =0), and
// (3) the exact bytes cc-router forwards.
type debugCapture struct {
	dir string
	seq atomic.Int64
}

func newDebugCapture(dir string) *debugCapture {
	if dir == "" {
		dir = "capture"
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		log.Printf("cc-router: debug: cannot create %s: %v", dir, err)
	}
	log.Printf("cc-router: DEBUG mode ON — dumping FULL request bodies (prompts, code, "+
		"metadata PII) as plaintext to %s/ and logging which credential each request "+
		"carries. Use on localhost only; delete %s/ when done.", dir, dir)
	return &debugCapture{dir: dir}
}

var nonword = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

// capture logs the inbound auth + model and writes the raw (and, if different,
// transformed) bodies to files for offline diffing.
func (d *debugCapture) capture(r *http.Request, res resolved) {
	model := extractModel(res.raw)
	log.Printf("cc-router: %s %s  model=%q  -> %s  auth=%s",
		r.Method, r.URL.Path, model, res.route.Upstream, redactAuth(r.Header))

	n := d.seq.Add(1)
	stamp := time.Now().Format("150405")
	base := fmt.Sprintf("%s-%03d-%s", stamp, n, sanitize(model))

	d.write(base+".in.json", res.raw)
	if !bytes.Equal(res.raw, res.body) {
		d.write(base+".out.json", res.body)
	}
}

func (d *debugCapture) write(name string, body []byte) {
	p := filepath.Join(d.dir, name)
	if err := os.WriteFile(p, body, 0o600); err != nil {
		log.Printf("cc-router: debug: write %s: %v", p, err)
		return
	}
	log.Printf("cc-router: debug: wrote %s (%d bytes)", p, len(body))
}

func sanitize(s string) string {
	s = nonword.ReplaceAllString(s, "-")
	if s == "" {
		return "none"
	}
	if len(s) > 40 {
		s = s[:40]
	}
	return s
}

// redactAuth reports which credential the client sent, with the secret masked —
// enough to tell a Pro/Max OAuth token (sk-ant-oat…) from an API key
// (sk-ant-api…) without logging the full value.
func redactAuth(h http.Header) string {
	if v := h.Get("Authorization"); v != "" {
		return "Authorization: " + maskToken(v)
	}
	if v := h.Get("X-Api-Key"); v != "" {
		return "x-api-key: " + maskToken(v)
	}
	return "(none)"
}

// maskToken keeps the scheme word (e.g. "Bearer ") and a short token prefix,
// masking the remainder.
func maskToken(v string) string {
	scheme, tok := "", v
	if i := strings.IndexByte(v, ' '); i >= 0 {
		scheme, tok = v[:i+1], v[i+1:]
	}
	const keep = 14
	if len(tok) <= keep {
		return scheme + tok
	}
	return fmt.Sprintf("%s%s…(%d more)", scheme, tok[:keep], len(tok)-keep)
}

// writeEcho responds with what cc-router resolved instead of forwarding — the
// matched route, outbound headers (auth masked), and the transformed body. Use
// a route with "upstream": "echo" to test routing/transforms with no provider.
// (Claude Code will show a parse error for the echo response; that's expected —
// read this JSON and the capture files.)
func writeEcho(w http.ResponseWriter, r *http.Request, res resolved) {
	out := r.Header.Clone()
	res.route.applyAuth(out)
	for k, v := range res.headers {
		if v != "" && out.Get(k) == "" {
			out.Set(k, v)
		}
	}

	var bodyDoc any
	if err := json.Unmarshal(res.body, &bodyDoc); err != nil {
		bodyDoc = string(res.body)
	}

	payload := map[string]any{
		"cc_router":          "echo",
		"matched":            res.route.Match,
		"model_rewrite":      res.route.ModelRewrite,
		"transforms":         res.route.Transforms,
		"inbound_auth":       redactAuth(r.Header),
		"outbound_auth":      redactAuth(out),
		"set_headers":        res.headers,
		"x_session_affinity": out.Get("X-Session-Affinity"),
		"transformed_body":   bodyDoc,
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(payload)
}
