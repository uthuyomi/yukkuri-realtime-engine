//go:build !windows

package whispercpp

import "os/exec"

func prepareChild(c *exec.Cmd)                 {}
func containChild(c *exec.Cmd) (func(), error) { return func() {}, nil }
