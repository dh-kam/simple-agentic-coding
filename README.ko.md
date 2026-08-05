# agentic

> 언어: [English](./README.md) · **한국어**

Go로 만든 최소한의 agentic 코딩 어시스턴트 — Anthropic Messages API + tool use 루프.
**Anthropic 정식 API** 또는 **GLM Coding Plan** 같은 Anthropic 호환 엔드포인트 어디서든 동작한다.

설계 문서는 [`docs/`](./docs) (한국어). 특히:
- [docs/01-architecture.md](./docs/01-architecture.md) — planning → 루프 흐름도
- [docs/04-go-skeleton.md](./docs/04-go-skeleton.md) — 루프 스켈레톤
- [docs/06-provider-config.md](./docs/06-provider-config.md) — base_url / api_key / model + GLM 호환

## 구조

```
main.go              진입점: .env 자동 로드 → planning + streaming + 도구 실행 (+ 녹화 옵션, 프롬프트 인자)
agent/
  agent.go           Agent + tool-use 루프(동시 도구 실행) + LLMClient seam + Approver 게이트
  planner.go         Planner 인터페이스 + LLMPlanner (명시적 planning 단계)
  compaction.go      수동 컨텍스트 compaction (토큰 한도 초과 시 오래된 교환을 요약)
  tools.go           read_file / run_command(+background) 도구 + 경로 샌드박싱(safePath)
  cctools.go         Claude-Code-style 도구: write/edit/multi_edit/notebook_edit/glob/grep/list_files/web_fetch/todo_write
  shells.go          백그라운드 bash: ShellRegistry + bash_output/kill_shell (프로세스 그룹 kill)
  subagent.go        Task 서브에이전트: NewTaskTool + NewSubagentRunner (1단계 위임)
  client.go          Anthropic Go SDK → LLMClient 어댑터 (streaming + base_url/key/model 주입)
  recorder.go        Recorder(실제 응답 녹화) + ReplayClient(재생) — 오프라인 테스트용
  *_test.go          루프·동시성·스트리밍·planning·compaction·approver·도구·실제 응답 재생 오프라인 검증
mcp/                 MCP 클라이언트: 외부 stdio/HTTP/SSE MCP 서버 접속 → 도구를 agent.Tool 로 랩 (GLM 호환, 클라이언트 사이드)
tui/                 Claude-Code-style REPL (bubbletea + lipgloss + glamour)
  tui.go             Run() 진입 — 에이전트 빌드 + 훅 연결
  model.go           Update/View: 배너·입력·스트리밍 마크다운·도구 호출 스피너/✓✗·슬래시 명령
  styles.go          lipgloss 스타일 + glamour 마크다운 렌더
config/              AGENT_CONFIG(JSON) 로드 — 시스템 프롬프트·model·도구 비활성화
testdata/glm_hello/  실제 GLM 요청/응답 — read_file 시나리오 (NNN_request/response.json)
testdata/glm_write/  실제 GLM 요청/응답 — write + read_file 시나리오
examples/mcp-echo/   검증용 최소 stdio MCP 서버(echo 도구) — go build -o /tmp/mcp-echo ./examples/mcp-echo
hello.txt            read_file 테스트용 픽스처
.env / .env.example  provider 설정
```

## 기능

