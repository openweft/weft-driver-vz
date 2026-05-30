//go:build darwin && cgo

package builtin

// consoletab.go – CGo bridge to consoletab.m.
//
// registerConsoleTab must be called before machine.StartGraphicApplication so
// that the NSApplicationDidFinishLaunchingNotification observer is in place
// before NSApp launches.
//
// Migrated from `pkg/openweft/weft/consoletab.go` — this file
// belongs in the Apple-VZ driver because the AppKit / VZ Reboot
// button is tightly coupled to vz.VirtualMachine and lives only
// on macOS hosts.

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework Cocoa
#include <stdlib.h>

void mockRegisterConsoleTab(const char *vmName, const char *consolePath);
*/
import "C"
import (
	"fmt"
	"os"
	"time"
	"unsafe"

	govz "github.com/Code-Hex/vz/v3"
)

// gVMInstance holds the running VirtualMachine so goVMReboot can perform
// an in-process stop+restart when the Reboot button is pressed.
var gVMInstance *govz.VirtualMachine

// registerConsoleTab registers an observer that – once NSApp has finished
// launching – creates a "Console" tab in the VM graphical window.
// The tab polls consolePath every 200 ms and appends new bytes as green
// monospaced text on a black background.
func registerConsoleTab(name, consolePath string) {
	n := C.CString(name)
	p := C.CString(consolePath)
	defer C.free(unsafe.Pointer(n))
	defer C.free(unsafe.Pointer(p))
	C.mockRegisterConsoleTab(n, p)
}

// goVMReboot is called from the Objective-C Reboot button in the console tab.
// It sends a graceful ACPI shutdown request to the guest, waits up to 30 s for
// the machine to reach the stopped state (falling back to a hard stop), then
// starts the machine again – all in a background goroutine so NSApp is not
// blocked.
//
//export goVMReboot
func goVMReboot() {
	vm := gVMInstance
	if vm == nil {
		return
	}
	go func() {
		if _, err := vm.RequestStop(); err != nil {
			fmt.Fprintf(os.Stderr, "reboot: request stop: %v\n", err)
		}
		// Wait for the guest to stop (up to 30 s), then hard-stop if needed.
		deadline := time.Now().Add(30 * time.Second)
		for vm.State() != govz.VirtualMachineStateStopped {
			if time.Now().After(deadline) {
				if err := vm.Stop(); err != nil {
					fmt.Fprintf(os.Stderr, "reboot: hard stop: %v\n", err)
				}
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
		// Ensure fully stopped before restarting.
		for vm.State() != govz.VirtualMachineStateStopped {
			time.Sleep(200 * time.Millisecond)
		}
		if err := vm.Start(); err != nil {
			fmt.Fprintf(os.Stderr, "reboot: start: %v\n", err)
		}
	}()
}
