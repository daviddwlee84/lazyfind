//go:build !darwin && !linux && !freebsd && !openbsd && !netbsd

package transport

import (
	"os/exec"
	"time"
)

func configureBackground(cmd *exec.Cmd) { cmd.WaitDelay = 2 * time.Second }
