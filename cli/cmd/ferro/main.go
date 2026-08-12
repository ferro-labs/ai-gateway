// Command ferro is the operator console for the Ferro Labs AI Gateway.
//
// It talks to the gateway only over HTTP and does not import gateway packages.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/charmbracelet/fang"

	"github.com/ferro-labs/ai-gateway/cli/internal/command"
)

func main() {
	if err := run(); err != nil {
		os.Exit(1) // fang has already rendered the error
	}
}

// run owns every deferred cleanup, because os.Exit runs none of them. With the
// signal context built directly in main, the os.Exit(1) on a failed command
// skipped `defer stop()` entirely, so the SIGINT handler signal.NotifyContext
// installs was never released on the error path.
func run() error {
	// SIGINT cancels in-flight requests and long-running tails cleanly rather
	// than killing the process mid-write.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	return fang.Execute(ctx, command.NewRoot())
}
