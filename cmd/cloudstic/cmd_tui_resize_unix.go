//go:build !windows

package main

import (
	"os"
	"os/signal"
	"syscall"
)

func tuiNotifyResize(ch chan<- os.Signal) {
	signal.Notify(ch, syscall.SIGWINCH)
}

func tuiStopResize(ch chan<- os.Signal) {
	signal.Stop(ch)
}
