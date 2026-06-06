//go:build darwin || windows

package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "otto-tray: not yet implemented")
	os.Exit(2)
}