- **명시적 planning** — 루프 진입 전, 도구 없는 단일 호출로 단계별 계획을 생성해 실행 컨텍스트에 주입 (`WithPlanner` / `WithOnPlan`). 자세한 건 [docs/05](./docs/05-design-details.md).
- **스트리밍 출력** — 응답을 토큰 단위로 콜백 (`WithOnText`). `AnthropicClient`는 `Messages.NewStreaming` + `Accumulate`로 전체 응답을 조립하면서 텍스트 델타만 실시간 전달.
- **동시 tool call** — 한 assistant 응답의 여러 `tool_use` 블록을 **병렬** 실행하고, 결과를 **한 user 메시지**로 묶어 반환 (이게 모델이 계속 병렬 호출을 하게 만드는 올바른 패턴).
- **수동 컨텍스트 compaction** — 입력 토큰 추정치가 한도를 넘으면 오래된 턴을 LLM 요약으로 교체 (`WithMaxContextTokens` / `AGENT_MAX_CONTEXT_TOKENS`). GLM은 서버 compaction이 없어서 직접 구현; **라운드 경계**에서만 잘라 `tool_use`/`tool_result` 페어링을 유지한다 — 한 라운드는 assistant 메시지 + 그에 답하는 tool_result 전부이므로 병렬 tool call(1+N개 메시지)에서도 안전하다.
- **Claude-Code-style 도구 세트** — `CCTools(base)` 가 아래 13개 도구를 한 번에 등록. 모든 파일 도구는 `base` 로 샌드박싱(`safePath`).
- **승인(ask) 게이트** — `WithApprover` 로 각 tool_use 실행 전 승인; 거부 시 그 사유를 tool_result 로 모델에 돌려보낸다. 대상은 `agent.IsMutating` 이 판정하며(파일 쓰기·셸·git), **알 수 없는 도구(MCP 등)는 기본적으로 승인 대상**이다.
- **영속 권한 규칙** — `.agentic/settings.json` 의 `allow_tools`/`deny_tools`/`mode`(`plan`·`auto-edit`·`full-auto`)가 프롬프트보다 먼저 적용된다. deny 가 allow 보다, 둘 다 mode 보다 우선한다. one-shot(`agentic ask`)은 답할 사람이 없으므로 규칙이 정하지 않은 도구는 실행되지만, `deny_tools` 와 `mode: "plan"` 은 동일하게 거부한다.
- **샌드박스** — 파일 도구는 `base` 안으로 제한된다(`safePath`). 경로의 모든 구성요소에서 symlink 를 해석하며, 아직 없는 파일도 존재하는 최상위 조상까지 거슬러 올라가 검사하고, 타깃이 없는 dangling symlink 는 거부한다. 쓰기는 `O_NOFOLLOW` 로 열어 검사와 write 사이에 symlink 가 끼어드는 race 를 막고, `.git/` 과 `.agentic/` 은 어떤 도구도 쓸 수 없다(hook·permission 규칙·skill 은 곧 임의 실행이다).
- **Task 서브에이전트** — `task` 도구가 독립 작업을 별도 컨텍스트의 서브에이전트에 위임(1단계까지만). 병렬 팬아웃에 사용. 서브에이전트는 부모의 **승인 게이트와 lifecycle hook 을 그대로 상속**한다.
- **MCP 클라이언트 연동** — 외부 stdio/HTTP/SSE MCP 서버(filesystem/git/DB 등)의 도구를 자동 발견해 에이전트에 붙인다. **클라이언트 사이드**라 GLM에서도 동작(Anthropic의 서버사이드 `mcp_toolset`과 무관). 공식 Go SDK(`go-sdk`) 사용.
- **Claude-Code-style TUI REPL** — `bubbletea` + `lipgloss` + `glamour` 로 인터랙티브 터미널. **멀티라인 입력**(textarea, `Enter` 전송 · `Ctrl+J` 줄바꿈, 박스가 내용에 맞게 확장), 스트리밍 마크다운, 도구 호출 스피너/✓✗, **파일 변경 diff 뷰**(write/edit/multi_edit 시 색칠된 unified diff, 60줄 캡), **권한 피커**(상태를 바꾸는 도구 호출 시 `allow`/`deny` 모달 — `WithApprover` 연동), `Ctrl+C` 실행 중단/종료, 슬래시 명령(`/help` `/clear` `/save` `/resume` `/undo` `/cost` `/status` `/exit`) + **Tab 자동완성**, `.agentic/commands/*.json` 사용자 정의 명령. 인자 없이 `go run .` 로 실행.
- **대화 세션 저장/불러오기** — `/save`·`/resume` 으로 대화 이력을 JSON(`AGENT_SESSION`, 기본 `.agentic/session.json`)에 영속화; **프로세스를 재시작해도 이전 대화를 이어**간다.
- **provider 무관** — Anthropic 정식 / GLM Coding Plan 모두 동작. beta 전용 기능(서버 compaction, web_search 등)에 기대지 않는다.

