package main

import (
	"os"
	"runtime"
)

// openTTY opens the controlling terminal device for the current OS.
// On Unix: /dev/tty
// On Windows: CONIN$ or CONOUT$
func openTTY(flag int) (*os.File, error) {
	if runtime.GOOS == "windows" {
		path := "CONIN$"
		if flag&(os.O_WRONLY|os.O_RDWR) != 0 {
			path = "CONOUT$"
		}
		f, err := os.OpenFile(path, flag, 0)
		if err == nil {
			return f, nil
		}
		if isCharDevice(os.Stdin) {
			return os.Stdin, nil
		}
		return nil, err
	}
	return os.OpenFile("/dev/tty", flag, 0)
}

func ttyUsable() bool {
	if !isCharDevice(os.Stdout) {
		return false
	}
	if runtime.GOOS == "windows" {
		return isCharDevice(os.Stdin)
	}
	f, err := openTTY(os.O_RDONLY)
	if err == nil {
		if f != os.Stdin {
			f.Close()
		}
		return true
	}
	return false
}
