//go:build windows

package main

import "os"

// Windows ACLs, rather than a POSIX umask, protect newly-created files.
func setSecureUmask() {}

func shutdownSignals() []os.Signal { return []os.Signal{os.Interrupt} }
