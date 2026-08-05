# agentic

> Language: **English** · [한국어](./README.ko.md)

A minimal agentic coding assistant in Go — an Anthropic Messages API + tool-use loop.
Runs against the **official Anthropic API** or any Anthropic-compatible endpoint such as
**GLM Coding Plan**.

Design docs live in [`docs/`](./docs) (in Korean). Notably:
- [docs/01-architecture.md](./docs/01-architecture.md) — planning → loop flow diagram
- [docs/04-go-skeleton.md](./docs/04-go-skeleton.md) — the loop skeleton
- [docs/06-provider-config.md](./docs/06-provider-config.md) — base_url / api_key / model + GLM compatibility

## Structure

```
main.go              entry: .env auto-load → planning + streaming + tool execution (+ record, prompt arg)
agent/
  agent.go           Agent + tool-use loop (concurrent tools) + LLMClient seam + Approver gate
  planner.go         Planner interface + LLMPlanner (explicit planning phase)
  compaction.go      manual context compaction (summarize old turns when over the token budget)
  tools.go           read_file / run_command(+background) tools + path sandboxing (safePath)
  cctools.go         Claude-Code-style tools: write/edit/multi_edit/notebook_edit/glob/grep/list_files/web_fetch/todo_write
  shells.go          background bash: ShellRegistry + bash_output/kill_shell (process-group kill)
  subagent.go        Task subagent: NewTaskTool + NewSubagentRunner (one level of delegation)
  client.go          Anthropic Go SDK → LLMClient adapter (streaming + base_url/key/model injection)
  recorder.go        Recorder (capture real responses) + ReplayClient (replay) — for offline tests
  *_test.go          offline tests: loop, concurrency, streaming, planning, compaction, approver, tools, real-response replay
mcp/                 MCP client: connect external stdio/HTTP/SSE MCP servers, wrap tools as agent.Tool (GLM-compatible, client-side)
tui/                 Claude-Code-style REPL (bubbletea + lipgloss + glamour)
  tui.go             Run() entry — builds agent + wires hooks
  model.go           Update/View: banner, input, streaming markdown, tool spinner/✓✗, slash commands
  styles.go          lipgloss styles + glamour markdown rendering
config/              loads AGENT_CONFIG (JSON) — system prompt, model, disable tools
testdata/glm_hello/  real GLM request/response — read_file scenario (NNN_request/response.json)
testdata/glm_write/  real GLM request/response — write + read_file scenario
examples/mcp-echo/   minimal stdio MCP server (echo tool) for verification — go build -o /tmp/mcp-echo ./examples/mcp-echo
.env / .env.example  provider settings
```

## Features

