package main

import (
	"bufio"
	"fmt"
	"math/rand"
	"os"
	"strings"
	"time"
)

func main() {
	rand.Seed(time.Now().UnixNano())

	reader := bufio.NewReader(os.Stdin)

	var userWins, computerWins, draws int
	choices := []string{"rock", "paper", "scissors"}

	for {
		fmt.Print("Enter rock, paper, scissors (or quit): ")

		input, _ := reader.ReadString('\n')
		input = strings.ToLower(strings.TrimSpace(input))

		if input == "quit" {
			fmt.Println("\n=== Final Score ===")
			fmt.Printf("You      : %d\n", userWins)
			fmt.Printf("Computer : %d\n", computerWins)
			fmt.Printf("Draws    : %d\n", draws)
			return
		}

		valid := false
		for _, c := range choices {
			if input == c {
				valid = true
				break
			}
		}
		if !valid {
			fmt.Println("Invalid input. Please try again.\n")
			continue
		}

		computerChoice := choices[rand.Intn(3)]

		fmt.Printf("You chose      : %s\n", input)
		fmt.Printf("Computer chose : %s\n", computerChoice)

		if input == computerChoice {
			fmt.Println("=> Draw!")
			draws++
		} else if (input == "rock" && computerChoice == "scissors") ||
			(input == "paper" && computerChoice == "rock") ||
			(input == "scissors" && computerChoice == "paper") {
			fmt.Println("=> You win!")
			userWins++
		} else {
			fmt.Println("=> Computer wins!")
			computerWins++
		}

		fmt.Printf("\n--- Score ---  You: %d  |  Computer: %d  |  Draws: %d\n\n",
			userWins, computerWins, draws)
	}
}
