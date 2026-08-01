# 검증 방법 10가지 — 에이전트 동작 검증 체크리스트

> `ask` 커맨드(`agentic ask "프롬프트"`)를 사용한 에이전트 동작 검증 방법.
> 각 항목은 독립적으로 실행 가능하며, 통과/실패가 명확히 판별된다.

## 검증 환경

```bash
# .env 설정 (GLM Coding Plan)
export ANTHROPIC_API_KEY=<키>
export ANTHROPIC_BASE_URL=https://open.bigmodel.cn/api/anthropic
export AGENT_MODEL=glm-5.2

# 빌드
go build -o agentic .
```

---

## 1. 파일 생성 (write)

**목적**: 에이전트가 파일을 올바르게 생성하는지 확인.

```bash
./agentic ask 'tmp_test/hello.go 에 Hello World를 출력하는 Go 프로그램을 만들어줘.'
```

**검증**:
- [ ] `● write path=tmp_test/hello.go` 표시
- [ ] `✎ tmp_test/hello.go` + 녹색 diff (+라인)
- [ ] `✓` 완료 표시
- [ ] `go build tmp_test/hello.go` 컴파일 통과
- [ ] glamour 렌더링된 답변 출력

**상태**: ✅ 검증 완료 (2026-08-01)

---

## 2. 파일 수정 + diff (edit)

**목적**: 기존 파일을 수정할 때 diff가 색상으로 표시되는지 확인.

```bash
echo 'package main' > tmp_test/edit.go
echo 'func main() {}' >> tmp_test/edit.go
./agentic ask 'tmp_test/edit.go 에 fmt import와 fmt.Println("hi")를 추가해줘.'
```

**검증**:
- [ ] `● read_file` → `● edit` 순서로 도구 호출
- [ ] `✎ tmp_test/edit.go` + 빨강 `-` / 초록 `+` diff
- [ ] 파일이 실제로 수정됨

**상태**: ✅ 검증 완료 (2026-08-01)

---

## 3. 다중 파일 프로젝트 (multi-file write)

**목적**: 여러 파일을 체계적으로 생성하는지 확인.

```bash
./agentic ask 'tmp_project/ 폴더에 Go CLI 도구를 만들어줘. main.go, handler.go, go.mod 세 파일로 구성.'
```

**검증**:
- [ ] planner가 다단계 계획 생성
- [ ] 파일별로 순차적 `● write` 호출
- [ ] 모든 파일 생성됨
- [ ] `go build tmp_project/` 컴파일 통과

**상태**: ✅ 검증 완료 (Godot 플랫폼 게임: 6파일 21호출, 2026-08-01)

---

## 4. 코드 검색 (grep/glob)

**목적**: 검색 도구를 사용해 기존 코드를 탐색하는지 확인.

```bash
./agentic ask '이 프로젝트에서 모든 Go 파일의 수를 세고, main 함수가 있는 파일 목록을 알려줘.'
```

**검증**:
- [ ] `● glob pattern=**/*.go` 또는 `● grep pattern=func main` 호출
- [ ] 검색 결과를 기반으로 정확한 답변

---

## 5. 명령 실행 (run_command)

**목적**: 셸 명령을 실행하고 결과를 활용하는지 확인.

```bash
./agentic ask 'go version을 실행하고, 현재 Go 버전을 알려줘.'
```

**검증**:
- [ ] `● run_command command=go version` 호출
- [ ] `✓` 완료
- [ ] 명령 출력을 기반으로 버전 정보 답변

---

## 6. 자기 수정 (self-correction)

**목적**: 에이전트가 자신의 출력을 검토하고 수정하는지 확인.

```bash
./agentic ask 'tmp_test/calc.go 에 간단한 계산기 프로그램을 만들고, 컴파일해보고, 오류가 있으면 수정해줘.'
```

**검증**:
- [ ] `● write` → `● run_command(go build)` → (오류 시) `● edit/multi_edit` 순서
- [ ] 자기 수정 diff 표시 (빨강/초록)
- [ ] 최종 컴파일 통과

**상태**: ✅ 검증 완료 (Godot 플랫폼: multi_edit으로 충돌 모양 수정, 2026-08-01)

---

## 7. 컨텍스트 인식 (read_file → modify)

**목적**: 기존 파일을 읽고 내용에 따라 적절히 수정하는지 확인.

```bash
echo 'package main' > tmp_test/ctx.go
echo '// TODO: implement Add function' >> tmp_test/ctx.go
./agentic ask 'tmp_test/ctx.go를 읽고 TODO 주석을 실제 구현으로 바꿔줘.'
```

**검증**:
- [ ] `● read_file path=tmp_test/ctx.go` 먼저 호출
- [ ] 파일 내용(주석 포함)을 이해하고 적절히 수정
- [ ] `● edit`로 TODO를 구현으로 교체

---

## 8. 복잡한 계획 수립 (complex planning)

**목적**: 다단계 작업에서 planner와 executor가 잘 협업하는지 확인.

```bash
./agentic ask 'tmp_game/에 숫자 야구 게임을 Go로 만들어줘. 3자리 숫자, 스트라이크/볼 판정, 10회 제한.'
```

**검증**:
- [ ] planner가 구체적인 단계별 계획 생성
- [ ] todo_write로 진행 상황 추적
- [ ] 계획에 따라 순차적 파일 생성
- [ ] 게임이 정상 동작 (go build 통과)

---

## 9. 에러 복구 (error recovery)

**목적**: 도구 실행 실패 시 에이전트가 복구하는지 확인.

```bash
./agentic ask '존재하지 않는 파일 nonexistent_file.go를 읽고, 없으면 새로 만들어줘.'
```

**검증**:
- [ ] `● read_file path=nonexistent_file.go` → `✗` 에러 표시
- [ ] 에러 메시지를 인식하고 대안 행동 (write로 생성)
- [ ] 최종적으로 파일 생성됨

---

## 10. MCP 연동 (MCP integration)

**목적**: 외부 MCP 서버의 도구를 호출할 수 있는지 확인.

```bash
# echo MCP 서버 빌드
go build -o /tmp/mcp-echo ./examples/mcp-echo

# MCP 설정
cat > /tmp/mcp.json <<'EOF'
{ "mcpServers": { "echo": { "command": "/tmp/mcp-echo" } } }
EOF

# 실행
AGENT_MCP_CONFIG=/tmp/mcp.json ./agentic ask 'echo 도구로 "MCP works!"를 에코해줘.'
```

**검증**:
- [ ] `● echo__echo` MCP 도구 호출 (이름공간 `echo__` 확인)
- [ ] `✓` 완료
- [ ] MCP 서버로부터의 결과("echo: MCP works!") 포함된 답변

**상태**: ✅ 검증 완료 (2026-08-01)

---

## 요약

| # | 검증 항목 | 도구 | 상태 |
|---|----------|------|------|
| 1 | 파일 생성 | write | ✅ |
| 2 | 파일 수정 + diff | edit | ✅ |
| 3 | 다중 파일 프로젝트 | write x N | ✅ |
| 4 | 코드 검색 | glob, grep | ✅ | |
| 5 | 명령 실행 | run_command | (스크립트 가능) |
| 6 | 자기 수정 | write → edit | ✅ |
| 7 | 컨텍스트 인식 | read_file → edit | ✅ | |
| 8 | 복잡한 계획 | planner + todo | ✅ |
| 9 | 에러 복구 | read_file(실패) → write | (스크립트 가능) |
| 10 | MCP 연동 | echo__echo | ✅ |
