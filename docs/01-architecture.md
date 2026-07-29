# 1단계: 전체 아키텍처 (planning → 루프)

## 핵심 분기 — 이것이 전부다

에이전트의 본질은 다음 한 가지 분기에 있다:

> LLM 응답에 `tool_calls`가 **있으면** → 도구를 실행하고 루프를 계속.
> **없으면** → LLM이 "다 했다"고 판단한 것이므로 루프를 종료.

이 `while` 루프가 에이전트의 전부다. 아래 다이어그램은 이 루프를
planning 단계와 함께 표현한 것이다.

---

## 전체 흐름도

```mermaid
flowchart TD
    Start([👤 사용자 요청<br/>"TODO 주석 찾아서 정리해줘"]) --> Plan

    subgraph Plan[① 계획 단계 — optional]
        PlanCall[LLM 계획 호출<br/>system: '작업을 단계로 쪼개라']
        PlanCall --> PlanOut[구조화된 계획<br/>step1 파일검색 → step2 읽기 → step3 요약]
        PlanOut --> CtxInit
    end

    CtxInit[② 컨텍스트 초기화<br/>system prompt + user msg + tool 정의] --> Loop

    Loop{{③ 루프 반복}} --> Build[요청 조립:<br/>messages[] + tools[]]
    Build --> Call[④ LLM API 호출]
    Call --> Resp[⑤ 응답 파싱]
    Resp --> Decision{⑥ tool_calls 가<br/>있는가?}

    Decision -- "없음 → 최종 답변" --> Done([⑦ 최종 응답 반환<br/>루프 종료])
    Decision -- "있음" --> Exec[⑧ 도구 실행<br/>read_file / run_command]
    Exec --> Append[⑨ 결과를 messages 에<br/>role=tool 로 append]
    Append --> Loop
```

---

## 단계별 설명

| # | 단계 | 설명 |
|---|---|---|
| ① | 계획 (optional) | 복잡한 작업일 때만. LLM에게 단계를 쪼개라고 한 번 더 호출해 구조화된 plan을 받는다. |
| ② | 컨텍스트 초기화 | `system prompt` + 사용자 메시지 + 사용 가능한 도구 정의를 세팅한다. |
| ③ | 루프 반복 | 매 반복마다 현재까지의 `messages[]`를 기반으로 LLM을 호출한다. |
| ④ | LLM 호출 | messages + tools를 API로 보낸다. |
| ⑤ | 응답 파싱 | 응답 메시지를 히스토리에 넣는다. |
| ⑥ | **분기** | `tool_calls` 유무로 "계속" 또는 "종료"를 결정한다. |
| ⑦ | 종료 | 최종 답변 반환. |
| ⑧ | 도구 실행 | LLM이 요청한 도구를 실행한다. |
| ⑨ | 결과 append | 실행 결과를 `role=tool` 메시지로 히스토리에 추가하고 ③으로 돌아간다. |

> ⑥번 분기가 가장 중요하다. 이 분기 하나가 "아직 일하는 중"과 "끝남"을 구분한다.
