//go:build darwin

package builtin

// hypervisor.go is the scaffold for the Apple VZ HypervisorDriver.
//
// Today, every action method returns drivers.ErrUnsupported. The
// real implementations will be lifted incrementally from weft's
// runvm.go + adapter.go's provisionVMDir / CloneVM / StartVM /
// StopVM / DeleteVM helpers as those methods are factored out of
// the control plane.
//
// The lift sequence (so each commit is small + reviewable):
//
//   1. CreateVM       ← provisionVMDir + machine-id + nvram setup
//   2. StartVM        ← fork "vz-vm-run" subprocess (the lifetime
//                       owner of the VZVirtualMachine)
//   3. StopVM         ← SIGTERM the subprocess + grace period
//   4. DeleteVM       ← os.RemoveAll(vmDir) after stop
//   5. AttachDisk     ← post-CloneVM disk attach for hot-plug
//   6. AttachNIC      ← virtio-net binding + MAC seed
//
// Each lift keeps the existing weft Adapter method as a thin
// wrapper that delegates to the driver — same external behaviour,
// just routed through the interface.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	vz "github.com/Code-Hex/vz/v3"
	drivers "github.com/openweft/weft-drivers"
)

// Hypervisor implements drivers.HypervisorDriver against Apple
// Virtualization.framework.
type Hypervisor struct {
	opts Options
}

// NewHypervisor constructs the driver. The caller (weft-agent or
// weft-control in single-host mode) injects the host identity.
func NewHypervisor(o Options) *Hypervisor {
	return &Hypervisor{opts: o}
}

// HostInfo returns the host this driver instance materialises VMs
// on. Used by the control plane's scheduler + audit logs.
func (h *Hypervisor) HostInfo(ctx context.Context) (drivers.HostInfo, error) {
	return hostInfoFor(h.opts), nil
}