### 도구 목록 (Claude Code CLI 대응)

| 도구 | 비고 |
|---|---|
| `read_file` · `write` · `edit` · `multi_edit` | 파일 읽기/생성/단일 str_replace/배치 str_replace |
| `notebook_edit` | `.ipynb` 셀 source 교체·삽입 (필드 보존) |
| `run_command` · `bash_output` · `kill_shell` | 셸 실행 + **백그라운드**(프로세스 그룹 kill) |
| `glob`(`**`) · `grep`(정규식) · `list_files` | 탐색 |
| `web_fetch` | URL → 텍스트(HTML 태그 제거). SSRF 방어: 사설/loopback/link-local 차단 + 검증한 IP 로 dial 고정, 리다이렉트마다 재검증 |
| `web_search` · `git` · `git_commit` · `code_review` | 검색 · 허용목록 기반 git · 커밋 · diff 리뷰 |
| `todo_write` | 작업 목록 추적 |
| `task` | 서브에이전트 위임 (별도 등록) |
| `<server>__<tool>` | MCP 서버가 노출한 도구 (자동 등록) |

## 보안 경계와 알려진 한계

이 도구는 **운영자 본인이 구동하는 개인용 코딩 에이전트**를 전제로 한다. 신뢰 경계는 "모델 출력은 신뢰하지 않는다"이며, 저장소 내용·웹 페이지·MCP 서버 응답을 통한 prompt injection 을 상수로 가정한다.

강제되는 것:

- 파일 도구는 `base` 밖을 읽거나 쓰지 못한다. 모든 경로 구성요소에서 symlink 를 해석하고, 아직 없는 파일도 존재하는 최상위 조상까지 검사하며, dangling symlink 는 거부한다. 쓰기는 `O_NOFOLLOW`.
- `.git/` 과 `.agentic/` 은 어떤 도구로도 수정할 수 없다. hook·permission 규칙·skill 은 곧 다음 세션의 임의 실행이기 때문이다.
- `git` 도구는 subcommand allowlist 에 더해 인자까지 검사한다. `config`/`remote`/`stash` 는 실행 불가, `--output`·`--no-index`·`--file` 계열은 거부, 절대 경로·`..`·pathspec magic(`:/`)·`<rev>:<path>` 도 거부, base 가 하위 디렉토리면 `-- .` 로 한정한다.
- `web_fetch` 는 사설·loopback·link-local·CGNAT 등 비공개 대역을 차단하고, 검증한 IP 로 dial 을 고정하며, 리다이렉트마다 재검증·재고정한다.
- `task` 서브에이전트는 부모의 승인 게이트를 상속하고, `disable_tools` 도 그대로 적용받는다.

강제되지 않는 것 (설계상 또는 미해결):

- **`run_command` 는 임의의 셸 명령을 실행한다.** 승인 게이트가 유일한 방어선이므로, `deny_tools` 나 `mode: "plan"` 없이 승인을 누르는 순간 샌드박스는 의미가 없다.
- **one-shot(`agentic ask`)은 무인 모드다.** 규칙이 정하지 않은 도구는 프롬프트 없이 실행된다. 신뢰할 수 없는 입력을 다룰 때는 `.agentic/settings.json` 에 `mode: "plan"` 이나 `deny_tools` 를 두거나 TUI 를 쓸 것.
- **`AGENTS.md`/`CLAUDE.md` 는 쓰기 가능하다.** 편집이 정상 작업이라 막지 않았지만, 이 파일들은 다음 세션의 system prompt 에 들어간다. 즉 신뢰 경계 안쪽이다.
- **hardlink 는 막지 못한다.** base 안에 base 밖 파일을 가리키는 hardlink 가 미리 존재하면 쓰기가 그 파일을 덮어쓴다(`O_NOFOLLOW` 는 symlink 만 잡는다).
- **`web_fetch`/`web_search` 는 승인 없이 실행된다.** 아웃바운드 요청은 exfiltration 채널이므로, 민감한 저장소에서는 `deny_tools` 로 막을 것.
- **HAR 캡처는 기본 켜짐**이며 대화 전문(에이전트가 읽은 파일 내용 포함)을 `~/.agentic/hars/` 에 남긴다. API 키·쿠키는 마스킹되지만 소스 코드는 그대로다. `AGENT_HAR_DISABLE=1` 로 끌 수 있다.

