# cc-router

A tiny, dependency-free **model-keyed gateway** for Claude Code. Point Claude Code
at it once; it routes each request to a different upstream based on the model, and
applies per-route request tweaks so each provider gets the body *it* wants.

```
                                     ┌─ claude-opus-*, claude-fable-*  → Anthropic (native, untouched - see note)
Claude Code ─(BASE_URL)─▶ cc-router ─┼─ claude-*haiku*/sonnet → Fireworks GLM (+ affinity, stripped markers)
                                     └─ qwen*             → local box (future)
```

**Note**: Real use experiences revealed an incompatibility between `fireworks.ai` and `Anthropic` thinking blocks. Claude Code
verifies the signature on a thinking block, which does not work when switching models in a session from fireworks.ai back to Anthropic.
So we have enhanced cc-router with the `"strip_thinking"` transform. It eliminated the 400 error in Claude Code. Furthermore,
stats shows that thinking blocks are 40+% of the context in a long-horizon coding session. "strip_thinking" helps
reduce the session context size, although the saved tokens are mostly cached reads.

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
    {
      "match": ["claude-opus-*", "claude-fable-*"],
      "upstream": "https://api.anthropic.com",
      "auth": { "mode": "passthrough" },
      "transforms": ["strip_thinking"]
    },
    {
      "match": ["claude-sonnet-*", "claude-*haiku*"],
      "upstream": "https://api.fireworks.ai/inference",
      "auth": { "mode": "bearer_env", "bearer_env": "FIREWORKS_API_KEY" },
      "model_rewrite": "accounts/fireworks/models/glm-4p6",
      "set_headers": { "x-session-affinity": "{{session_id}}" },
      "transforms": ["strip_cache_control", "strip_attribution", "strip_thinking"]
    },
    {
      "match": ["*"], "upstream": "https://api.anthropic.com",
      "auth": { "mode": "passthrough" },
      "transforms": ["strip_thinking"]
    }
  ]
}
```

**Tip**: if you need to accept routing from other local machines, change "listen" to the following:
```
  "listen": "0.0.0.0:8787",
