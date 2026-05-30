//go:build darwin

package builtin

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	drivers "github.com/openweft/weft-drivers"
)

func TestHypervisor_DeleteVM_RemovesDir(t *testing.T) {
	h := NewHypervisor(Options{HostUUID: "h"})
	dir := filepath.Join(t.TempDir(), "vm-1")
	if err := os.MkdirAll(filepath.Join(dir, "subdir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := h.DeleteVM(context.Background(), dir); err != nil {
		t.Fatalf("DeleteVM: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("expected vmDir gone, stat err = %v", err)
	}
}

// TestHypervisor_DeleteVM_Idempotent confirms the interface
// contract: missing path is success, not an error. Lets the
// reconciler retry safely.
func TestHypervisor_DeleteVM_Idempotent(t *testing.T) {
	h := NewHypervisor(Options{HostUUID: "h"})
	if err := h.DeleteVM(context.Background(), filepath.Join(t.TempDir(), "never-existed")); err != nil {
		t.Errorf("delete of missing path should be no-op, got %v", err)
	}
}

func TestHypervisor_DeleteVM_RejectsEmpty(t *testing.T) {
	h := NewHypervisor(Options{HostUUID: "h"})
	err := h.DeleteVM(context.Background(), "")
	if err == nil {
		t.Errorf("empty vmUUID should be rejected")
	}
	// And it's a plain error, NOT ErrUnsupported — empty is a
	// caller bug, not a "not implemented" condition.
	if errors.Is(err, drivers.ErrUnsupported) {
		t.Errorf("empty vmUUID should not return ErrUnsupported")
	}
}

// TestHypervisor_StopVM_SignalsRunningProcess spawns a real
// sleeping subprocess, writes its PID to vm.pid the way the VM
// fork path does, then calls StopVM. The subprocess should
// receive SIGTERM and exit within the test deadline.
func TestHypervisor_StopVM_SignalsRunningProcess(t *testing.T) {
	h := NewHypervisor(Options{HostUUID: "h"})
	vmDir := t.TempDir()

	// `sleep 30` is portable on darwin + linux. The signal will
	// abort it well before the 30s wallclock.
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("spawn sleep: %v", err)
	}
	pidStr := strconv.Itoa(cmd.Process.Pid)
	if err := os.WriteFile(filepath.Join(vmDir, "vm.pid"), []byte(pidStr+"\n"), 0o600); err != nil {
		t.Fatalf("write vm.pid: %v", err)
	}

	if err := h.StopVM(context.Background(), vmDir); err != nil {
		t.Fatalf("StopVM: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
		// Subprocess exited (signal-killed). Success.
	case <-time.After(3 * time.Second):
		// Hard-kill so the test process doesn't leak a child.
		_ = cmd.Process.Kill()
		<-done
		t.Fatalf("subprocess did not exit within 3s of StopVM — SIGTERM not delivered")
	}
}

// TestHypervisor_StopVM_NoPidFile is the "already stopped"
// idempotence case: vmDir exists but vm.pid was never written
// (or has been removed by the subprocess on graceful exit).
// Per the contract this is success, not an error.
func TestHypervisor_StopVM_NoPidFile(t *testing.T) {
	h := NewHypervisor(Options{HostUUID: "h"})
	vmDir := t.TempDir()
	if err := h.StopVM(context.Background(), vmDir); err != nil {
		t.Errorf("missing vm.pid should be no-op, got %v", err)
	}
}

// TestHypervisor_StopVM_DeadPID covers the "process already
// exited" case. Per the contract this is also no-op.
func TestHypervisor_StopVM_DeadPID(t *testing.T) {
	h := NewHypervisor(Options{HostUUID: "h"})
	vmDir := t.TempDir()
	// Spawn + immediately wait so we have a known-dead PID.
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	deadPID := strconv.Itoa(cmd.Process.Pid)
	if err := os.WriteFile(filepath.Join(vmDir, "vm.pid"), []byte(deadPID), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := h.StopVM(context.Background(), vmDir); err != nil {
		t.Errorf("dead PID should be no-op, got %v", err)
	}
}

// TestHypervisor_StopVM_MalformedPID confirms a corrupted
// vm.pid file surfaces as an error (it's not a transient
// "already stopped" condition — operator should know).
func TestHypervisor_StopVM_MalformedPID(t *testing.T) {
	h := NewHypervisor(Options{HostUUID: "h"})
	vmDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(vmDir, "vm.pid"), []byte("not-a-number"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := h.StopVM(context.Background(), vmDir); err == nil {
		t.Errorf("malformed PID should surface as error")
	}
}

func TestHypervisor_StopVM_RejectsEmpty(t *testing.T) {
	h := NewHypervisor(Options{HostUUID: "h"})
	if err := h.StopVM(context.Background(), ""); err == nil {
		t.Errorf("empty vmUUID should be rejected")
	}
}

// TestHypervisor_StartVM_RejectsEmpty covers the contract:
// empty vmUUID is a caller bug, surfaced as a plain error
// (not ErrUnsupported).
func TestHypervisor_StartVM_RejectsEmpty(t *testing.T) {
	h := NewHypervisor(Options{HostUUID: "h"})
	if err := h.StartVM(context.Background(), ""); err == nil {
		t.Errorf("empty vmUUID should be rejected")
	}
}

// TestHypervisor_StartVM_UnsupportedWithoutSpawn verifies the
// contract for the no-spawn case: a driver without a
// SpawnVMCommand closure returns ErrUnsupported so the caller
// can detect "this driver doesn't know how to launch processes"
// (matches the dev/test path where weft-control isn't wired to
// fork its own binary).
func TestHypervisor_StartVM_UnsupportedWithoutSpawn(t *testing.T) {
	h := NewHypervisor(Options{HostUUID: "h"})
	err := h.StartVM(context.Background(), t.TempDir())
	if !errors.Is(err, drivers.ErrUnsupported) {
		t.Errorf("nil SpawnVMCommand should yield ErrUnsupported, got %v", err)
	}
}

// TestHypervisor_StartVM_ForksWritesPIDAndOnExit covers the
// full happy path:
//
//   * the SpawnVMCommand closure is invoked with the vmDir
//   * the subprocess is forked
//   * vm.pid is written with the subprocess's PID
//   * OnVMExit fires when the subprocess exits + vm.pid is
//     removed before it does
//
// Uses `sleep 30` as a stand-in for vz-vm-run so the test stays
// portable + finishes in < 1s after the StopVM kill.
func TestHypervisor_StartVM_ForksWritesPIDAndOnExit(t *testing.T) {
	vmDir := t.TempDir()
	gotExit := make(chan string, 1)
	spawnedFor := ""
	h := NewHypervisor(Options{
		HostUUID: "h",
		SpawnVMCommand: func(d string) (*exec.Cmd, error) {
			spawnedFor = d
			return exec.Command("sleep", "30"), nil
		},
		OnVMExit: func(d string) { gotExit <- d },
	})

	if err := h.StartVM(context.Background(), vmDir); err != nil {
		t.Fatalf("StartVM: %v", err)
	}
	if spawnedFor != vmDir {
		t.Errorf("SpawnVMCommand received %q, want %q", spawnedFor, vmDir)
	}

	// vm.pid is written before StartVM returns.
	data, err := os.ReadFile(filepath.Join(vmDir, "vm.pid"))
	if err != nil {
		t.Fatalf("read vm.pid: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		t.Fatalf("vm.pid content %q invalid: %v", data, err)
	}

	// Signal the subprocess so it exits + the wait goroutine
	// fires OnVMExit.
	proc, err := os.FindProcess(pid)
	if err != nil {
		t.Fatalf("find subprocess: %v", err)
	}
	if err := proc.Kill(); err != nil {
		t.Fatalf("kill subprocess: %v", err)
	}

	select {
	case d := <-gotExit:
		if d != vmDir {
			t.Errorf("OnVMExit received %q, want %q", d, vmDir)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("OnVMExit did not fire within 3s of subprocess kill")
	}

	// vm.pid removed by the wait goroutine before OnVMExit.
	if _, err := os.Stat(filepath.Join(vmDir, "vm.pid")); !os.IsNotExist(err) {
		t.Errorf("vm.pid should be removed after exit, stat err = %v", err)
	}
}

// TestHypervisor_StartVM_SpawnError surfaces closure errors as
// plain errors (not ErrUnsupported — the closure exists and
// chose to fail, which is different from "not implemented").
func TestHypervisor_StartVM_SpawnError(t *testing.T) {
	h := NewHypervisor(Options{
		HostUUID: "h",
		SpawnVMCommand: func(d string) (*exec.Cmd, error) {
			return nil, errors.New("boom")
		},
	})
	err := h.StartVM(context.Background(), t.TempDir())
	if err == nil {
		t.Fatal("expected error from closure failure")
	}
	if errors.Is(err, drivers.ErrUnsupported) {
		t.Errorf("closure failure should not surface as ErrUnsupported")
	}
}

// TestHypervisor_StartVM_NilCmdRejected guards against a
// closure that forgets to return an error when it returns nil.
func TestHypervisor_StartVM_NilCmdRejected(t *testing.T) {
	h := NewHypervisor(Options{
		HostUUID: "h",
		SpawnVMCommand: func(d string) (*exec.Cmd, error) {
			return nil, nil
		},
	})
	if err := h.StartVM(context.Background(), t.TempDir()); err == nil {
		t.Errorf("nil cmd with no error should be rejected")
	}
}

// TestHypervisor_CreateVM_WritesThreeFiles covers the happy
// path: nvram.bin, machine-id.bin, mac.txt all materialise
// under the vmDir.
func TestHypervisor_CreateVM_WritesThreeFiles(t *testing.T) {
	h := NewHypervisor(Options{HostUUID: "h"})
	vmDir := t.TempDir()
	if err := h.CreateVM(context.Background(), drivers.VMSpec{UUID: vmDir}); err != nil {
		t.Fatalf("CreateVM: %v", err)
	}
	for _, name := range []string{"nvram.bin", "machine-id.bin", "mac.txt"} {
		st, err := os.Stat(filepath.Join(vmDir, name))
		if err != nil {
			t.Errorf("expected %s, stat err = %v", name, err)
			continue
		}
		if st.Size() == 0 {
			t.Errorf("%s exists but is empty", name)
		}
	}
	// mac.txt content shape: "xx:xx:xx:xx:xx:xx".
	mac, err := os.ReadFile(filepath.Join(vmDir, "mac.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(mac), ":") || strings.Count(string(mac), ":") != 5 {
		t.Errorf("mac.txt content %q doesn't look like a MAC", mac)
	}
}

// TestHypervisor_CreateVM_Idempotent covers the contract: re-
// running CreateVM with the same UUID is a no-op AND preserves
// the existing identity (the MAC must NOT be regenerated, since
// downstream DHCP / virtio-net config keys off it).
func TestHypervisor_CreateVM_Idempotent(t *testing.T) {
	h := NewHypervisor(Options{HostUUID: "h"})
	vmDir := t.TempDir()
	if err := h.CreateVM(context.Background(), drivers.VMSpec{UUID: vmDir}); err != nil {
		t.Fatal(err)
	}
	macBefore, err := os.ReadFile(filepath.Join(vmDir, "mac.txt"))
	if err != nil {
		t.Fatal(err)
	}
	midBefore, err := os.ReadFile(filepath.Join(vmDir, "machine-id.bin"))
	if err != nil {
		t.Fatal(err)
	}
	nvramStBefore, err := os.Stat(filepath.Join(vmDir, "nvram.bin"))
	if err != nil {
		t.Fatal(err)
	}

	// Second call.
	if err := h.CreateVM(context.Background(), drivers.VMSpec{UUID: vmDir}); err != nil {
		t.Fatalf("second CreateVM: %v", err)
	}

	macAfter, _ := os.ReadFile(filepath.Join(vmDir, "mac.txt"))
	midAfter, _ := os.ReadFile(filepath.Join(vmDir, "machine-id.bin"))
	nvramStAfter, _ := os.Stat(filepath.Join(vmDir, "nvram.bin"))

	if string(macBefore) != string(macAfter) {
		t.Errorf("MAC changed across CreateVM calls: %q → %q", macBefore, macAfter)
	}
	if string(midBefore) != string(midAfter) {
		t.Errorf("machine-id changed across CreateVM calls")
	}
	if nvramStBefore.ModTime() != nvramStAfter.ModTime() {
		t.Errorf("nvram.bin was rewritten on idempotent CreateVM")
	}
}

func TestHypervisor_CreateVM_RejectsEmpty(t *testing.T) {
	h := NewHypervisor(Options{HostUUID: "h"})
	err := h.CreateVM(context.Background(), drivers.VMSpec{})
	if err == nil {
		t.Errorf("empty UUID should be rejected")
	}
	if errors.Is(err, drivers.ErrUnsupported) {
		t.Errorf("empty UUID should not return ErrUnsupported")
	}
}

// TestHypervisor_CreateVM_MkdirsVMDir confirms the driver
// creates the directory if it doesn't already exist. The weft
// Adapter currently mkdirs the vmDir before calling, but a
// reconciler or weft-agent might call CreateVM on a fresh
// host where the dir hasn't been touched yet — the contract
// must support that.
func TestHypervisor_CreateVM_MkdirsVMDir(t *testing.T) {
	h := NewHypervisor(Options{HostUUID: "h"})
	vmDir := filepath.Join(t.TempDir(), "subdir-not-yet-created")
	if err := h.CreateVM(context.Background(), drivers.VMSpec{UUID: vmDir}); err != nil {
		t.Fatalf("CreateVM should mkdir, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(vmDir, "mac.txt")); err != nil {
		t.Errorf("mac.txt missing after CreateVM that created its dir: %v", err)
	}
}

// TestHypervisor_AttachDisk_CreatesBackingFile exercises the
// transitional path: SizeGiB > 0 → vz.CreateDiskImage. Use a
// small size (1 GiB → sparse file, instant) so the test stays
// fast.
func TestHypervisor_AttachDisk_CreatesBackingFile(t *testing.T) {
	h := NewHypervisor(Options{HostUUID: "h"})
	vmDir := t.TempDir()
	backing := filepath.Join(vmDir, "data-0.img")
	err := h.AttachDisk(context.Background(), vmDir, drivers.DiskSpec{
		BackingPath: backing,
		SizeGiB:     1,
	})
	if err != nil {
		t.Fatalf("AttachDisk: %v", err)
	}
	st, err := os.Stat(backing)
	if err != nil {
		t.Fatalf("backing file not created: %v", err)
	}
	// 1 GiB exact (sparse file's apparent size).
	if st.Size() != int64(1)*1024*1024*1024 {
		t.Errorf("backing size = %d, want 1 GiB", st.Size())
	}
}

// TestHypervisor_AttachDisk_Idempotent confirms an existing
// backing file is left alone — no truncation, no re-creation.
// Critical: the operator's data must survive a reconciler retry.
func TestHypervisor_AttachDisk_Idempotent(t *testing.T) {
	h := NewHypervisor(Options{HostUUID: "h"})
	vmDir := t.TempDir()
	backing := filepath.Join(vmDir, "data.img")
	// Pre-create with some content so we can detect overwrites.
	const sentinel = "DO NOT TRUNCATE ME"
	if err := os.WriteFile(backing, []byte(sentinel), 0o600); err != nil {
		t.Fatal(err)
	}
	// AttachDisk with SizeGiB > 0 — should NOT touch the file.
	if err := h.AttachDisk(context.Background(), vmDir, drivers.DiskSpec{
		BackingPath: backing,
		SizeGiB:     10,
	}); err != nil {
		t.Fatalf("AttachDisk: %v", err)
	}
	got, err := os.ReadFile(backing)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != sentinel {
		t.Errorf("existing backing file was overwritten: got %q, want %q", got, sentinel)
	}
}

// TestHypervisor_AttachDisk_MissingFileNoSize is the failure
// contract: BackingPath missing AND SizeGiB == 0 means the
// caller forgot to either pre-create the file OR ask the
// driver to. Surface this as an error so the operator can
// fix it, not a silent skip.
func TestHypervisor_AttachDisk_MissingFileNoSize(t *testing.T) {
	h := NewHypervisor(Options{HostUUID: "h"})
	vmDir := t.TempDir()
	err := h.AttachDisk(context.Background(), vmDir, drivers.DiskSpec{
		BackingPath: filepath.Join(vmDir, "ghost.img"),
		// SizeGiB == 0
	})
	if err == nil {
		t.Errorf("missing backing path + no SizeGiB should be an error")
	}
}

func TestHypervisor_AttachDisk_RejectsEmpty(t *testing.T) {
	h := NewHypervisor(Options{HostUUID: "h"})
	if err := h.AttachDisk(context.Background(), "", drivers.DiskSpec{BackingPath: "/x"}); err == nil {
		t.Errorf("empty vmUUID should be rejected")
	}
	if err := h.AttachDisk(context.Background(), t.TempDir(), drivers.DiskSpec{}); err == nil {
		t.Errorf("empty BackingPath should be rejected")
	}
}

// TestHypervisor_DetachDisk_RemovesBackingFile covers the happy
// path: existing file → removed.
func TestHypervisor_DetachDisk_RemovesBackingFile(t *testing.T) {
	h := NewHypervisor(Options{HostUUID: "h"})
	vmDir := t.TempDir()
	backing := filepath.Join(vmDir, "data.img")
	if err := os.WriteFile(backing, []byte("disk content"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := h.DetachDisk(context.Background(), vmDir, backing); err != nil {
		t.Fatalf("DetachDisk: %v", err)
	}
	if _, err := os.Stat(backing); !os.IsNotExist(err) {
		t.Errorf("backing file should be gone, stat err = %v", err)
	}
}

// TestHypervisor_DetachDisk_Idempotent: removing a file that
// doesn't exist is success, matching the interface's "missing
// → nil" contract.
func TestHypervisor_DetachDisk_Idempotent(t *testing.T) {
	h := NewHypervisor(Options{HostUUID: "h"})
	if err := h.DetachDisk(context.Background(), "vm", filepath.Join(t.TempDir(), "never-existed.img")); err != nil {
		t.Errorf("missing file should be no-op, got %v", err)
	}
}

func TestHypervisor_DetachDisk_RejectsEmpty(t *testing.T) {
	h := NewHypervisor(Options{HostUUID: "h"})
	if err := h.DetachDisk(context.Background(), "vm", ""); err == nil {
		t.Errorf("empty volumeUUID should be rejected")
	}
}
