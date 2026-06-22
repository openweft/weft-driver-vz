//go:build darwin

package builtin

// host_info.go centralises the HostInfo derivation every driver
// type in this module surfaces. All four drivers (Hypervisor,
// Network, Volume, Image) report the same HostInfo since they
// all run on the same physical macOS host — the bundle's
// `New()` factory injects it.
//
// The HostUUID is provided by the caller (typically the
// weft-agent's startup code, which loads or generates a stable
// UUID at /var/lib/weft/host-uuid). This module never invents
// UUIDs on its own — that would mean the same host could
// register with different identities across reboots.

import (
	"os/exec"
	"runtime"

	drivers "github.com/openweft/weft-drivers"
)

// Options bundles the construction inputs for the driver
// instances. Fields are minimal on purpose; per-driver tuning
// goes into per-driver Options structs as the impls grow.
//
// SpawnVMCommand + OnVMExit are the two hooks that let the
// Hypervisor driver fork + monitor VM subprocesses without
// depending on the host process layout. The weft Adapter wires
// them to its `vz-vm-run` subcommand + RecordEvent helpers; a
// future weft-agent binary plugs the same hooks into whatever
// VM launcher it ships.
type Options struct {
	// HostUUID is the stable cluster identity of this host. The
	// caller persists it (typically at /var/lib/weft/host-uuid).
	HostUUID string
	// Hostname overrides os.Hostname() — useful in tests + when
	// the operator wants a logical name independent of DNS.
	Hostname string
	// AZ is the availability-zone label this host belongs to.
	AZ string
	// SpawnVMCommand produces the *exec.Cmd that becomes the VM's
	// host-side process for a given vmDir. Returning a nil cmd is
	// not allowed; return an error instead.
	//
	// In single-host weft-control mode this closure forks
	// `weft vz-vm-run --vmdir=…`. In multi-host weft-agent mode it
	// forks the agent's own VM launcher. The driver does NOT
	// dictate the shape.
	SpawnVMCommand func(vmDir string) (*exec.Cmd, error)
	// OnVMExit fires from the driver's wait goroutine when a
	// VM subprocess exits. Optional; nil disables the callback.
	// The weft Adapter uses it to publish `server.vz_vm_run_exited`
	// events via RecordEvent.
	OnVMExit func(vmDir string)
	// OnEvent is the bridge for fine-grained driver lifecycle
	// events (`vz_vm_run.*`, `vz.state.*`, `guest.<WEFT_MARK>`)
	// produced inside the vz-vm-run subprocess. The weft Adapter
	// wires this to weft.RecordEvent so the subprocess's events
	// land in the same `timings.jsonl` stream as control-plane
	// events. nil disables the bridge (events become best-effort
	// stderr lines only).
	OnEvent func(vmDir, kind string, meta map[string]string)
}

// Version is the compile-time build version of weft-driver-vz.
// Set via -ldflags "-X github.com/openweft/weft-driver-vz/builtin.Version=vX.Y.Z"
// at link time ; "dev" for un-stamped builds. Reported in HostInfo
// so weft can surface it in the TUI / webui chrome.
var Version = "dev"

// hostInfoFor returns the HostInfo every driver in this bundle
// surfaces. Architecture comes from runtime.GOARCH ("arm64" on
// Apple silicon, "amd64" on Intel Macs).
func hostInfoFor(o Options) drivers.HostInfo {
	return drivers.HostInfo{
		UUID:         o.HostUUID,
		Hostname:     o.Hostname,
		AZ:           o.AZ,
		Hypervisor:   "apple-vz",
		Architecture: runtime.GOARCH,
		Version:      Version,
	}
}
