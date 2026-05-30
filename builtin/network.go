//go:build darwin

package builtin

// network.go is the scaffold for the Apple VZ NetworkDriver.
//
// Apple VZ exposes a limited set of network attachments natively:
//
//   * NATNetworkDeviceAttachment — host-shared NAT via Apple's
//     vmnet framework. Maps to weft.NetworkTypeNAT.
//   * BridgedNetworkDeviceAttachment — bridge onto a host
//     interface. Maps to weft.NetworkTypeBridged. Requires the
//     com.apple.vm.networking entitlement.
//   * (no native "isolated" — emulated via NAT with no upstream
//     gateway).
//
// For mesh-type networks (WireGuard), this driver delegates to
// the future wireguard-go-based sub-driver: the WG interface
// runs in userspace on the host, and per-VM ports become regular
// virtio-net devices on a host-local bridge that the WG iface
// terminates. That work is in a sibling module
// (weft-driver-wireguard) so it can be reused under QEMU/KVM and
// Cloud Hypervisor too.

import (
	"context"

	drivers "github.com/openweft/weft-drivers"
)

// Network implements drivers.NetworkDriver for Apple VZ hosts.
type Network struct {
	opts Options
}

func NewNetwork(o Options) *Network {
	return &Network{opts: o}
}

func (n *Network) HostInfo(ctx context.Context) (drivers.HostInfo, error) {
	return hostInfoFor(n.opts), nil
}

func (n *Network) EnsureNetwork(ctx context.Context, spec drivers.NetworkSpec) error {
	return drivers.ErrUnsupported
}

func (n *Network) DestroyNetwork(ctx context.Context, networkUUID string) error {
	return drivers.ErrUnsupported
}

func (n *Network) AttachPort(ctx context.Context, spec drivers.PortSpec) (drivers.NICHandle, error) {
	return drivers.NICHandle{}, drivers.ErrUnsupported
}

func (n *Network) DetachPort(ctx context.Context, portUUID string) error {
	return drivers.ErrUnsupported
}

func (n *Network) RotateMeshPeer(ctx context.Context, spec drivers.PortSpec) error {
	return drivers.ErrUnsupported
}

var _ drivers.NetworkDriver = (*Network)(nil)
