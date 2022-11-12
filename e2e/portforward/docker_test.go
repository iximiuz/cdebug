package portforward

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"gotest.tools/assert"
	"gotest.tools/poll"
	"gotest.tools/v3/icmd"
)

const (
	imageNginx = "docker.io/library/nginx:1.23"
)

type forwarding struct {
	LocalHost  string `json:"localHost"`
	LocalPort  string `json:"localPort"`
	RemoteHost string `json:"remoteHost"`
	RemotePort string `json:"remotePort"`
}

func TestPortForwardDockerRemotePort(t *testing.T) {
	// Start target container.
	targetID := runBackgroundNginx(t)
	defer func() { removeContainer(t, targetID).Assert(t, icmd.Success) }()

	// Initiate port forwarding.
	cmd := icmd.Command("cdebug", "port-forward", "-q", "-o", "json", targetID, "80")
	res := icmd.StartCmd(cmd)
	assert.NilError(t, res.Error)
	defer func() { icmd.WaitOnCmd(cmd.Timeout, res).Assert(t, icmd.Success) }()

	// Wait until it's up and running.
	var addr string
	poll.WaitOn(
		t, func(poll.LogT) poll.Result {
			var fwds []forwarding
			t.Log(res.Stdout())
			if json.Unmarshal([]byte(res.Stdout()), fwds) == nil && len(fwds) > 0 {
				addr = fwds[0].LocalHost + ":" + fwds[0].LocalPort
				return poll.Success()
			}

			assert.NilError(t, res.Error)
			return poll.Continue("waiting for `cdebug port-forward` to start up...")
		},
		poll.WithDelay(500*time.Millisecond),
		poll.WithTimeout(3*time.Second),
	)

	// Probe target through forwarded port.
	t.Fatalf("not implemented: %s", addr)
}

func runBackgroundNginx(t *testing.T) string {
	res := icmd.RunCommand("docker", "run", "-d", imageNginx)
	res.Assert(t, icmd.Success)
	return strings.TrimSpace(res.Stdout())
}

func removeContainer(t *testing.T, id string) *icmd.Result {
	return icmd.RunCommand("docker", "rm", id)
}