```

| Field | Meaning |
|---|---|
| `match` | glob patterns (`path.Match`) tested against the request's `model`; `*` is a catch-all (also matches bodyless calls) |
| `upstream` | base URL; the incoming path (`/v1/messages`, …) is appended |
| `auth.mode` | `passthrough` (forward the client's `x-api-key`/Bearer — **Anthropic upstreams only**), `bearer_env` (strip the client's auth, then inject `Authorization: Bearer $<bearer_env>`), or `none` (strip the client's auth, send nothing — e.g. a local box) |
| `model_rewrite` | replaces `body.model` (e.g. `claude-3-5-haiku` → a Fireworks model id) |
| `set_headers` | headers added to the outbound request, **only if the client didn't already send them** (set-if-absent — see below) |
| `transforms` | named body mutators, applied in order |

A route with **no** `model_rewrite` and **no** `transforms` forwards the original
bytes untouched — the Anthropic passthrough is byte-identical.

#### `set_headers` is set-if-absent, and supports `{{session_id}}`

cc-router never clobbers a header the client already sent: `set_headers` only
fills a header that's **missing**. So if you ever set one client-side (e.g. via
`ANTHROPIC_CUSTOM_HEADERS`), that value wins; otherwise cc-router supplies it.
(Auth is the exception — `bearer_env` always overwrites `Authorization`, so a
provider key can't leak past it.)

Header values may contain the placeholder **`{{session_id}}`**, which cc-router
fills from the request body — no client-side configuration. It reads the
per-session id Claude Code embeds in `metadata.user_id` (itself a JSON string:
`{"device_id":…,"account_uuid":…,"session_id":…}`). The value is constant for
every turn of one CC session — main loop, subagents, and Haiku background calls
all share it — and changes only on a new session.

That makes it the right key for `x-session-affinity`: a cache-aware backend
(Fireworks) treats the header as an opaque sticky key — *same value → same
backend*. Pinning per session means all the turns that share a growing cacheable
prefix land on one warm backend, while a different session gets a different key
and spreads across backends. A single static value would instead funnel *every*
session — and every concurrent CC agent you're running — onto one backend, whose
cache then thrashes between all those unrelated prefixes.

> Why per-session and not per-directory? The cwd would group sessions of the
> same project, but it only exists as free text inside the system prompt (fragile
> to parse), and prompt-cache TTLs are short (~5 min) — so cross-session warmth
> rarely survives anyway. `session_id` is structured, foolproof, and captures the
> warmth that actually exists: an active session's rapid back-and-forth. If a
> request carries no `metadata.user_id` (bodyless preflights, the `quota` probe),
> the token resolves to empty and no affinity header is sent.

### Transforms

| Name | Replicates | Status |
|---|---|---|
| `strip_cache_control` | `DISABLE_PROMPT_CACHING=1` — recursively deletes every `cache_control` key | ✅ working |
| `strip_attribution` | `CLAUDE_CODE_ATTRIBUTION_HEADER=0` — removes the billing-header system block | ✅ working |
| `strip_metadata` | (PII minimization) — drops the `metadata` object (`user_id` = device/account/session ids) so third-party upstreams don't receive identifiers they don't need; session affinity still works since `session_id` is read into the header first | ✅ working |
| `strip_thinking` | removes "thinking"/"redacted_thinking" blocks from the requests, they stay in Claude Code's context | ✅ working |

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

### To strip thinking blocks only

If you want to experiment stripping thinking blocks to save some tokens, you can use config in `config.passthru.json`:
```
{
  "listen": "0.0.0.0:8787",
  "routes": [
    {
      "match": ["*"],
      "upstream": "https://api.anthropic.com",
      "auth": { "mode": "passthrough" },
      "transforms": ["strip_thinking"]
    }
  ]
}
```

It seems to work. One caveat is that it seems to mess up Claude Code's context % calculations. When choosing a model, add `[1m]`
explicitly so it will help to correct the calculations to some degree. Other than that, it seems to work without visible negative
impact. If you look at the quality test results of Anthropic models with and without thinking, they differ quite negligibly.
And we are only removing the thinking blocks in requests, not disabling thinking. How much do we save the tokens? Hard to tell
without accurate benchmarking.

## Run

```bash
go build && CC_ROUTER_CONFIG=config.json FIREWORKS_API_KEY=fw_… ./cc-router
```

Point Claude Code at it (no cache env vars needed — cc-router handles them):

```bash
export ANTHROPIC_BASE_URL="http://127.0.0.1:8787"
```

## Auth & Pro/Max subscription

Auth passes through by default. Crucially, **a Pro/Max subscription works through
this proxy**: with `ANTHROPIC_BASE_URL` pointed at cc-router, Claude Code still
sends its subscription OAuth token (`Authorization: Bearer sk-ant-oat01…`) — it
does *not* demand an API key. So one endpoint can serve both:

- **Anthropic route** (`auth.mode: passthrough`) forwards the OAuth token
  unchanged to real `api.anthropic.com` → runs on your subscription, no API billing.
- **Other routes** (`auth.mode: bearer_env`) **overwrite** `Authorization` with the
  provider key and drop `x-api-key`, so the subscription token is never sent to a
  third party.

> The subscription token only ever reaches real Anthropic, byte-unchanged — it is
> not used to power any other provider. Confirm acceptance with one Opus round-trip
> through the passthrough route.

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
| **Which backend** | `x-session-affinity: {{session_id}}` (`set_headers`) | pins each CC session to one server that *might* have a warm cache |
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

## Security model

cc-router sits in the request path holding your Anthropic subscription token and
provider API keys, and sees every prompt and `metadata.user_id`. The design
keeps that from leaking; the key invariants are **enforced at config load** (it
refuses to start on a violation) rather than left to convention:

- **The client credential only ever reaches Anthropic.** `passthrough` forwards
  the inbound `Authorization`/`x-api-key` unchanged, so it is allowed *only* to
  `*.anthropic.com`. Any third-party/local upstream must use `bearer_env` or
  `none`, both of which **strip the client's auth first** — so your subscription
  OAuth token can never ride along to Fireworks et al., even if the provider key
  env var is unset (you'd get an unauthenticated request, never a leak).
- **No cleartext credentials.** Non-loopback upstreams must be `https`; plaintext
  `http` is allowed only to a `localhost` box.
- **No header injection from request bodies.** Values templated from the body
  (e.g. `{{session_id}}`) are sanitized to visible ASCII and length-bounded
  before being placed in a header.
- **No client auth on the listener.** cc-router authenticates *to* upstreams, not
  callers. Bind to `127.0.0.1` (the default); it warns loudly on a non-loopback
  bind because anyone who can reach the port could spend your keys.
- **Debug capture is a plaintext PII sink.** When enabled it writes full request
  bodies (prompts, code, metadata) to `capture/` (created `0700`, files `0600`).
  It is off by default — keep it to localhost and delete `capture/` when done.

## Notes & caveats

- **Unofficial.** Relies on Claude Code's current header/body behavior; a future
  version could change it.
- **Single-tenant by design.** No cross-user isolation — run your own instance on
  localhost / a trusted network. Multi-tenant routing is a job for a full gateway
  (e.g. Bifrost), not this.

## License

MIT — see [LICENSE](LICENSE).