- **Explicit planning** — before the loop, one no-tools call produces a step-by-step plan injected into the execution context (`WithPlanner` / `WithOnPlan`). See [docs/05](./docs/05-design-details.md).
- **Streaming output** — per-token callback (`WithOnText`). `AnthropicClient` uses `Messages.NewStreaming` + `Accumulate` to assemble the full response while forwarding only text deltas in real time.
- **Concurrent tool calls** — multiple `tool_use` blocks in one assistant turn run **in parallel**; results are returned in **a single user message** (the pattern that keeps the model issuing parallel calls).
- **Manual context compaction** — when the estimated input tokens exceed the budget, older (assistant, tool_result) pairs are replaced with an LLM summary (`WithMaxContextTokens` / `AGENT_MAX_CONTEXT_TOKENS`). GLM has no server-side compaction, so we do it ourselves; pairs are summarized **whole** so `tool_use`/`tool_result` pairing stays valid.
- **Claude-Code-style tool set** — `CCTools(base)` registers the 13 tools below at once. All file tools are sandboxed to `base` (`safePath`).
- **Approval (ask) gate** — `WithApprover` gates each tool_use before it runs; a denial (with reason) is fed back to the model as the tool_result.
- **Task subagent** — the `task` tool delegates independent work to a subagent in its own context (one level deep). Useful for parallel fan-out.
- **MCP client integration** — auto-discovers tools from external stdio/HTTP/SSE MCP servers (filesystem/git/DB, …). **Client-side**, so it works on GLM too (independent of Anthropic's server-side `mcp_toolset`). Uses the official Go SDK (`go-sdk`).
- **Claude-Code-style TUI REPL** — an interactive terminal built with `bubbletea` + `lipgloss` + `glamour`. **Multiline input** (textarea, `Enter` to submit · `Ctrl+J` for newline, box grows with content), streaming markdown, tool-call spinner/✓✗, **file-change diff view** (colored unified diff on write/edit/multi_edit, capped at 60 lines), **permission picker** (`allow`/`deny` modal for write/edit/multi_edit/run_command — wired to `WithApprover`), `Ctrl+C` interrupt/quit, slash commands (`/help` `/clear` `/save` `/resume` `/exit`) + **Tab completion**. Launch with `go run .` (no args).
- **Session save/resume** — `/save` and `/resume` persist the conversation as JSON (`AGENT_SESSION`, default `.agentic/session.json`); **restart the process and continue the same conversation**.
- **Provider-agnostic** — works on Anthropic first-party and GLM Coding Plan. No reliance on beta-only features (server compaction, web_search, …).

### Tool list (mirrors Claude Code CLI)

| Tool | Notes |
|---|---|
| `read_file` · `write` · `edit` · `multi_edit` | file read/create/single str_replace/batch str_replace |
| `notebook_edit` | `.ipynb` cell source replace/insert (preserves other fields) |
| `run_command` · `bash_output` · `kill_shell` | shell exec + **background** (process-group kill) |
| `glob` (`**`) · `grep` (regex) · `list_files` | search |
| `web_fetch` | URL → text (HTML tags stripped) |
| `todo_write` | task list tracking |
| `task` | subagent delegation (registered separately) |
| `<server>__<tool>` | tools exposed by MCP servers (auto-registered) |

## Config file

Set `AGENT_CONFIG` to a JSON file to override defaults/env vars:

```jsonc
{
  "system_prompt": "You are a … assistant. …",
  "model": "glm-5.2",
  "max_context_tokens": 80000,
  "disable_tools": ["run_command", "task"]
}
```

- `system_prompt` · `model` · `max_context_tokens` override their defaults.
- Listing a name in `disable_tools` turns that tool off (unregistered from the agent).

## MCP servers

Auto-imports tools from stdio · **Streamable HTTP** · **SSE** MCP servers. Unlike Anthropic's
server-side `mcp_toolset`, we **connect directly** (`go-sdk`), so it works on GLM too. Point
`AGENT_MCP_CONFIG` at a Claude-Desktop-style file — `command` (stdio) or `url` (HTTP, Streamable by
default; SSE-only servers use `"transport":"sse"`):

```jsonc
// mcp.json
{ "mcpServers": {
    "fs":    { "command": "npx", "args": ["-y", "@modelcontextprotocol/server-filesystem", "."] },
    "echo":  { "command": "/tmp/mcp-echo" },
    "remote":{ "url": "https://mcp.example.com/mcp" },
    "old":   { "url": "https://legacy.example.com/sse", "transport": "sse" }
}}
```
```bash
AGENT_MCP_CONFIG=./mcp.json go run .
```

Each server's tools are namespaced `"<server>__<tool>"` to avoid collisions (e.g. `fs__read_file`).
A server that fails to connect is skipped with a warning. Verification server: `go build -o /tmp/mcp-echo ./examples/mcp-echo`.
If a server also exposes **resources**/**prompts**, `<server>__read_resource` (URI→body) and
`<server>__get_prompt` (name+args→rendered) tools are auto-registered (available list shown in the tool
description). The resource list is **auto-summarized into the system prompt** so the model can pull it on its own.

## Run

If a `.env` exists it's auto-loaded at startup (godotenv). Already-set env vars take precedence.

```bash
# After filling .env (see .env.example):
#   ANTHROPIC_API_KEY=<your GLM key from open.bigmodel.cn>
#   ANTHROPIC_BASE_URL=https://open.bigmodel.cn/api/anthropic
#   AGENT_MODEL=glm-5.2

go run .                       # no args → interactive TUI REPL (Claude Code style)
go run . ask "summarize main.go"   # prompt arg → run once and exit (also used for recording)
```

**TUI REPL** (`go run .`): welcome banner + input box, token-by-token streaming answer (glamour
markdown render), tool-call lines (`⏺ read_file path=…` → `✓`/`✗`), spinner. Commands: `/help` `/clear`
`/exit`, `Ctrl+C` to quit, `PgUp/PgDn` to scroll. (Run in a terminal — this environment has no TTY, so it
can't be launched here.)

For the official Anthropic API, leave `ANTHROPIC_BASE_URL` empty and set `AGENT_MODEL=claude-opus-5`.

> Quickest way to check the endpoint answers before writing code:
> ```bash
> curl https://open.bigmodel.cn/api/anthropic/v1/messages \
>   -H "x-api-key: $ANTHROPIC_API_KEY" -H "anthropic-version: 2023-06-01" \
>   -H "content-type: application/json" \
>   -d '{"model":"glm-5.2","max_tokens":256,"messages":[{"role":"user","content":"hi"}]}'
> ```

## Tests

```bash
go test ./...     # offline: loop + concurrent tools + streaming + planning + sandbox + real-response replay
go build ./...    # compile check
```

### Offline tests = real responses (record/replay)

Record a real provider call once; afterwards tests replay that data **with no network**, validating the
exact real response shapes.

```bash
# 1) Record (needs provider key, once) — setting AGENT_RECORD_DIR wraps the client in a Recorder.
#    A prompt arg lets you record different scenarios into different directories.
AGENT_RECORD_DIR=agent/testdata/glm_hello go run .                 # read_file scenario
AGENT_RECORD_DIR=agent/testdata/glm_write go run . <other prompt>  # write+read scenario

# 2) Replay (no network) — replay_test.go replays testdata/<dir>/*_response.json in order
go test ./agent/ -run 'TestRun_replayRealGLM|TestRun_replayGLMWrite' -v
```

Currently captured real responses: `testdata/glm_hello` (planner→read_file→final, 3 calls),
`testdata/glm_write` (planner→write→read_file→final, 4 calls). Recording files (`NNN_request.json` /
`NNN_response.json`) contain **no API key** (the key travels in the HTTP header, not the request body) →
safe to commit.

## Build / Release

The `Makefile` does local builds and release cross-compilation. The version is stamped via
`-ldflags -X main.version=…` and shown by `agentic --version`.

```bash
make build                       # dist/agentic (current platform)
make release                     # 5 cross-binaries into dist/ (static, CGO off)
  #   agentic-linux-amd64 / -linux-arm64
  #   agentic-darwin-amd64 / -darwin-arm64
  #   agentic-windows-amd64.exe
make release-darwin-arm64        # single target
VERSION=v0.1.0 make build        # version stamp (auto-detects git tag)
```

### Release automation (goreleaser)

`.goreleaser.yaml` + `.github/workflows/release.yml` — on a `v*` tag push, automatically produces
cross-binaries + **checksums.txt** + a GitHub Release + a **Homebrew formula** (`dh-kam/homebrew-tap`).

```bash
goreleaser release --clean            # local release (needs git + GITHUB_TOKEN)
goreleaser release --snapshot --clean # dry-run, no upload
goreleaser check                      # validate config
git tag v0.1.0 && git push --tags     # CI builds the release → brew install dh-kam/tap/agentic
```

> Pushing the Homebrew formula requires the CI secret `HOMEBREW_TAP_GITHUB_TOKEN` (a PAT with write
> access to the tap repo). Without it, binaries + checksum + Release are still created (only the formula is skipped).

## GLM compatibility summary

The GLM-compatible endpoint supports only the **common subset** of the Messages API → messages, system,
streaming, and **tool use** work, but adaptive thinking / server compaction / server tools
(web_search, code_execution) / Files API / Managed Agents **do not**. So this project uses only the
`Messages.NewStreaming` + `tools` manual loop (no beta features; streaming is in the GLM-compatible
subset) and implements compaction **ourselves** (see the feature above). Details in [docs/06](./docs/06-provider-config.md).
