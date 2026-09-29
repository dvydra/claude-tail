package main

import (
	"fmt"
	"strings"
)

func runWTF(cfg Config) error {
	if len(cfg.WTFArgs) > 0 {
		return fmt.Errorf("wtf: unsupported arguments: %s", strings.Join(cfg.WTFArgs, " "))
	}

	fmt.Println("No sessions active or seen today.")
	return nil
}
