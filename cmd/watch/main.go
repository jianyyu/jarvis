// cmd/watch/main.go
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
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

	daemon, err := watch.NewDaemon(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		fmt.Fprintln(os.Stderr, "\nConfigure the Slack watcher in ~/.jarvis/config.yaml:")
		fmt.Fprintln(os.Stderr, "  watchers:")
		fmt.Fprintln(os.Stderr, "    slack:")
		fmt.Fprintln(os.Stderr, "      enabled: true")
		fmt.Fprintln(os.Stderr, "      mcp_server_cmd: \"python3.10 /path/to/slack_mcp_deploy.pex\"")
		fmt.Fprintln(os.Stderr, "      user_id: \"U050RJFF7T3\"")
		fmt.Fprintln(os.Stderr, "      poll_interval: 30")
		fmt.Fprintln(os.Stderr, "      folder: \"Slack\"")
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

	log.Println("starting Slack watcher")
	if err := daemon.Run(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
