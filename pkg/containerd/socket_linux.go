//go:build linux

package containerd

import (
	"path/filepath"

	"golang.org/x/sys/unix"
)

func isSocketAccessible(sockfile string) error {
	abs, err := filepath.Abs(sockfile)
	if err != nil {
		return err
	}

	// Shamelessly borrowed from nerdctl:
	// > set AT_EACCESS to allow running nerdctl as a setuid binary
	return unix.Faccessat(-1, abs, unix.R_OK|unix.W_OK, unix.AT_EACCESS)
}
