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

	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, "gateway-tray:", err)
		os.Exit(1)
	}
	installDir, err := resolveInstallDirFrom(exe)
	if err != nil {
		fmt.Fprintln(os.Stderr, "gateway-tray:", err)
		os.Exit(1)
	}
	home, _ := os.UserHomeDir()
	gwHome := resolveGWHome(os.Getenv, home)

	if *uninstall {
		if err := runUninstall(installDir); err != nil {
			fmt.Fprintln(os.Stderr, "gateway-tray --uninstall:", err)
			os.Exit(1)
		}
		fmt.Println("gateway-tray: login-item removed (binary not deleted)")
		return
	}

	cfg, isFirstRun := loadTrayConfig(gwTrayConfigPath(gwHome))
	runTray(installDir, gwHome, cfg, isFirstRun)
}
