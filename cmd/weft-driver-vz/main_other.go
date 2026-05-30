//go:build !darwin

// The Apple-VZ driver plugin only builds on macOS — it binds
// Virtualization.framework via cgo. On other platforms this stub exists so
// `go build ./...` succeeds; weft launches weft-driver-qemu there instead.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "weft-driver-vz requires macOS (Apple Virtualization.framework); use weft-driver-qemu elsewhere")
	os.Exit(1)
}
