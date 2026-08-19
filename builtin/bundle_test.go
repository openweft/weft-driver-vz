//go:build darwin

package builtin

import (
	"context"
	"errors"
	"runtime"
	"testing"

	drivers "github.com/openweft/weft-drivers"
)

// TestNewBundle_ReturnsFourDrivers verifies the bundle wires up
// the four drivers, each satisfies its interface, and each
// reports a consistent HostInfo.
func TestNewBundle_ReturnsFourDrivers(t *testing.T) {
	b := New(BundleOptions{
		Options: Options{
			HostUUID: "host-uuid-test",
			Hostname: "compute-test",
			AZ:       "us-east-1a",
		},
		StateDir: "/tmp/weft-driver-vz-test",
	})

	if b.Hypervisor == nil || b.Network == nil || b.Volume == nil || b.Image == nil {
		t.Fatalf("bundle missing a driver: %+v", b)
	}

	ctx := context.Background()
	wantInfo := drivers.HostInfo{
		UUID:         "host-uuid-test",
		Hostname:     "compute-test",
		AZ:           "us-east-1a",
		Hypervisor:   "apple-vz",
		Architecture: runtime.GOARCH,
		// Version is a build-time -ldflags override that defaults to "dev".
		// Asserting the package variable keeps this correct under a plain
		// `go test` and under a release build alike; hardcoding "" made every
		// macOS lane red the moment HostInfo.Version started being populated.
		Version: Version,
	}
	if wantInfo.Version == "" {
		t.Fatal("HostInfo.Version is empty: the field is no longer being populated")
	}

	for name, d := range map[string]interface {
		HostInfo(context.Context) (drivers.HostInfo, error)
	}{
		"hypervisor": b.Hypervisor,
		"network":    b.Network,
		"volume":     b.Volume,
		"image":      b.Image,
	} {
		got, err := d.HostInfo(ctx)
		if err != nil {
			t.Errorf("%s HostInfo: %v", name, err)
			continue
		}
		if got != wantInfo {
			t.Errorf("%s HostInfo = %+v, want %+v", name, got, wantInfo)
		}
	}
}

// TestVolumeBackendName confirms the operator-facing identifier
// matches what gets recorded in the Host registry's volume_backends.
func TestVolumeBackendName(t *testing.T) {
	v := NewVolume(VolumeOptions{StateDir: "/tmp/x"})
	if got := v.Name(); got != "file" {
		t.Errorf("Name() = %q, want file", got)
	}
	if !v.Local() {
		t.Errorf("file-backed volume must report Local() == true")
	}
}

// TestStubsReturnUnsupported is the contract test that documents
// the current state: every action method returns ErrUnsupported.
// As real implementations land, each of these expectations
// becomes a test that the action *actually works*.
func TestStubsReturnUnsupported(t *testing.T) {
	b := New(BundleOptions{Options: Options{HostUUID: "u"}})
	ctx := context.Background()

	checks := []struct {
		name string
		err  error
	}{
		// CreateVM with zero VMSpec returns an error (empty UUID),
		// not ErrUnsupported — see TestHypervisor_CreateVM_* for
		// the real contract.
		// StartVM with the zero-value Options returns
		// ErrUnsupported because SpawnVMCommand is nil — the
		// contract this test asserts. See
		// TestHypervisor_StartVM_* for the wired-up case.
		{"Hypervisor.StartVM", b.Hypervisor.StartVM(ctx, "vm")},
		// Hypervisor.StopVM + Hypervisor.DeleteVM + CreateVM
		// moved out of the "unsupported" list — see
		// hypervisor_test.go for their real contracts.
		{"Network.EnsureNetwork", b.Network.EnsureNetwork(ctx, drivers.NetworkSpec{})},
		{"Network.DestroyNetwork", b.Network.DestroyNetwork(ctx, "n")},
		{"Network.DetachPort", b.Network.DetachPort(ctx, "p")},
		{"Network.RotateMeshPeer", b.Network.RotateMeshPeer(ctx, drivers.PortSpec{})},
		{"Volume.EnsureVolume", b.Volume.EnsureVolume(ctx, drivers.VolumeSpec{})},
		{"Volume.DestroyVolume", b.Volume.DestroyVolume(ctx, "v")},
		{"Volume.DetachVolume", b.Volume.DetachVolume(ctx, "v", "h")},
		{"Image.Pull", b.Image.Pull(ctx, "ref")},
		{"Image.Delete", b.Image.Delete(ctx, "ref")},
	}
	for _, c := range checks {
		if !errors.Is(c.err, drivers.ErrUnsupported) {
			t.Errorf("%s: expected ErrUnsupported, got %v", c.name, c.err)
		}
	}

	// Hypervisor.AttachDisk on zero DiskSpec returns a plain
	// error (empty BackingPath), not ErrUnsupported.
	if err := b.Hypervisor.AttachDisk(ctx, "vm", drivers.DiskSpec{}); err == nil {
		t.Errorf("Hypervisor.AttachDisk with empty spec: expected error, got nil")
	}
	// Hypervisor.DetachDisk: empty volumeUUID is an error;
	// missing file is no-op (covered in hypervisor_test.go).
	if err := b.Hypervisor.DetachDisk(ctx, "vm", ""); err == nil {
		t.Errorf("Hypervisor.DetachDisk with empty volumeUUID: expected error, got nil")
	}

	// AttachPort / AttachVolume / LocalPath / InCache return
	// values alongside the error — separate group so the test
	// table stays clean.
	if _, err := b.Network.AttachPort(ctx, drivers.PortSpec{}); !errors.Is(err, drivers.ErrUnsupported) {
		t.Errorf("Network.AttachPort: expected ErrUnsupported, got %v", err)
	}
	if _, err := b.Volume.AttachVolume(ctx, "v", "h"); !errors.Is(err, drivers.ErrUnsupported) {
		t.Errorf("Volume.AttachVolume: expected ErrUnsupported, got %v", err)
	}
	if _, err := b.Image.LocalPath(ctx, "ref"); !errors.Is(err, drivers.ErrUnsupported) {
		t.Errorf("Image.LocalPath: expected ErrUnsupported, got %v", err)
	}
	if _, err := b.Image.InCache(ctx, "ref"); !errors.Is(err, drivers.ErrUnsupported) {
		t.Errorf("Image.InCache: expected ErrUnsupported, got %v", err)
	}
}
