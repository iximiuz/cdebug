package containerd

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"

	"github.com/containerd/containerd"
	"github.com/containerd/containerd/cmd/ctr/commands/content"
	"github.com/docker/cli/cli/streams"
	"golang.org/x/sys/unix"
)

const (
	defaultNamespace = "default"
)

var wellKnownAddresses = []string{
	"/run/containerd/containerd.sock",
	"/var/run/docker/containerd/containerd.sock",
}

type Client struct {
	*containerd.Client
	out       *streams.Out
	namespace string
}

type Options struct {
	Out       *streams.Out
	Address   string
	Namespace string
}

func NewClient(opts Options) (*Client, error) {
	addr, err := detectAddress(opts)
	if err != nil {
		return nil, err
	}

	namespace := defaultNamespace
	if len(opts.Namespace) > 0 {
		namespace = opts.Namespace
	}

	inner, err := containerd.New(addr, containerd.WithDefaultNamespace(namespace))
	if err != nil {
		return nil, err
	}

	out := opts.Out
	if out == nil {
		out = streams.NewOut(io.Discard)
	}

	return &Client{
		Client:    inner,
		out:       out,
		namespace: namespace,
	}, nil
}

func (c *Client) Namespace() string {
	return c.namespace
}

func (c *Client) ImagePullEx(
	ctx context.Context,
	ref string,
) (containerd.Image, error) {
	pctx, stopProgress := context.WithCancel(ctx)
	jobs := content.NewJobs(ref)
	progressCh := make(chan struct{})
	go func() {
		content.ShowProgress(pctx, jobs, c.ContentStore(), c.out)
		close(progressCh)
	}()

	image, err := c.Pull(ctx, ref, containerd.WithPullUnpack)
	stopProgress()
	if err != nil {
		return image, err
	}

	<-progressCh
	return image, nil
}

func detectAddress(opts Options) (string, error) {
	addresses := wellKnownAddresses[:]
	if len(opts.Address) > 0 {
		addresses = []string{strings.TrimPrefix(opts.Address, "unix://")}
	}

	for _, addr := range addresses {
		if isSocketAccessible(addr) == nil {
			return addr, nil
		}
	}

	return "", errors.New("cannot detect (good enough) containerd address")
}

func isSocketAccessible(sockfile string) error {
	abs, err := filepath.Abs(sockfile)
	if err != nil {
		return err
	}

	// Shamelessly borrowed from nerdctl:
	// > set AT_EACCESS to allow running nerdctl as a setuid binary
	return unix.Faccessat(-1, abs, unix.R_OK|unix.W_OK, unix.AT_EACCESS)
}
