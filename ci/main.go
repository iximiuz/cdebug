// A generated module for Ci functions
//
// This module has been generated via dagger init and serves as a reference to
// basic module structure as you get started with Dagger.
//
// Two functions have been pre-created. You can modify, delete, or add to them,
// as needed. They demonstrate usage of arguments and return types using simple
// echo and grep commands. The functions can be called from the dagger CLI or
// from one of the SDKs.
//
// The first line in this comment block is a short description line and the
// rest is a long description with more detail on the module's purpose or usage,
// if appropriate. All modules should have a short description.

package main

import (
	"context"
	"fmt"
)

type Ci struct{}

// Returns a container that echoes whatever string argument is provided
func (m *Ci) ContainerEcho(stringArg string) *Container {
	return dag.Container().From("alpine:latest").WithExec([]string{"echo", stringArg})
}

// Runs the e2e tests for the project.
func (m *Ci) TestE2e(ctx context.Context, dir *Directory) error {
	container := m.testBase(ctx).
		WithDirectory("/app", dir).
		WithWorkdir("/app").
		WithUnixSocket("/var/run/docker.sock", dag.CurrentModule().Host().UnixSocket("unix:///var/run/docker.sock")).
		WithExec([]string{"go", "test", "-v", "-count", "1", "./e2e/exec"})

	container, err := container.Sync(ctx)

	stderr, _ := container.Stderr(ctx)
	fmt.Println("STDERR", stderr)

	stdout, _ := container.Stdout(ctx)
	fmt.Println("STDOUT", stdout)

	return err
}

func (m *Ci) testBase(ctx context.Context) *Container {
	return dag.Container().
		From("golang:1.22-alpine").
		WithExec([]string{"apk", "add", "--no-cache", "sudo", "docker"})
}
