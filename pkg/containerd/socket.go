//go:build !linux

package containerd

import (
	"path/filepath"
)

func isSocketAccessible(sockfile string) error {
	_, err := filepath.Abs(sockfile)
	if err != nil {
		return err
	}

	// Assuming on macOS and Windows Docker Desktop and alike
	// run in unprivileged mode.
	return nil
}
