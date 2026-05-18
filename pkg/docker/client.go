package docker

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/docker/cli/cli/connhelper"
	"github.com/docker/cli/cli/streams"
	"github.com/docker/docker/api/types/image"
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

	host := opts.Host
	if host == "" {
		host = os.Getenv("DOCKER_HOST")
	}
	if host != "" {
		helper, err := connhelper.GetConnectionHelper(host)
		if err != nil {
			return nil, fmt.Errorf("cannot resolve Docker connection helper: %w", err)
		}
		if helper != nil {
			httpClient := &http.Client{
				Transport: &http.Transport{DialContext: helper.Dialer},
			}
			dockerOpts = append(dockerOpts,
				client.WithHTTPClient(httpClient),
				client.WithHost(helper.Host),
				client.WithDialContext(helper.Dialer),
			)
		} else if opts.Host != "" {
			dockerOpts = append(dockerOpts, client.WithHost(opts.Host))
		}
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
	img string,
	options image.PullOptions,
) error {
	resp, err := c.CommonAPIClient.ImagePull(ctx, img, options)
	if err != nil {
		return err
	}
	defer resp.Close()

	return jsonmessage.DisplayJSONMessagesToStream(resp, c.out, nil)
}
