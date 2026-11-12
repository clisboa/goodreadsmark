package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/clisboa/goodreadsmark/internal/app"

	_ "modernc.org/sqlite"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := app.Run(
		ctx,
		app.CLIOptions{
			Args: os.Args[1:],
			Env:  app.Environment(os.Environ()),
			In:   os.Stdin,
			Out:  os.Stdout,
			Err:  os.Stderr,
		},
	)
	stop()
	os.Exit(code)
}
