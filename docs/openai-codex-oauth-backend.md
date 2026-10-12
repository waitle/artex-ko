# OpenAI Codex OAuth backend

`openai-codex` is separate from the existing OpenAI API-key and Responses formats.
It uses one **server-owned ChatGPT account shared by all Codex profiles**. Treat it
as a personal coding-assistant integration, not a per-user SaaS authentication
mechanism. Existing API-key modes are unchanged.

## Login and configuration

All endpoints require the existing ARTEX API authentication:

- `POST /api/llm/oauth/codex/start`: `202` with `verification_url`, `user_code`,
  `expires_at`, and `poll_interval` (seconds).
- `POST /api/llm/oauth/codex/poll`: `200` with `connected` and optional `pending`.
  Poll no faster than the interval. Login expires after 15 minutes.
- `GET /api/llm/oauth/codex/status`: read-only connection/expiry metadata.
- `DELETE /api/llm/oauth/codex`: removes local credentials, cancels active OAuth
  requests, clears pending login, invalidates provider/task caches, and returns
  `{"connected":false}`. This is local logout, not an upstream account revocation.

The browser opens `https://auth.openai.com/codex/device` to approve the displayed
code. Device login and token exchange are native Go requests; Node, Codex CLI,
localhost callbacks, pasted tokens and Hermes/CLI credential imports are not used.
If the device endpoint rejects the server (including HTTP 403), ARTEX reports the
failure; it does not invent a code or silently switch authentication mechanisms.

Choose `format: "openai-codex"`, leave API key/base URL/proxy empty, and select an
exact model from the authenticated catalog returned by `POST /api/llm/models`
with `{"provider":"openai-codex"}`. The server fetches the fixed Codex `/models`
endpoint; no guessed/offline model catalog is substituted. Saving profiles is
allowed before login, but runtime readiness requires an owned connected account.
Environment bootstrap can use `ARTEX_LLM_PROVIDER=openai-codex` and an explicit
`ARTEX_LLM_MODEL`; it still requires this server's own OAuth login. With no active
DB profile, a successful login/poll restores this environment configuration,
including after logout and re-login. An active DB profile always takes precedence;
a disconnected Codex profile cannot be activated (HTTP 503), leaving the previous
selection unchanged. Logout clears the global Codex runtime and chat fallback;
revoked cached agents report unavailable rather than using another global model.

## Credentials and transport

The canonical file is `codex-oauth.json` beside `jwt.key` in the server key
directory by default. Set `ARTEX_CODEX_AUTH_DIR` to a private persistent directory
for containers; mount the directory, not a single credential file, because token
rotation uses atomic rename. The directory must remain **outside the browsable
data tree**. Symlink-resolved directories are
checked; OAuth is disabled if key and data directories overlap. Credentials are
stored with mode `0600`, synced and atomically renamed. Non-regular files and
files with unsafe permissions are rejected. Credential values never appear in
API status responses or backend errors/raw-wire recording.

**Only one ARTEX process may own this file.** An in-process mutex serializes
refresh and immediately persists rotating refresh tokens. Multiple agents and
profiles share the same owner; multiple independent server processes/file owners
are not supported. Do not copy the file to another live process or CLI.

Every request resolves current credentials instead of caching a bearer token in
a provider. Logout cancels the current credential epoch and prevents reuse of
cached providers. Expired tokens refresh through the fixed OAuth issuer.
Permanent refresh rejection disconnects the local account. Base URL, API key and
proxy overrides are rejected for OAuth profiles. Redirects are not followed and
only the fixed Codex Responses destination is authorized. OAuth clients bypass
ambient proxy environment variables.

## SDK reuse and deliberate limitations

The adapter uses **Norma v0.4.3's existing Responses provider**, tool conversion,
SSE parser, rate limiter and retries, with a small HTTP request-normalizing
transport. Requests force `stream:true`, `store:false`, and a present
`instructions` field; unsupported output limits/sampling/stop fields are removed.
Local nonstreaming `Complete` collects normalized SDK stream events, including
text, thinking summaries, tools and usage. Failed or unterminated streams return
an error rather than a successful partial completion. Upstream HTTP/error text is
sanitized so echoed credential values do not enter logs/API errors.

Tool execution is gated by a bounded normalized-event suffix buffer: text before
its first tool event still streams immediately, but that tool and **every later
event** (including text, thinking, sequential tools and usage) are retained in
order until the SDK stream exhausts successfully after a validated
`response.completed` terminal. Every tool's complete argument JSON must validate
before any retained event is released; missing/invalid arguments are rejected,
never substituted with `{}`. Failed, incomplete, truncated, errored, cancelled,
or over-budget streams discard the suffix and expose only a sanitized error.
The suffix is capped at **4 MiB of accounted storage and 4,096 events**; accounting
includes retained strings, a conservative fixed event allowance, and the extra
argument-validation copy. This is an adapter buffer budget, not a process RSS
limit (the SDK parser and bounded SSE-frame/JSON validation allocations also
exist). Tools and text after the first tool therefore lose incremental latency:
tools cannot begin while the upstream response is still arriving. Requests still
force `parallel_tool_calls:false`; no SDK fork or dependency change is required.
Cancellation is checked before release and between released events; as with any
executor, cancellation cannot undo a tool already dispatched after successful
validation.

- `include:["reasoning.encrypted_content"]` is requested, but **Norma v0.4.3 does
  not expose encrypted reasoning in normalized events and drops thinking blocks
  when building Responses history**. Encrypted reasoning cannot be preserved or
  replayed without an upstream SDK extension. Full text/tool history remains
  stateless; no response IDs/item references are used.
- `parallel_tool_calls:false` is forced: Norma's normalized tool-input events do
  not retain output indexes, so interleaved parallel arguments cannot be safely
  attributed. Sequential tool calls remain supported.
- Codex ignores profile max-output-token and sampling controls stripped by the
  transport. Backend subscription quotas/capabilities still apply.
- No live login, model discovery or OpenAI request was performed during automated
  verification. Tests use local `httptest` fixtures only. Database-dependent
  integration tests are skipped with empty database environment variables;
  schema migration changes have not been exercised against a live database.

## Verification

From the repository root:

```sh
TEST_DATABASE_URL='' ARTEX_PG_DSN='' ARTEX_CONFIG=/path/to/missing-config go test ./...
TEST_DATABASE_URL='' ARTEX_PG_DSN='' ARTEX_CONFIG=/path/to/missing-config \
  go test -race ./codexoauth ./agent ./server ./db \
  -run 'Test(Codex|Device|ProviderForces|Models|Concurrent|Refresh|Logout|StreamTool|FailedOr|RevokedCodex)' -count=1
```
