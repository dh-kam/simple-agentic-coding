# 3단계: 구체적 예제 — 시퀀스로 보는 실제 흐름

## 요청

> **"이 저장소의 TODO 주석을 찾아서 정리해줘"**

사용 가능한 도구: `run_command`, `read_file`

---

## 시퀀스 다이어그램

```mermaid
sequenceDiagram
    actor U as 👤 User
    participant A as Agent(루프)
    participant L as LLM API
    participant T as Tools

    U->>A: "TODO 주석 찾아서 정리해줘"

    rect rgb(230, 245, 255)
    Note over A,L: 반복 1회차
    A->>L: Chat(messages=[system,user], tools)
    L-->>A: assistant{ tool_calls:[run_command("grep -rn TODO *.go")] }
    A->>T: run_command("grep -rn TODO *.go")
    T-->>A: "main.go:10: TODO fix...\nutils.go:5: TODO refactor..."
    Note over A: messages += [assistant(툴콜), tool(결과)]
    end

    rect rgb(230, 245, 255)
    Note over A,L: 반복 2회차
    A->>L: Chat(messages, tools)
    L-->>A: assistant{ tool_calls:[read_file("utils.go")] }
    A->>T: read_file("utils.go")
    T-->>A: "<파일 내용 전체>"
    Note over A: messages += [assistant(툴콜), tool(결과)]
    end

    rect rgb(235, 255, 235)
    Note over A,L: 반복 3회차 — 종료 조건
    A->>L: Chat(messages, tools)
    L-->>A: assistant{ content:"TODO 2개: ① parser 리팩터 ② nil 체크", tool_calls:[] }
    Note over A: tool_calls 없음 → 루프 종료
    end

    A-->>U: "TODO 2개: ① parser 리팩터 ② nil 체크"
```

---

## 반복별 요약

| 반복 | LLM 응답 | 에이전트 동작 | 다음 |
|---|---|---|---|
| 1회차 | `run_command("grep -rn TODO *.go")` | 명령 실행 → 결과 append | 루프 계속 |
| 2회차 | `read_file("utils.go")` | 파일 읽기 → 결과 append | 루프 계속 |
| 3회차 | `content="TODO 2개: ..."`, `tool_calls=[]` | 최종 답변 판단 | **루프 종료** |

> 매 반복마다 `messages[]`가 누적된다. LLM은 이 히스토리 전체를 보고
> "무엇을 했고, 다음에 뭘 해야 할지"를 판단한다.
