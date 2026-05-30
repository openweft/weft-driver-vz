//go:build darwin

package builtin

// volume.go is the scaffold for the host-local file-backed
// VolumeDriver. Maps weft.Volume entries to raw / qcow2 files
// under <stateDir>/volumes/<uuid>/disk.<format>.
//
// Local() == true: a file-backed volume can only attach to VMs
// running on the same host. The scheduler must co-locate the VM
// with the volume's host.
//
// Future siblings:
//
//   * weft-driver-ceph — Local() == false, cluster-wide RBD
//   * weft-driver-iscsi — Local() == false
//   * weft-driver-nfs — Local() == false

import (
	"context"

	drivers "github.com/openweft/weft-drivers"
)

// Volume implements drivers.VolumeDriver for host-local files.
type Volume struct {
	opts Options
	// stateDir is the root under which per-volume directories
	// live. Today resolved via the constructor; tomorrow may come
	// from a config block read by weft-agent.
	stateDir string
}

// VolumeOptions extends the shared Options with volume-driver
// specifics. Kept as its own struct so subsequent driver-specific
// fields (cache mode, default format, fsync policy) don't bloat
// the shared Options type.
type VolumeOptions struct {
	Options
	StateDir string
}

func NewVolume(o VolumeOptions) *Volume {
	return &Volume{opts: o.Options, stateDir: o.StateDir}
}

// Name identifies the backend. Matches the operator-facing
// `volume_backends` list in the Host registry.
func (v *Volume) Name() string { return "file" }

// Local is true: file-backed volumes are bound to this host.
func (v *Volume) Local() bool { return true }

func (v *Volume) HostInfo(ctx context.Context) (drivers.HostInfo, error) {
	return hostInfoFor(v.opts), nil
}

func (v *Volume) EnsureVolume(ctx context.Context, spec drivers.VolumeSpec) error {
	return drivers.ErrUnsupported
}

func (v *Volume) DestroyVolume(ctx context.Context, volumeUUID string) error {
	return drivers.ErrUnsupported
}

func (v *Volume) AttachVolume(ctx context.Context, volumeUUID, hostUUID string) (drivers.AttachedVolume, error) {
	return drivers.AttachedVolume{}, drivers.ErrUnsupported
}

func (v *Volume) DetachVolume(ctx context.Context, volumeUUID, hostUUID string) error {
	return drivers.ErrUnsupported
}

var _ drivers.VolumeDriver = (*Volume)(nil)
