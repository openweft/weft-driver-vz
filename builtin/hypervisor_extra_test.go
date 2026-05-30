//go:build darwin

package builtin

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	drivers "github.com/openweft/weft-drivers"
)

// TestHypervisor_AttachNIC_Unsupported locks the current contract:
// host-side virtio-net is wired into the unified VM config built by
// weft-agent, not by per-NIC AttachNIC calls. The method exists to
// satisfy the interface and must return ErrUnsupported so the
// scheduler doesn't dispatch live attach requests.
func TestHypervisor_AttachNIC_Unsupported(t *testing.T) {
	h := NewHypervisor(Options{HostUUID: "h"})
	err := h.AttachNIC(context.Background(), "vm", drivers.NICHandle{})
	if !errors.Is(err, drivers.ErrUnsupported) {
		t.Errorf("AttachNIC = %v, want ErrUnsupported", err)
	}
}

func TestHypervisor_DetachNIC_Unsupported(t *testing.T) {
	h := NewHypervisor(Options{HostUUID: "h"})
	err := h.DetachNIC(context.Background(), "vm", "en0")
	if !errors.Is(err, drivers.ErrUnsupported) {
		t.Errorf("DetachNIC = %v, want ErrUnsupported", err)
	}
}

// TestHypervisor_CreateVM_MkdirError verifies that mkdir failures
// (e.g. write-protected parent) surface as a CreateVM error rather
// than silently swallowed.
func TestHypervisor_CreateVM_MkdirError(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root can write anywhere; skip mkdir-failure path")
	}
	h := NewHypervisor(Options{HostUUID: "h"})
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o500); err != nil { // r-x only
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })
	vmDir := filepath.Join(parent, "child")
	if err := h.CreateVM(context.Background(), drivers.VMSpec{UUID: vmDir}); err == nil {
		t.Errorf("expected mkdir error when parent is read-only")
	}
}

// TestHypervisor_StopVM_PIDFileReadError exercises the "exists but
// unreadable" branch of os.ReadFile in StopVM — distinct from the
// IsNotExist path which is treated as success.
func TestHypervisor_StopVM_PIDFileReadError(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root can read anywhere; skip read-permission path")
	}
	if runtime.GOOS != "darwin" {
		t.Skip("file-permission semantics tested on darwin only")
	}
	h := NewHypervisor(Options{HostUUID: "h"})
	vmDir := t.TempDir()
	pidPath := filepath.Join(vmDir, "vm.pid")
	if err := os.WriteFile(pidPath, []byte("123\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Make file unreadable.
	if err := os.Chmod(pidPath, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(pidPath, 0o600) })
	err := h.StopVM(context.Background(), vmDir)
	if err == nil {
		t.Errorf("expected read error on unreadable vm.pid")
	}
}

// TestHypervisor_AttachDisk_CreateImageError verifies that when
// vz.CreateDiskImage fails (e.g. backing path inside a read-only
// directory), the error is surfaced.
func TestHypervisor_AttachDisk_CreateImageError(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root can write anywhere")
	}
	h := NewHypervisor(Options{HostUUID: "h"})
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })
	err := h.AttachDisk(context.Background(), parent, drivers.DiskSpec{
		BackingPath: filepath.Join(parent, "ghost.img"),
		SizeGiB:     1,
	})
	if err == nil {
		t.Errorf("expected create-image error when backing path parent is read-only")
	}
}

// TestHypervisor_DetachDisk_PermissionError covers the remove-error
// branch: file exists but parent dir is read-only so unlink fails.
func TestHypervisor_DetachDisk_PermissionError(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root can remove anywhere")
	}
	h := NewHypervisor(Options{HostUUID: "h"})
	parent := t.TempDir()
	backing := filepath.Join(parent, "data.img")
	if err := os.WriteFile(backing, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Make parent read-only so unlink fails.
	if err := os.Chmod(parent, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })
	err := h.DetachDisk(context.Background(), "vm", backing)
	if err == nil {
		t.Errorf("expected remove error when parent dir is read-only")
	}
}
