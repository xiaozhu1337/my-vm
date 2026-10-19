package main

import (
	"context"
	"flag"
	"fmt"
	"my-vm/internal/logging"
	"my-vm/internal/sharing"
	"os"
	"os/signal"
	"path/filepath"
)

func main() {
	install := flag.Bool("install", false, "Install guest folder integration for this user")
	prepare := flag.Bool("prepare", false, "Prepare guest system components")
	flag.Parse()
	root, err := os.UserConfigDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	logger, err := logging.Open(filepath.Join(root, "MyVMGuestTools", "logs"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
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
		err = sharing.RunProxy(ctx, logger, mapShares)
	}
	if err != nil {
		fmt.Fprintln(logger, err)
		fmt.Fprintln(os.Stderr, err)
		logger.Close()
		os.Exit(1)
	}
}
