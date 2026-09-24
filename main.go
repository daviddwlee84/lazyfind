package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/daviddwlee84/lazyfind/internal/cli"
	buildversion "github.com/daviddwlee84/lazyfind/internal/version"
)

var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := 0
	if err := cli.New(buildversion.Current(version)).ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "lazyfind:", err)
		code = cli.ExitCode(err)
	}
	stop()
	os.Exit(code)
}
