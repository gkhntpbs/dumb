//go:build linux

package main

import "golang.org/x/sys/unix"

const pairGetTerm = unix.TCGETS
const pairSetTerm = unix.TCSETS
