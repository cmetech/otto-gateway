//go:build darwin

package main

import "syscall"

func syscall0() syscall.Signal { return syscall.Signal(0) }
