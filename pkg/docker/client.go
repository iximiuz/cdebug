package docker

import (
	"context"
	"fmt"

	"github.com/docker/cli/cli/streams"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/jsonmessage"
)

type Client struct {
	client.CommonAPIClient
	aux *streams.Out
}

var _ client.CommonAPIClient = &Client{}

func NewClient(aux *streams.Out) (*Client, error) {
	inner, err := client.NewClientWithOpts(
		client.FromEnv,
		client.WithAPIVersionNegotiation(),
	)
	if err != nil {
		return nil, fmt.Errorf("cannot initialize Docker client: %w", err)
	}

	return &Client{
		CommonAPIClient: inner,
		aux:             aux,
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

	return jsonmessage.DisplayJSONMessagesToStream(resp, c.aux, nil)
}
