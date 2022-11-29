package exec

import (
	"testing"

	"gotest.tools/assert"
	"gotest.tools/assert/cmp"
	"gotest.tools/v3/icmd"

	"github.com/iximiuz/cdebug/e2e/internal/fixture"
	"github.com/iximiuz/cdebug/pkg/uuid"
)

func TestExecNerdctlSimple(t *testing.T) {
	name := t.Name() + "-" + uuid.ShortID()
	_, cleanup := fixture.NerdctlRunBackground(t, fixture.ImageNginx,
		[]string{"--name", name},
	)
	defer cleanup()

	res := icmd.RunCmd(
		icmd.Command(
			"sudo", "cdebug", "exec", "--rm", "-q",
			"nerdctl://"+name,
			"cat", "/etc/os-release",
		),
	)
	res.Assert(t, icmd.Success)
	assert.Check(t, cmp.Contains(res.Stdout(), "debian"))
}
