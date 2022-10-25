package tty

import (
	"context"
	"os"
	"os/signal"
	"time"

	"github.com/docker/cli/cli/streams"
	"github.com/docker/docker/api/types"
	dockerclient "github.com/docker/docker/client"
	mobysignal "github.com/moby/sys/signal"
	"github.com/sirupsen/logrus"
)

func StartResizing(
	ctx context.Context,
	out *streams.Out,
	client dockerclient.ContainerAPIClient,
	contID string,
) {
	go func() {
		for retry := 0; retry < 10; retry++ {
			if err := resize(ctx, out, client, contID); err == nil {
				return
			}
			time.Sleep(time.Duration(retry+1) * 10 * time.Millisecond)
		}
		logrus.Warn("Cannot resize TTY")
	}()

	sigchan := make(chan os.Signal, 1)
	signal.Notify(sigchan, mobysignal.SIGWINCH)
	go func() {
		for range sigchan {
			resize(ctx, out, client, contID)
		}
	}()
}

func resize(
	ctx context.Context,
	out *streams.Out,
	client dockerclient.ContainerAPIClient,
	contID string,
) error {
	height, width := out.GetTtySize()
	if height == 0 && width == 0 {
		return nil
	}

	if err := client.ContainerResize(ctx, contID, types.ResizeOptions{Height: height, Width: width}); err != nil {
		logrus.WithError(err).Debug("TTY resize error")
		return err
	}

	return nil
}
