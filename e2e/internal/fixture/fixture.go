package fixture

import (
	"strings"
	"testing"

	"github.com/iximiuz/cdebug/pkg/uuid"
	"gotest.tools/icmd"
)

const (
	ImageNginx = "docker.io/library/nginx:1.23"

	ContainerdCtrNamespace = "cdebug-test-ctr"
	// TODO: ContainerdCRINamespace = "cdebug-test-cri"
)

func ctrCmd(args ...string) icmd.Cmd {
	return icmd.Command(
		"sudo", append([]string{"ctr", "--namespace", ContainerdCtrNamespace}, args...)...,
	)
}

func dockerCmd(args ...string) icmd.Cmd {
	return icmd.Command(
		"sudo", append([]string{"docker"}, args...)...,
	)
}

func ContainerdRunBackground(
	t *testing.T,
	image string,
	flags []string,
	args ...string,
) (string, func()) {
	icmd.RunCmd(ctrCmd("image", "pull", image)).Assert(t, icmd.Success)

	contID := t.Name() + "_" + uuid.ShortID()

	cmd := ctrCmd("run", "-d")
	cmd.Command = append(cmd.Command, flags...)
	cmd.Command = append(cmd.Command, image)
	cmd.Command = append(cmd.Command, contID)
	cmd.Command = append(cmd.Command, args...)

	icmd.RunCmd(cmd).Assert(t, icmd.Success)

	cleanup := func() {
		icmd.RunCmd(ctrCmd("task", "rm", "-f", contID)).Assert(t, icmd.Success)
		icmd.RunCmd(ctrCmd("container", "rm", contID)).Assert(t, icmd.Success)
	}

	return contID, cleanup
}

func DockerRunBackground(
	t *testing.T,
	image string,
	flags []string,
	args ...string,
) (string, func()) {
	cmd := dockerCmd("run", "-d")
	cmd.Command = append(cmd.Command, flags...)
	cmd.Command = append(cmd.Command, image)
	cmd.Command = append(cmd.Command, args...)

	res := icmd.RunCmd(cmd)
	res.Assert(t, icmd.Success)

	contID := strings.TrimSpace(res.Stdout())
	cleanup := func() {
		icmd.RunCmd(dockerCmd("rm", "-f", contID)).Assert(t, icmd.Success)
	}

	return contID, cleanup
}
