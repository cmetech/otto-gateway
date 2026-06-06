//go:build darwin || windows

package main

import (
	"flag"
	"fmt"
	"os"
)

func main() {
	uninstall := flag.Bool("uninstall", false, "remove login-item registration (LaunchAgent on darwin, Run key on windows) and exit")
	flag.Parse()

	installRoot, err := resolveInstallRoot()
	if err != nil {
		fmt.Fprintln(os.Stderr, "otto-tray:", err)
		os.Exit(1)
	}

	if *uninstall {
		if err := runUninstall(installRoot); err != nil {
			fmt.Fprintln(os.Stderr, "otto-tray --uninstall:", err)
			os.Exit(1)
		}
		fmt.Println("otto-tray: login-item removed (binary not deleted)")
		return
	}

	cfg, isFirstRun := loadTrayConfig(trayConfigPath(installRoot))
	runTray(installRoot, cfg, isFirstRun)
}
