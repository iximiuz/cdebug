package exec

import (
	"testing"

	"gotest.tools/assert"
	"gotest.tools/assert/cmp"
	"gotest.tools/v3/icmd"

	"github.com/iximiuz/cdebug/e2e/internal/fixture"
)

func TestExecContainerdSimple(t *testing.T) {
	targetID, cleanup := fixture.ContainerdRunBackground(t, fixture.ImageNginx, nil)
	defer cleanup()

	res := icmd.RunCmd(
		icmd.Command(
			"sudo", "cdebug", "exec", "-n", fixture.ContainerdCtrNamespace, "--rm", "-q",
			"containerd://"+targetID,
			"cat", "/etc/os-release",
		),
	)
	res.Assert(t, icmd.Success)
	assert.Check(t, cmp.Contains(res.Stdout(), "debian"))
}

func TestExecContainerdHostNamespaces(t *testing.T) {
	targetID, cleanup := fixture.ContainerdRunBackground(t, fixture.ImageNginx,
		[]string{"--net-host"},
	)
	defer cleanup()

	res := icmd.RunCmd(
		icmd.Command(
			"sudo", "cdebug", "exec", "-n", fixture.ContainerdCtrNamespace, "--rm", "-q",
			"containerd://"+targetID,
			"cat", "/etc/os-release",
		),
	)
	res.Assert(t, icmd.Success)
	assert.Check(t, cmp.Contains(res.Stdout(), "debian"))
}

func TestExecContainerdNixery(t *testing.T) {
	targetID, cleanup := fixture.ContainerdRunBackground(t, fixture.ImageNginx, nil)
	defer cleanup()

	res := icmd.RunCmd(
		icmd.Command(
			"sudo", "cdebug", "exec", "-n", fixture.ContainerdCtrNamespace, "--rm", "-q",
			"--image", "nixery.dev/shell/vim",
			"containerd://"+targetID,
			"vim", "--version",
		),
	)
	res.Assert(t, icmd.Success)
	assert.Check(t, cmp.Contains(res.Stdout(), "VIM - Vi IMproved"))
}
