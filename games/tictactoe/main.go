package main

import (
	"bufio"
	"fmt"
	"math/rand"
	"os"
	"strings"
)

func main() {
	board := []string{"1", "2", "3", "4", "5", "6", "7", "8", "9"}
	reader := bufio.NewReader(os.Stdin)

	printBoard(board)

	for {
		// 사람 차례 (X)
		humanMove(reader, board)
		printBoard(board)

		if winner := checkWinner(board); winner != "" {
			fmt.Printf("🎉 %s 승리!\n", winner)
			return
		}
		if isFull(board) {
			fmt.Println("🤝 무승부!")
			return
		}

		// 컴퓨터 차례 (O)
		computerMove(board)
		printBoard(board)

		if winner := checkWinner(board); winner != "" {
			fmt.Printf("🎉 %s 승리!\n", winner)
			return
		}
		if isFull(board) {
			fmt.Println("🤝 무승부!")
			return
		}
	}
}

// printBoard 보드를 터미널에 출력
func printBoard(b []string) {
	fmt.Println()
	fmt.Printf(" %s | %s | %s\n", b[0], b[1], b[2])
	fmt.Println("---+---+---")
	fmt.Printf(" %s | %s | %s\n", b[3], b[4], b[5])
	fmt.Println("---+---+---")
	fmt.Printf(" %s | %s | %s\n", b[6], b[7], b[8])
	fmt.Println()
}

// humanMove 사람의 수 입력 처리
func humanMove(r *bufio.Reader, b []string) {
	for {
		fmt.Print("당신(X) - 놓을 칸 번호(1~9): ")
		input, _ := r.ReadString('\n')
		input = strings.TrimSpace(input)

		var pos int
		_, err := fmt.Sscanf(input, "%d", &pos)
		if err != nil || pos < 1 || pos > 9 {
			fmt.Println("⚠️  1~9 사이의 숫자를 입력하세요.")
			continue
		}

		idx := pos - 1
		if b[idx] == "X" || b[idx] == "O" {
			fmt.Println("⚠️  이미 채워진 칸입니다. 다시 선택하세요.")
			continue
		}
		b[idx] = "X"
		return
	}
}

// computerMove 빈 칸 중 무작위로 선택해 O 놓기
func computerMove(b []string) {
	var empty []int
	for i, v := range b {
		if v != "X" && v != "O" {
			empty = append(empty, i)
		}
	}
	if len(empty) == 0 {
		return
	}
	pick := empty[rand.Intn(len(empty))]
	b[pick] = "O"
	fmt.Printf("컴퓨터(O)가 %d번 칸에 놓았습니다.\n", pick+1)
}

// checkWinner 승자가 있으면 "X" 또는 "O", 없으면 "" 반환
func checkWinner(b []string) string {
	lines := [8][3]int{
		{0, 1, 2}, {3, 4, 5}, {6, 7, 8}, // 가로
		{0, 3, 6}, {1, 4, 7}, {2, 5, 8}, // 세로
		{0, 4, 8}, {2, 4, 6},           // 대각선
	}
	for _, l := range lines {
		if b[l[0]] == b[l[1]] && b[l[1]] == b[l[2]] {
			return b[l[0]] // "X" 또는 "O"
		}
	}
	return ""
}

// isFull 보드가 꽉 찼는지 확인
func isFull(b []string) bool {
	for _, v := range b {
		if v != "X" && v != "O" {
			return false
		}
	}
	return true
}
