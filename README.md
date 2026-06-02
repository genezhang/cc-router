# cc-router

A tiny, dependency-free **model-keyed gateway** for Claude Code. Point Claude Code
at it once; it routes each request to a different upstream based on the model, and
applies per-route request tweaks so each provider gets the body *it* wants.

```
                         ┌─ claude-opus-*      → Anthropic (native, untouched)
Claude Code ─(BASE_URL)─▶ cc-router ─┼─ claude-*haiku*/sonnet → Fireworks GLM (+ affinity, stripped markers)
                         └─ qwen*             → local box (future)
```

## Why a gateway instead of env vars

Claude Code's cache-tuning knobs are process-global env vars
(`ANTHROPIC_CUSTOM_HEADERS`, `CLAUDE_CODE_ATTRIBUTION_HEADER=0`,
`DISABLE_PROMPT_CACHING=1`). But **one Claude Code session emits more than one
model** — your main loop uses whatever you picked, while background/quick tasks
fire Haiku. So you can't say "attribution off for Haiku, on for Opus" from the CC
side; the env var hits every request.

The only place the per-model distinction is visible is `body.model`. cc-router
routes on it and replicates those settings **per route** instead:

| CC env (if set globally) | Conflicts because | cc-router per-route equivalent |
|---|---|---|
| `ANTHROPIC_CUSTOM_HEADERS: x-session-affinity` | Anthropic ignores it; only Fireworks cares | `set_headers` on the Fireworks route |
| `CLAUDE_CODE_ATTRIBUTION_HEADER=0` | also strips attribution from Opus→Anthropic | `strip_attribution` transform |
| `DISABLE_PROMPT_CACHING=1` | kills Anthropic's native `cache_control` caching | `strip_cache_control` transform |

Leave all three **out** of Claude Code; cc-router applies them only where they help.

## Config

JSON, first-match-wins. See [`config.example.json`](config.example.json).

```jsonc
{
  "listen": "127.0.0.1:8787",
  "routes": [
    { "match": ["claude-opus-*"], "upstream": "https://api.anthropic.com",
      "auth": { "mode": "passthrough" } },

    { "match": ["claude-sonnet-*", "claude-*haiku*"],
      "upstream": "https://api.fireworks.ai/inference",
      "auth": { "mode": "bearer_env", "bearer_env": "FIREWORKS_API_KEY" },
      "model_rewrite": "accounts/fireworks/models/glm-4p6",
      "set_headers": { "x-session-affinity": "cc-router-personal" },
      "transforms": ["strip_cache_control", "strip_attribution"] },

    { "match": ["*"], "upstream": "https://api.anthropic.com",
      "auth": { "mode": "passthrough" } }
  ]
}
```

| Field | Meaning |
|---|---|
| `match` | glob patterns (`path.Match`) tested against the request's `model`; `*` is a catch-all (also matches bodyless calls) |
| `upstream` | base URL; the incoming path (`/v1/messages`, …) is appended |
| `auth.mode` | `passthrough` (forward the client's `x-api-key`/Bearer) or `bearer_env` (inject `Authorization: Bearer $<bearer_env>`, drop `x-api-key`) |
| `model_rewrite` | replaces `body.model` (e.g. `claude-3-5-haiku` → a Fireworks model id) |
| `set_headers` | headers set on the outbound request |
| `transforms` | named body mutators, applied in order |

A route with **no** `model_rewrite` and **no** `transforms` forwards the original
bytes untouched — the Anthropic passthrough is byte-identical.

### Transforms

| Name | Replicates | Status |
|---|---|---|
| `strip_cache_control` | `DISABLE_PROMPT_CACHING=1` — recursively deletes every `cache_control` key | ✅ working |
| `strip_attribution` | `CLAUDE_CODE_ATTRIBUTION_HEADER=0` — removes the billing-header system block | ✅ working |

**What `strip_attribution` actually removes** (captured from a live Claude Code
session): with the header on, Claude Code prepends a block as **`system[0]`**:

```
x-anthropic-billing-header: cc_version=2.1.160.bca; cc_entrypoint=cli; cch=45c9a;
```

The `cch` hash **changes every request** (`45c9a` → `b0712` on consecutive turns
of one session), and because it sits at the very front of the prompt it
invalidates the whole cacheable prefix on every turn. `strip_attribution` drops
any `system` element whose text starts with `x-anthropic-billing-header:`,
reproducing exactly what `CLAUDE_CODE_ATTRIBUTION_HEADER=0` does — which is why
it's the single biggest cache-hit lever.

## Run

```bash
go build && CC_ROUTER_CONFIG=config.json FIREWORKS_API_KEY=fw_… ./cc-router
```

Point Claude Code at it (no cache env vars needed — cc-router handles them):

```bash
export ANTHROPIC_BASE_URL="http://127.0.0.1:8787"
```

## Debug / echo mode

Set `CC_ROUTER_DEBUG=1` (or `"debug": true` in config) and cc-router logs which
credential each request carries (masked) and dumps the inbound/outbound bodies to
`capture/`. Point a route at `"upstream": "echo"` and it **responds with what it
resolved** instead of forwarding — no provider or key needed.

```bash
go build && CC_ROUTER_CONFIG=config.echo.json ./cc-router
# then point Claude Code at it and send one message
export ANTHROPIC_BASE_URL="http://127.0.0.1:8787"
```

A single request answers three things at once:

1. **What credential Claude Code sends through a custom base URL** — the log line
   shows `Authorization: Bearer sk-ant-oat…` (Pro/Max subscription token) vs.
   `x-api-key: sk-ant-api…` (API billing) vs. `(none)`.
2. **The attribution-block shape** — run once with `CLAUDE_CODE_ATTRIBUTION_HEADER`
   unset and once `=0`, then `diff capture/*.in.json` to see exactly what the
   attribution injects (the fixture needed to finish `strip_attribution`).
3. **That transforms produce the right bytes** — the echo response and
   `capture/*.out.json` show the rewritten model and stripped markers.

> Claude Code will show a parse error for the echo response (it isn't a real
> model reply) — that's expected; read the log and the `capture/` files.

`config.example.json` is the real routing config; `config.echo.json` is the
debug/echo harness.

## Maximizing cache hits: two axes

Hit rate has two independent levers:

| Axis | Lever | Effect |
|---|---|---|
| **Which backend** | `x-session-affinity` (`set_headers`) | routes to a server that *might* have a warm cache |
| **Whether the prefix matches** | `strip_attribution` + `strip_cache_control` | stabilize the cacheable prefix the upstream sees |

In practice prefix stability is the bigger lever.

## Status & roadmap

- ✅ model routing, model rewrite, `strip_cache_control`, `strip_attribution`, auth passthrough/inject, header injection, SSE passthrough, debug/echo mode.
- 🔭 **local OpenAI-only box (Qwen):** this gateway does **no protocol
  translation** — it assumes every upstream speaks Anthropic `/v1/messages`
  (Anthropic native + Fireworks Anthropic-compat both do). For an OpenAI-only
  local model, either run it behind an Anthropic-compatible server (vLLM /
  llama.cpp have `/v1/messages` shims) or route that rule to a translating
  gateway (e.g. Bifrost). Don't hand-roll Anthropic↔OpenAI SSE translation here.

## Notes & caveats

- **Unofficial.** Relies on Claude Code's current header/body behavior; a future
  version could change it.
- **Run on localhost / a trusted network** — it can hold upstream keys and sits
  in the request path.

## License

MIT — see [LICENSE](LICENSE).
