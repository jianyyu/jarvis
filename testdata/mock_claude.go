// mock_claude simulates a Claude Code session for testing.
// It reads stdin, writes stdout, and simulates approval prompts.
//
// Usage: go run mock_claude.go [--resume SESSION_ID]
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"
)

func main() {
	resume := flag.String("resume", "", "session ID to resume")
	appendPrompt := flag.String("append-system-prompt", "", "system prompt")
	flag.Parse()

	if *resume != "" {
		fmt.Printf("Resuming session %s...\n", *resume)
	}
	if *appendPrompt != "" {
		// Silently accept the prompt
	}

	fmt.Println("Mock Claude Code v1.0")
	fmt.Println("Type 'help' for commands, 'exit' to quit")

	scanner := bufio.NewScanner(os.Stdin)
	// Split on \n or \r to handle both PTY auto-approve (\r) and manual input (\n).
	scanner.Split(func(data []byte, atEOF bool) (advance int, token []byte, err error) {
		if atEOF && len(data) == 0 {
			return 0, nil, nil
		}
		for i, b := range data {
			if b == '\n' || b == '\r' {
				return i + 1, data[:i], nil
			}
		}
		if atEOF {
			return len(data), data, nil
		}
		return 0, nil, nil // need more data
	})
	for {
		fmt.Print("> ")
		if !scanner.Scan() {
			break
		}
		line := strings.TrimSpace(scanner.Text())

		switch {
		case line == "exit":
			fmt.Println("Goodbye!")
			return
		case line == "help":
			fmt.Println("Commands: help, exit, approve, work, crash")
		case line == "approve":
			fmt.Println("I need to run this command:")
			fmt.Println("  rm -rf build/ && npm run build")
			fmt.Println("")
			fmt.Println("Allow Bash? (y/n)")
			if scanner.Scan() {
				resp := strings.TrimSpace(scanner.Text())
				// Accept "y" or empty (from PTY auto-approve sending \r → empty line).
				if resp == "y" || resp == "" {
					fmt.Println("Running command...")
					time.Sleep(500 * time.Millisecond)
					fmt.Println("Command completed successfully.")
				} else {
					fmt.Println("Command denied.")
				}
			}
		case line == "read":
			fmt.Println("I need to read a file:")
			fmt.Println("  src/main.go")
			fmt.Println("")
			fmt.Println("Allow Read? (y/n)")
			if scanner.Scan() {
				resp := strings.TrimSpace(scanner.Text())
				if resp == "y" || resp == "" {
					fmt.Println("Reading file...")
					time.Sleep(200 * time.Millisecond)
					fmt.Println("File read successfully.")
				} else {
					fmt.Println("Read denied.")
				}
			}
		case line == "work":
			fmt.Println("Working on something...")
			for i := 0; i < 5; i++ {
				time.Sleep(200 * time.Millisecond)
				fmt.Printf("  Step %d/5 complete\n", i+1)
			}
			fmt.Println("Done working.")
		case line == "crash":
			fmt.Println("Simulating crash...")
			os.Exit(1)
		default:
			fmt.Printf("Echo: %s\n", line)
		}
	}
}
