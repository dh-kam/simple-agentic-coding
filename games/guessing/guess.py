"""숫자 맞추기 게임 (Number Guessing Game)

컴퓨터가 1~100 사이의 숫자를 하나 고르면,
사용자가 그 숫자를 맞힙니다. 입력할 때마다 Higher / Lower 힌트를 주고,
정답을 맞히면 총 시도 횟수와 함께 축하 메시지를 출력합니다.
"""

import random


def main() -> None:
    print("=" * 40)
    print("   🔢 숫자 맞추기 게임")
    print("   컴퓨터가 1~100 중 하나를 골랐습니다!")
    print("=" * 40)

    answer = random.randint(1, 100)   # 정답
    attempts = 0                       # 시도 횟수

    while True:
        raw = input("\n숫자를 입력하세요 (1~100): ").strip()

        # ── 입력값 검증 ──────────────────────────────
        try:
            guess = int(raw)
        except ValueError:
            print("⚠️  숫자만 입력할 수 있습니다. 다시 시도해 주세요.")
            continue

        if guess < 1 or guess > 100:
            print("⚠️  1부터 100 사이의 숫자를 입력해 주세요.")
            continue

        # ── 정상 입력: 시도 횟수 증가 ─────────────────
        attempts += 1

        # ── 힌트 판정 ───────────────────────────────
        if guess < answer:
            print("📈 Higher!")
        elif guess > answer:
            print("📉 Lower!")
        else:
            print("=" * 40)
            print(f"🎉 축하합니다! 정답은 {answer} 입니다!")
            print(f"총 시도 횟수: {attempts}회")
            print("=" * 40)
            break


if __name__ == "__main__":
    main()
