# Go 기반 Agentic 코딩 어시스턴트 — 설계 문서

LLM API 호출 + tool calling + planning + context 관리를 하나의 루프로 묶은
**tool-use loop (ReAct loop)** 기반 에이전트의 설계 가이드.

---

## 한 줄 요약

> **에이전트 = "LLM 호출"을 감싼 `while` 루프.**
> 루프 안에서 (1) 컨텍스트를 조립하고 → (2) LLM을 호출하고 →
> (3) 응답이 "도구 호출"이면 실행 후 결과를 다시 컨텍스트에 넣고 →
> (4) "최종 답변"이면 루프를 빠져나간다.

이 패턴은 Claude Code, Cursor, Devin 등 모든 현대 코딩 에이전트의 핵심 엔진이다.
"계획(planning)" 단계는 루프 진입 **전에 한 번** 별도로 돌릴 수도 있고,
루프 **안에서 LLM 스스로** 하게 둘 수도 있다. 두 방식 모두 [05-design-details.md](./05-design-details.md)에서 다룬다.

---

## 문서 목차

| 문서 | 내용 |
|---|---|
| [01-architecture.md](./01-architecture.md) | 전체 아키텍처: planning → 메인 루프 흐름도 |
| [02-llm-call.md](./02-llm-call.md) | 한 번의 LLM 호출의 입출력 + 프롬프트/JSON 예시 |
| [03-sequence-example.md](./03-sequence-example.md) | 구체적 요청에 대한 시퀀스 다이어그램 |
| [04-go-skeleton.md](./04-go-skeleton.md) | Go 스켈레톤 코드 (루프 구현) |
| [05-design-details.md](./05-design-details.md) | 디자인 디테일 + planning 방식 + 함정 정리 |
| [06-provider-config.md](./06-provider-config.md) | base_url / api_key / model env 설정 + GLM 호환 |

---

## Provider 설정 (GLM Coding Plan 등)

이 에이전트는 **Anthropic Messages API 호환 엔드포인트**를 쓰면 어떤 provider든 동작한다.
GLM Coding Plan도 호환 엔드포인트(`https://open.bigmodel.cn/api/anthropic`)를 제공하므로,
본인의 GLM API 키로 그대로 실행할 수 있다. 설정 방법과 호환성 제약은
[06-provider-config.md](./06-provider-config.md) 참조.

---

## 다음 단계 제안

1. **루프 1개 + 도구 1개(`read_file`)** 만 짜서 끝까지 돌려본다 ("hello.txt 읽어줘" 정도).
2. `run_command` 추가, 여러 `tool_calls` 동시 처리 추가.
3. streaming, planning, 컨텍스트 압축 순으로 확장.

> 처음부터 복잡하게 가면 디버깅이 어렵다. 1번이 끝까지 동작하는 것을 먼저 확인할 것.
