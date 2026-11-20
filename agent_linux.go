package main

import (
	"fmt"
	"io"
)

func agentStatus() string { return "macOS only" }

func install(_ options, _ io.Writer) error {
	return fmt.Errorf("install requires macOS; use dustpan run for foreground operation")
}

func uninstall(_ string, _ io.Writer) error {
	return fmt.Errorf("uninstall requires macOS")
}
