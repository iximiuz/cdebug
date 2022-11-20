package docker

import (
	"context"
	"fmt"
	"io"

	"github.com/docker/cli/cli/streams"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/jsonmessage"
)

type Client struct {
	client.CommonAPIClient
	out *streams.Out
}

var _ client.CommonAPIClient = &Client{}

type Options struct {
	Out  *streams.Out
	Host string
}

func NewClient(opts Options) (*Client, error) {
	dockerOpts := []client.Opt{
		client.FromEnv,
		client.WithAPIVersionNegotiation(),
	}
	if len(opts.Host) > 0 {
		dockerOpts = append(dockerOpts, client.WithHost(opts.Host))
	}

	inner, err := client.NewClientWithOpts(dockerOpts...)
	if err != nil {
		return nil, fmt.Errorf("cannot initialize Docker client: %w", err)
	}

	out := opts.Out
	if out == nil {
		out = streams.NewOut(io.Discard)
	}

	return &Client{
		CommonAPIClient: inner,
		out:             out,
	}, nil
}

func (c *Client) ImagePullEx(
	ctx context.Context,
	image string,
	options types.ImagePullOptions,
) error {
	resp, err := c.CommonAPIClient.ImagePull(ctx, image, options)
	if err != nil {
		return err
	}
	defer resp.Close()

	return jsonmessage.DisplayJSONMessagesToStream(resp, c.out, nil)
}
