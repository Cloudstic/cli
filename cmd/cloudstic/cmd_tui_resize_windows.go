//go:build windows

package main

import "os"

func tuiNotifyResize(ch chan<- os.Signal) {}

func tuiStopResize(ch chan<- os.Signal) {}
