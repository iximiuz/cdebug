package tty

import (
	"context"
	"os"
	"os/signal"
	"time"

	"github.com/docker/cli/cli/streams"
	"github.com/docker/docker/api/types/container"
	dockerclient "github.com/docker/docker/client"
	mobysignal "github.com/moby/sys/signal"
	"github.com/sirupsen/logrus"
	"k8s.io/client-go/tools/remotecommand"
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

	if err := client.ContainerResize(ctx, contID, container.ResizeOptions{Height: height, Width: width}); err != nil {
		logrus.WithError(err).Debug("TTY resize error")
		return err
	}

	return nil
}

type ResizeQueue struct {
	ctx context.Context
	out *streams.Out
	ch  chan os.Signal
}

var _ remotecommand.TerminalSizeQueue = &ResizeQueue{}

func NewResizeQueue(ctx context.Context, out *streams.Out) *ResizeQueue {
	return &ResizeQueue{
		ctx: ctx,
		out: out,
		ch:  make(chan os.Signal, 100),
	}
}

func (r *ResizeQueue) Start() {
	signal.Notify(r.ch, mobysignal.SIGWINCH)
	r.ch <- mobysignal.SIGWINCH // send a dummy signal to trigger the first resize
}

func (r *ResizeQueue) Next() *remotecommand.TerminalSize {
	<-r.ch

	height, width := r.out.GetTtySize()
	return &remotecommand.TerminalSize{
		Height: uint16(height),
		Width:  uint16(width),
	}
}
