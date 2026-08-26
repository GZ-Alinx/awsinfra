//go:build !windows

package main

import (
	"os"
	"syscall"
)

func setSecureUmask() { syscall.Umask(0o077) }

func shutdownSignals() []os.Signal { return []os.Signal{syscall.SIGINT, syscall.SIGTERM} }
