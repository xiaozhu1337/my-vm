package main

import (
	"context"
	"flag"
	"fmt"
	"my-vm/internal/guestops"
	"my-vm/internal/logging"
	"my-vm/internal/sharing"
	"os"
	"os/signal"
	"path/filepath"
)

var mediaRoot, installDesktop, installUserSID string

func main() {
	install := flag.Bool("install", filepath.Base(os.Args[0]) == "install.exe", "Install all guest enhancements for this user")
	prepare := flag.Bool("prepare", false, "Prepare guest system components")
	flag.StringVar(&mediaRoot, "media", "", "Root directory of the offline enhancement disc")
	flag.StringVar(&installDesktop, "desktop", "", "Desktop selected for privileged component preparation")
	flag.StringVar(&installUserSID, "user-sid", "", "Original desktop user receiving remote desktop access")
	flag.Parse()
	if mediaRoot == "" {
		if exe, err := os.Executable(); err == nil {
			mediaRoot = filepath.Dir(exe)
		}
	}
	root, err := os.UserConfigDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		if *install || (*prepare && preparationReportedExit != 1) {
			installationResult(err)
		}
		if *prepare {
			os.Exit(preparationReportedExit)
		}
		os.Exit(1)
	}
	logger, err := logging.Open(filepath.Join(root, "MyVMGuestTools", "logs"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		if *install || (*prepare && preparationReportedExit != 1) {
			installationResult(err)
		}
		if *prepare {
			os.Exit(preparationReportedExit)
		}
		os.Exit(1)
	}
	defer logger.Close()
	if *prepare {
		err = prepareSystem()
	} else if *install {
		err = installUser()
	} else {
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
		defer cancel()
		go runDesktop(ctx, logger)
		go guestops.Run(ctx, logger)
		err = sharing.RunProxy(ctx, logger, mapShares)
	}
	if err != nil {
		fmt.Fprintln(logger, err)
		fmt.Fprintln(os.Stderr, err)
		logger.Close()
		if *install || (*prepare && preparationReportedExit != 1) {
			installationResult(err)
		}
		if *prepare {
			// Windows uses a dedicated code after showing the detailed preparation error.
			os.Exit(preparationReportedExit)
		}
		os.Exit(1)
	}
	if *install {
		installationResult(nil)
	}
}
