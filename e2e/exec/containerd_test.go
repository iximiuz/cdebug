package exec

import (
	"strings"
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
			"cdebug", "exec", "-n", fixture.ContainerdCtrNamespace, "--rm", "-q",
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
			"cdebug", "exec", "-n", fixture.ContainerdCtrNamespace, "--rm", "-q",
			"containerd://"+targetID,
			"cat", "/etc/os-release",
		),
	)
	res.Assert(t, icmd.Success)
	assert.Check(t, cmp.Contains(res.Stdout(), "debian"))
}

func TestExecContainerdRunAsUser(t *testing.T) {
	targetID, cleanup := fixture.ContainerdRunBackground(t, fixture.ImageNginxUnprivileged, nil)
	defer cleanup()

	res := icmd.RunCmd(
		icmd.Command(
			"cdebug", "exec", "-n", fixture.ContainerdCtrNamespace, "--rm", "-q", "-u", "101:101",
			"containerd://"+targetID,
			"id", "-u",
		),
	)
	res.Assert(t, icmd.Success)
	assert.Equal(t, res.Stderr(), "")
	assert.Equal(t, strings.TrimSpace(res.Stdout()), "101")

	res = icmd.RunCmd(
		icmd.Command(
			"cdebug", "exec", "-n", fixture.ContainerdCtrNamespace, "--rm", "-q", "-u", "101:101",
			"containerd://"+targetID,
			"busybox",
		),
	)
	res.Assert(t, icmd.Success)
	assert.Check(t, cmp.Contains(res.Stdout(), "BusyBox v1"))
}

func TestExecContainerdNixery(t *testing.T) {
	targetID, cleanup := fixture.ContainerdRunBackground(t, fixture.ImageNginx, nil)
	defer cleanup()

	res := icmd.RunCmd(
		icmd.Command(
			"cdebug", "exec", "-n", fixture.ContainerdCtrNamespace, "--rm", "-q",
			"--image", "nixery.dev/shell/vim",
			"containerd://"+targetID,
			"vim", "--version",
		),
	)
	res.Assert(t, icmd.Success)
	assert.Check(t, cmp.Contains(res.Stdout(), "VIM - Vi IMproved"))
}
