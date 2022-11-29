package exec

import (
	"testing"

	"gotest.tools/assert"
	"gotest.tools/assert/cmp"
	"gotest.tools/v3/icmd"

	"github.com/iximiuz/cdebug/e2e/internal/fixture"
)

func TestExecDockerSimple(t *testing.T) {
	targetID, cleanup := fixture.DockerRunBackground(t, fixture.ImageNginx, nil)
	defer cleanup()

	res := icmd.RunCmd(
		icmd.Command("cdebug", "exec", "--rm", "-q", targetID, "cat", "/etc/os-release"),
	)
	res.Assert(t, icmd.Success)
	assert.Check(t, cmp.Contains(res.Stdout(), "debian"))
}

func TestExecDockerHostNamespaces(t *testing.T) {
	targetID, cleanup := fixture.DockerRunBackground(t, fixture.ImageNginx,
		[]string{"--net", "host", "--pid", "host"},
	)
	defer cleanup()

	res := icmd.RunCmd(
		icmd.Command("cdebug", "exec", "--rm", "-q", targetID, "cat", "/etc/os-release"),
	)
	res.Assert(t, icmd.Success)
	assert.Check(t, cmp.Contains(res.Stdout(), "debian"))
}

func TestExecDockerNixery(t *testing.T) {
	targetID, cleanup := fixture.DockerRunBackground(t, fixture.ImageNginx, nil)
	defer cleanup()

	res := icmd.RunCmd(
		icmd.Command(
			"cdebug", "exec", "--rm", "-q",
			"--image", "nixery.dev/shell/vim",
			targetID,
			"vim", "--version",
		),
	)
	res.Assert(t, icmd.Success)
	assert.Check(t, cmp.Contains(res.Stdout(), "VIM - Vi IMproved"))
}