## 설정 파일

`AGENT_CONFIG` 로 JSON 설정 파일을 지정하면 기본값/환경변수를 덮어쓴다:

```jsonc
{
  "system_prompt": "너는 … 어시스턴트다. …",
  "model": "glm-5.2",
  "max_context_tokens": 80000,
  "disable_tools": ["run_command", "task"]
}
```

- `system_prompt` · `model` · `max_context_tokens` 는 각 기본값을 오버라이드.
- `disable_tools` 에 이름을 적으면 해당 도구를 꺼준다(agent 에서 unregister).

## MCP 서버 연동

stdio · **Streamable HTTP** · **SSE** MCP 서버의 도구를 자동으로 가져온다.
Anthropic의 서버사이드 `mcp_toolset`과 달리 **직접 접속**(`go-sdk`)하므로 GLM에서도 동작한다.
Claude-Desktop 형식의 설정 파일을 `AGENT_MCP_CONFIG` 로 지정 — `command`(stdio) 또는
`url`(HTTP, 기본 Streamable; SSE 전용 서버는 `"transport":"sse"`):

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

각 서버의 도구는 `"<서버>__<도구>"` 로 이름공간이 붙어 충돌을 피한다(예: `fs__read_file`).
연결 실패한 서버는 경고만 찍고 건너뛴다. 검증용 서버: `go build -o /tmp/mcp-echo ./examples/mcp-echo`.
서버가 **resources**/**prompts** 도 노출하면 `<서버>__read_resource`(URI→본문) · `<서버>__get_prompt`(이름+인자→렌더) 도구도 자동 등록된다(사용 가능 목록은 도구 설명에 표시). 리소스 목록은 **시스템 프롬프트에 자동 요약 주입**돼 모델이 알아서 읽을 수 있다.

## 실행

`.env` 가 있으면 시작 시 자동 로드된다 (godotenv). 이미 설정된 환경변수가 우선한다.

```bash
# .env 에 값을 채운 뒤 (.env.example 참조):
#   ANTHROPIC_API_KEY=<open.bigmodel.cn 에서 발급한 GLM 키>
#   ANTHROPIC_BASE_URL=https://open.bigmodel.cn/api/anthropic
#   AGENT_MODEL=glm-5.2

go run .                      # 인자 없음 → 인터랙티브 TUI REPL (Claude Code 스타일)
go run . hello.txt를 읽고 요약  # 프롬프트 인자 → 한 번 실행 후 종료 (녹화에도 사용)
```

**TUI REPL** (`go run .`): 환영 배너 + 입력 상자, 토큰 단위 스트리밍 답변(glamour 마크다운 렌더),
도구 호출 라인(`⏺ read_file path=…` → `✓`/`✗`), 스피너. 명령: `/help` `/clear` `/exit`, `Ctrl+C` 종료,
`PgUp/PgDn` 스크롤. (터미널에서 실행 — 본 환경엔 TTY가 없어 여기서는 띄울 수 없습니다.)

Anthropic 정식 API를 쓸 땐 `ANTHROPIC_BASE_URL`을 비우고 `AGENT_MODEL=claude-opus-5`.

> 코드를 짜기 전 엔드포인트가 답하는지 가장 빠르게 확인:
> ```bash
> curl https://open.bigmodel.cn/api/anthropic/v1/messages \
>   -H "x-api-key: $ANTHROPIC_API_KEY" -H "anthropic-version: 2023-06-01" \
>   -H "content-type: application/json" \
>   -d '{"model":"glm-5.2","max_tokens":256,"messages":[{"role":"user","content":"안녕"}]}'
> ```

## 테스트

```bash
go test ./...     # 네트워크 없이: 루프 + 동시 도구 + 스트리밍 + planning + 샌드박스 + 실제 응답 재생
go build ./...    # 컴파일 검증
```

### 오프라인 테스트 = 실제 응답 기반 (record/replay)

실제 provider 호출을 한 번 녹화해 두면, 이후 테스트는 그 데이터를 재생해 **네트워크 없이**
실제 응답 형태를 그대로 검증한다.

```bash
# 1) 녹화 (provider 키 필요, 1회만) — AGENT_RECORD_DIR 를 주면 Recorder 로 감싼다.
#    인자로 프롬프트를 줄 수 있어 시나리오별로 다른 디렉토리에 녹화 가능.
AGENT_RECORD_DIR=agent/testdata/glm_hello go run .                       # read_file 시나리오
AGENT_RECORD_DIR=agent/testdata/glm_write go run . <다른 작업 프롬프트>    # write+read 시나리오

# 2) 재생 (네트워크 불필요) — replay_test.go 가 testdata/<dir> 의 *_response.json 을 순서대로 재생
go test ./agent/ -run 'TestRun_replayRealGLM|TestRun_replayGLMWrite' -v
```

현재 캡처된 실제 응답: `testdata/glm_hello`(planner→read_file→final, 3 호출),
`testdata/glm_write`(planner→write→read_file→final, 4 호출). 녹화 파일(`NNN_request.json` /
`NNN_response.json`)에는 API 키가 들어가지 않는다(키는 HTTP 헤더로만 가고 요청 본문에는 없다) → 커밋해도 안전.

## 빌드 / 배포

`Makefile` 로 로컬 빌드와 릴리스 크로스컴파일. 버전은 `-ldflags -X main.version=…` 로 찍히고
`agentic --version` 으로 확인한다.

```bash
make build                       # dist/agentic (현재 플랫폼)
make release                     # dist/ 에 5개 크로스바이너리 (static, CGO off)
  #   agentic-linux-amd64 / -linux-arm64
  #   agentic-darwin-amd64 / -darwin-arm64
  #   agentic-windows-amd64.exe
make release-darwin-arm64        # 단일 타깃
VERSION=v0.1.0 make build        # 버전 스탬프 (git tag 자동 감지)
```

### 릴리스 자동화 (goreleaser)

`.goreleaser.yaml` + `.github/workflows/release.yml` — `v*` 태그 푸시 시 자동:
크로스바이너리 + **checksums.txt** + GitHub Release + **Homebrew 포뮬러**(`dh-kam/homebrew-tap`).

```bash
goreleaser release --clean           # 로컬 릴리스 (git + GITHUB_TOKEN 필요)
goreleaser release --snapshot --clean # 업로드 없는 dry-run
goreleaser check                      # 설정 검증
git tag v0.1.0 && git push --tags    # CI가 릴리스를 만든다 → brew install dh-kam/tap/agentic
```

> Homebrew 포뮬러 푸시는 CI 시크릿 `HOMEBREW_TAP_GITHUB_TOKEN`(tap 리포지토리 쓰기 권한 PAT) 필요.
> 토큰이 없어도 바이너리+체인섬+Release는 생성된다(포뮬러만 스킵).

## GLM 호환성 요약

GLM 호환 엔드포인트는 Messages API **공통 서브셋**만 지원한다 → messages, system, streaming,
**tool use**는 되지만 adaptive thinking / 서버 compaction / 서버 도구(web_search, code_execution) /
Files API / Managed Agents 등은 **안 된다**. 그래서 이 프로젝트는 beta 기능 없이
`Messages.NewStreaming` + `tools` 수동 루프만 쓰고(streaming은 GLM 호환 서브셋에 포함),
compaction은 **직접 구현**했다(위 기능 참조). 자세한 건 [docs/06](./docs/06-provider-config.md).