// CreateVM provisions the host-side Apple VZ state for one VM:
//
//   * `<vmDir>/nvram.bin` — EFI variable store
//   * `<vmDir>/machine-id.bin` — Apple VZ machine identifier
//   * `<vmDir>/mac.txt` — locally administered MAC for the
//     guest NIC
//
// These three files together encode the VM's persistent host
// identity — the same machine-id + nvram is what makes a guest
// recognise itself as the same machine across reboots.
//
// Idempotent per the HypervisorDriver contract: re-running with
// the same UUID is a no-op for any file that already exists.
// The MAC is generated only on first call so a re-creation
// after a restart doesn't change the MAC under the guest's
// feet (DHCP leases, virtio-net config, anti-spoofing rules
// downstream all assume the MAC is stable).
//
// Transitional convention: `spec.UUID` is the absolute path to
// the VM directory, same as DeleteVM / StopVM / StartVM.
// `spec.CPUCount`, `MemoryMiB`, `BootKind`, `BootRef`, `Cmdline`
// aren't consumed yet — they become a `<vmDir>/vmspec.hcl`
// (next commit) so the runvm subprocess + the future
// reconciler can read them back.
//
// Apple VZ specifics: `vz.NewEFIVariableStore`,
// `vz.NewGenericMachineIdentifier`,
// `vz.NewRandomLocallyAdministeredMACAddress` are the first
// `vz.*` calls to live in the driver module — previously they
// were duplicated in `provisionVMDir` and `RegisterMicroVM`
// inside the weft module.
func (h *Hypervisor) CreateVM(ctx context.Context, spec drivers.VMSpec) error {
	if spec.UUID == "" {
		return fmt.Errorf("CreateVM: empty UUID")
	}
	vmDir := spec.UUID // transitional: UUID is the absolute path
	if err := os.MkdirAll(vmDir, 0o700); err != nil {
		return fmt.Errorf("CreateVM: mkdir %s: %w", vmDir, err)
	}
	nvramPath := filepath.Join(vmDir, "nvram.bin")
	if _, err := os.Stat(nvramPath); os.IsNotExist(err) {
		if _, err := vz.NewEFIVariableStore(nvramPath, vz.WithCreatingEFIVariableStore()); err != nil {
			return fmt.Errorf("CreateVM: nvram: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("CreateVM: stat nvram: %w", err)
	}
	machineIDPath := filepath.Join(vmDir, "machine-id.bin")
	if _, err := os.Stat(machineIDPath); os.IsNotExist(err) {
		mid, err := vz.NewGenericMachineIdentifier()
		if err != nil {
			return fmt.Errorf("CreateVM: machine identifier: %w", err)
		}
		if err := os.WriteFile(machineIDPath, mid.DataRepresentation(), 0o600); err != nil {
			return fmt.Errorf("CreateVM: write machine-id: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("CreateVM: stat machine-id: %w", err)
	}
	macPath := filepath.Join(vmDir, "mac.txt")
	if _, err := os.Stat(macPath); os.IsNotExist(err) {
		macAddr, err := vz.NewRandomLocallyAdministeredMACAddress()
		if err != nil {
			return fmt.Errorf("CreateVM: mac: %w", err)
		}
		if err := os.WriteFile(macPath, []byte(macAddr.String()), 0o600); err != nil {
			return fmt.Errorf("CreateVM: write mac: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("CreateVM: stat mac: %w", err)
	}
	return nil
}

// StartVM forks the VM's host-side subprocess and persists its
// PID for StopVM. The actual exec.Cmd is produced by
// Options.SpawnVMCommand — the driver doesn't know what binary
// it launches (weft-control forks `weft vz-vm-run`; weft-agent
// will fork its own launcher). When SpawnVMCommand is nil the
// driver returns ErrUnsupported — this is the dev/test path
// where no host-side runner is available.
//
// Lifecycle bookkeeping:
//
//   * vm.pid is written under the vmDir before this method
//     returns, so StopVM works immediately.
//   * A goroutine cmd.Wait()'s the subprocess, removes vm.pid on
//     exit, then invokes Options.OnVMExit (if set). This is how
//     the Adapter learns about crashes / graceful exits.
//
// Idempotence is NOT provided here — calling StartVM twice on
// the same vmUUID forks two subprocesses. Higher layers
// (scheduler / reconciler) own the "is this VM already running?"
// check via the vm.pid presence.
//
// Transitional convention: `vmUUID` is the absolute path to the
// VM's directory, same as DeleteVM / StopVM.
func (h *Hypervisor) StartVM(ctx context.Context, vmUUID string) error {
	if vmUUID == "" {
		return fmt.Errorf("StartVM: empty vmUUID")
	}
	if h.opts.SpawnVMCommand == nil {
		return drivers.ErrUnsupported
	}
	cmd, err := h.opts.SpawnVMCommand(vmUUID)
	if err != nil {
		return fmt.Errorf("StartVM: build command: %w", err)
	}
	if cmd == nil {
		return fmt.Errorf("StartVM: SpawnVMCommand returned nil cmd without error")
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("StartVM: fork: %w", err)
	}
	pidFile := filepath.Join(vmUUID, "vm.pid")
	// Matches the original Adapter behaviour: pid file write
	// failure is ignored. The VM is running; StopVM just won't
	// find anything to signal until the wait goroutine cleans up
	// (which it would anyway when the subprocess exits).
	_ = os.WriteFile(pidFile, []byte(strconv.Itoa(cmd.Process.Pid)), 0o600)
	onExit := h.opts.OnVMExit
	go func() {
		_ = cmd.Wait()
		_ = os.Remove(pidFile)
		if onExit != nil {
			onExit(vmUUID)
		}
	}()
	return nil
}

// StopVM signals graceful shutdown to the VM's host-side
// subprocess by reading its persisted PID file and sending
// SIGTERM. Idempotent per the interface contract:
//
//   * No vm.pid file → already stopped / never started → nil.
//   * PID points to a dead / unknown process → nil (not an
//     error). The reconciler treats "stop requested, no process
//     to signal" as the desired terminal state.
//   * Malformed PID content → error (operator-visible bug, not
//     a transient condition).
//
// Transitional convention: `vmUUID` is the absolute path to the
// VM's directory. See DeleteVM for the same convention; future
// VM-inventory work replaces this with a real UUID lookup.
//
// Note on signal escalation: per the HypervisorDriver contract,
// drivers may escalate to a hard stop after the ctx deadline.
// This impl only sends SIGTERM today — escalation comes when the
// weft-agent reconciler owns the deadline-tracking. The caller
// (Adapter.StopVM) is what waits today, by virtue of the host
// owning the subprocess.
func (h *Hypervisor) StopVM(ctx context.Context, vmUUID string) error {
	if vmUUID == "" {
		return fmt.Errorf("StopVM: empty vmUUID")
	}
	pidFile := filepath.Join(vmUUID, "vm.pid")
	data, err := os.ReadFile(pidFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("StopVM: read pid file: %w", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return fmt.Errorf("StopVM: parse pid: %w", err)
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		// On Unix FindProcess always succeeds; this branch only
		// fires on platforms where the lookup itself can fail.
		return nil
	}
	// Signal failure (e.g. process already exited) is the
	// idempotent path — return nil per the interface contract.
	_ = proc.Signal(syscall.SIGTERM)
	return nil
}

// DeleteVM removes the host-local state directory for a VM.
// Idempotent: missing directory is treated as success
// (os.RemoveAll's contract).
//
// Transitional convention: `vmUUID` is interpreted as the
// absolute path to the VM's directory. Once a VM inventory
// registry lands (Phase F), it becomes the stable UUID and this
// driver maintains an internal `<stateDir>/vms/<uuid>/` mapping.
// The weft Adapter currently computes the path and passes it
// here — see Adapter.DeleteVM.
func (h *Hypervisor) DeleteVM(ctx context.Context, vmUUID string) error {
	if vmUUID == "" {
		return fmt.Errorf("DeleteVM: empty vmUUID")
	}
	return os.RemoveAll(vmUUID)
}

// AttachDisk wires a disk into the VM's host-side state. In
// the transitional model (pre-VolumeDriver), the driver is also
// responsible for creating the backing file when it doesn't
// already exist + the spec carries a positive SizeGiB:
//
//   * BackingPath exists                       → idempotent no-op
//   * BackingPath missing, SizeGiB > 0         → vz.CreateDiskImage
//   * BackingPath missing, SizeGiB == 0        → error
//
// The actual "tell the VM to open this file" step lives in the
// runvm.go subprocess that reads config.json; AttachDisk only
// makes the backing storage ready. Once VolumeDriver lands,
// this method becomes purely descriptive (records the
// attachment in the VM's vmspec.hcl) and creation moves to
// VolumeDriver.EnsureVolume.
//
// Idempotent: a re-call with the same BackingPath after the
// file already exists is success, not an error. The size is
// NOT verified against the on-disk file — that's the
// reconciler's job once VolumeDriver lands.
func (h *Hypervisor) AttachDisk(ctx context.Context, vmUUID string, disk drivers.DiskSpec) error {
	if vmUUID == "" {
		return fmt.Errorf("AttachDisk: empty vmUUID")
	}
	if disk.BackingPath == "" {
		return fmt.Errorf("AttachDisk: empty BackingPath")
	}
	if _, err := os.Stat(disk.BackingPath); err == nil {
		// File already there — idempotent path.
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("AttachDisk: stat %s: %w", disk.BackingPath, err)
	}
	if disk.SizeGiB <= 0 {
		return fmt.Errorf("AttachDisk: BackingPath %s missing and no SizeGiB to create it", disk.BackingPath)
	}
	if err := os.MkdirAll(filepath.Dir(disk.BackingPath), 0o700); err != nil {
		return fmt.Errorf("AttachDisk: mkdir parent: %w", err)
	}
	sizeBytes := int64(disk.SizeGiB) * 1024 * 1024 * 1024
	if err := vz.CreateDiskImage(disk.BackingPath, sizeBytes); err != nil {
		return fmt.Errorf("AttachDisk: create disk image: %w", err)
	}
	return nil
}

// DetachDisk removes the backing file associated with a disk
// attachment. Transitional convention: `volumeUUID` is the
// absolute path to the backing file (same shape as DiskSpec.
// BackingPath in AttachDisk). Once VolumeDriver lands, the
// backing file's lifetime moves there and this method becomes
// purely descriptive ("forget the attachment").
//
// Idempotent per the HypervisorDriver contract: missing file is
// success, not an error. Empty volumeUUID is a caller bug
// (surfaced as an error). vmUUID is currently unused —
// preserved in the signature for the future VM-inventory
// integration where the driver records which VMs reference
// which volumes.
func (h *Hypervisor) DetachDisk(ctx context.Context, vmUUID, volumeUUID string) error {
	if volumeUUID == "" {
		return fmt.Errorf("DetachDisk: empty volumeUUID")
	}
	if err := os.Remove(volumeUUID); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("DetachDisk: remove %s: %w", volumeUUID, err)
	}
	return nil
}

func (h *Hypervisor) AttachNIC(ctx context.Context, vmUUID string, nic drivers.NICHandle) error {
	return drivers.ErrUnsupported
}

func (h *Hypervisor) DetachNIC(ctx context.Context, vmUUID, nicDevice string) error {
	return drivers.ErrUnsupported
}

// Compile-time guarantee that we satisfy the interface. Any
// future drift between the API module and this impl is caught at
// build time.
var _ drivers.HypervisorDriver = (*Hypervisor)(nil)
