package main

import (
	"os"
	"runtime"
)

// openTTY opens the controlling terminal device for the current OS.
// On Unix: /dev/tty
// On Windows: CONIN$ for input/read-write, CONOUT$ for write
func openTTY(flag int) (*os.File, error) {
	if runtime.GOOS == "windows" {
		if flag&os.O_WRONLY != 0 && flag&os.O_RDWR == 0 {
			f, err := os.OpenFile("CONOUT$", flag, 0)
			if err == nil {
				return f, nil
			}
			return os.Stdout, nil
		}
		f, err := os.OpenFile("CONIN$", flag, 0)
		if err == nil {
			return f, nil
		}
		return os.Stdin, nil
	}
	return os.OpenFile("/dev/tty", flag, 0)
}

func ttyUsable() bool {
	if runtime.GOOS == "windows" {
		return isCharDevice(os.Stdin) || isCharDevice(os.Stdout)
	}
	if !isCharDevice(os.Stdout) {
		return false
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
