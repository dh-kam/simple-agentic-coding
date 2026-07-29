# 6단계: Provider 설정 (base_url / api_key / model)

에이전트는 LLM 호출을 **Anthropic Messages API** 형태로 보낸다.
따라서 Messages API와 호환되는 엔드포인트라면 어떤 provider든 쓸 수 있다.
GLM Coding Plan도 호환 엔드포인트를 제공하므로, **본인의 GLM 키로 그대로 실행**된다.

---

## 세 가지 설정값

에이전트는 아래 세 값을 환경변수로 받는다.

| 환경변수 | 의미 | Anthropic 기본값 | GLM 예시 |
|---|---|---|---|
| `ANTHROPIC_API_KEY` | API 키 | Anthropic 콘솔 키 | open.bigmodel.cn 발급 GLM 키 |
| `ANTHROPIC_BASE_URL` | API 호스트 | `https://api.anthropic.com` | `https://open.bigmodel.cn/api/anthropic` |
| `AGENT_MODEL` | 모델 ID | `claude-opus-5` | `glm-4.6` |

> SDK가 `ANTHROPIC_API_KEY` / `ANTHROPIC_BASE_URL`을 자동으로 읽는다.
> `AGENT_MODEL`은 코드에서 `Model:` 필드로 직접 전달한다.
> (Anthropic Go SDK는 `anthropic.Model`이 `string` alias라 어떤 모델 ID든 허용.)

## Go 클라이언트 구성

```go
import (
    "github.com/anthropics/anthropic-sdk-go"
    "github.com/anthropics/anthropic-sdk-go/option"
)

client := anthropic.NewClient(
    option.WithAPIKey(os.Getenv("ANTHROPIC_API_KEY")),
    option.WithBaseURL(os.Getenv("ANTHROPIC_BASE_URL")),
)
// 루프 안에서: Model: os.Getenv("AGENT_MODEL")
```

## smoke test (curl)

코드를 짜기 전에 키/엔드포인트가 답하는지 가장 빠르게 확인:

```bash
curl https://open.bigmodel.cn/api/anthropic/v1/messages \
  -H "x-api-key: $ANTHROPIC_API_KEY" \
  -H "anthropic-version: 2023-06-01" \
  -H "content-type: application/json" \
  -d '{"model":"glm-4.6","max_tokens":256,"messages":[{"role":"user","content":"안녕"}]}'
```

200 + 답이 오면 Claude Code CLI도 같은 환경변수 조합으로 GLM 위에서 도는 것과 같은 경로다.

---

## 호환성 — GLM이 지원하는 것 / 아닌 것

GLM 호환 엔드포인트는 Anthropic API의 **공통 서브셋**만 구현한다.

| 기능 | GLM | 비고 |
|---|---|---|
| messages · system prompt · streaming | ✅ | |
| **tool use** (`tools` + `tool_use` 응답) | ✅ | GA Messages API 일부 → 본 에이전트가 쓰는 전부 |
| adaptive thinking · effort · compaction · context editing · fast mode · refusal fallbacks | ❌ | Anthropic 전용 beta |
| 서버 도구 (`web_search_*`, `code_execution_*`) | ❌ | |
| Files API · Managed Agents (`/v1/agents` 등) | ❌ | |

### 그래서 설계가 정해진다

- ✅ **수동 tool-use 루프**(`client.Messages.New` + `tools`)를 쓴다 → core API + 도구만 쓰므로 GLM에서도 동일 동작. 본 프로젝트가 정확히 이 패턴.
- ⚠️ **`BetaToolRunner` / beta-only 기능은 피한다** (또는 쓰기 전에 GLM에서 테스트). beta 헤더 의존성이 있어 GLM이 요청을 거부할 수 있다.
- ⚠️ Anthropic 전용 기능(컨텍스트 압축, adaptive thinking 등)에 기대지 않는다 → 컨텍스트 관리를 **직접** 구현한다 ([05-design-details.md](./05-design-details.md) 참조).

---

## 참고: "Agent SDK" vs "Anthropic Go SDK"

이름이 비슷해 헷갈리기 쉽다:

| 이름 | 정체 | Go |
|---|---|---|
| **Claude Agent SDK** (`@anthropic-ai/claude-agent-sdk`) | Claude Code 자체를 라이브러리로 포장 (built-in 도구·루프·훅 포함) | ❌ TS/Python 전용 |
| **Anthropic Go SDK** (`github.com/anthropics/anthropic-sdk-go`) | Messages API + tool use | ✅ ← **본 프로젝트가 사용** |

Go에서는 Agent SDK가 없으므로, Messages API + tool use 루프를 직접 도는다.
그게 [04-go-skeleton.md](./04-go-skeleton.md)와 프로젝트 루트 코드가 하는 일이다.
