package main

import (
	"context"
	"flag"
	"fmt"
	"my-vm/internal/guestagent"
	"my-vm/internal/logging"
	"os"
	"os/signal"
	"path/filepath"
)

var mediaRoot, installDesktop, installUserSID, readyEvent string

func main() {
	install := flag.Bool("install", true, "Install all guest enhancements for this user; false runs the installed agent")
	prepare := flag.Bool("prepare", false, "Prepare guest system components")
	flag.StringVar(&mediaRoot, "media", "", "Root directory of the offline enhancement disc")
	flag.StringVar(&installDesktop, "desktop", "", "Desktop selected for privileged component preparation")
	flag.StringVar(&installUserSID, "user-sid", "", "Original desktop user receiving remote desktop access")
	flag.StringVar(&readyEvent, "ready-event", "", "Private installer startup notification")
	flag.Parse()
	if *prepare {
		*install = false
	}
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
		err = guestagent.Run(ctx, cancel, logger, mapShares, runDesktop, desktopStatus, readyEvent)
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
