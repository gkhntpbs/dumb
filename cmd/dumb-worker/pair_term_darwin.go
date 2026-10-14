//go:build darwin

package main

import "golang.org/x/sys/unix"

const pairGetTerm = unix.TIOCGETA
const pairSetTerm = unix.TIOCSETA
