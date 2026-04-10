// cmd/watch/main.go
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"jarvis/internal/config"
	"jarvis/internal/watch"
)

func main() {
	log.SetPrefix("jarvis-watch: ")
	log.SetFlags(log.Ldate | log.Ltime)

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error loading config: %v\n", err)
		os.Exit(1)
	}

	slackEnabled := cfg.Watchers.Slack.Enabled
	gmailEnabled := cfg.Watchers.Gmail.Enabled

	if !slackEnabled && !gmailEnabled {
		fmt.Fprintln(os.Stderr, "error: no watchers enabled in config")
		fmt.Fprintln(os.Stderr, "\nConfigure watchers in ~/.jarvis/config.yaml:")
		fmt.Fprintln(os.Stderr, "  watchers:")
		fmt.Fprintln(os.Stderr, "    slack:")
		fmt.Fprintln(os.Stderr, "      enabled: true")
		fmt.Fprintln(os.Stderr, "      mcp_server_cmd: \"python3.10 /path/to/slack_mcp_deploy.pex\"")
		fmt.Fprintln(os.Stderr, "      user_id: \"U050RJFF7T3\"")
		fmt.Fprintln(os.Stderr, "      poll_interval: 30")
		fmt.Fprintln(os.Stderr, "      folder: \"Slack\"")
		fmt.Fprintln(os.Stderr, "    gmail:")
		fmt.Fprintln(os.Stderr, "      enabled: true")
		fmt.Fprintln(os.Stderr, "      poll_interval: 600")
		fmt.Fprintln(os.Stderr, "      folder: \"Gmail\"")
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigs
		log.Println("received shutdown signal")
		cancel()
	}()

	var wg sync.WaitGroup

	if slackEnabled {
		slackDaemon, err := watch.NewDaemon(cfg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "slack watcher error: %v\n", err)
			os.Exit(1)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			log.Println("starting Slack watcher")
			if err := slackDaemon.Run(ctx); err != nil {
				log.Printf("slack watcher error: %v", err)
			}
		}()
	}

	if gmailEnabled {
		gmailDaemon, err := watch.NewGmailDaemon(cfg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "gmail watcher error: %v\n", err)
			os.Exit(1)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			log.Println("starting Gmail watcher")
			if err := gmailDaemon.Run(ctx); err != nil {
				log.Printf("gmail watcher error: %v", err)
			}
		}()
	}

	wg.Wait()
}
