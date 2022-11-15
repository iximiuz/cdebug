package signalutil

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

func InterruptibleContext(ctx context.Context) context.Context {
	ctx, cancel := context.WithCancel(ctx)

	go func() {
		defer cancel()

		signalCh := make(chan os.Signal, 128)
		signal.Notify(signalCh, os.Interrupt, syscall.SIGTERM)
		defer signal.Stop(signalCh)

		select {
		case <-signalCh:
		case <-ctx.Done():
		}
	}()

	return ctx
}
