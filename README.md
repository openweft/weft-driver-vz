<p align="center"><img src="https://raw.githubusercontent.com/openweft/brand/main/social/openweft.png" alt="openweft" width="720"></p>

# weft-driver-vz

The macOS / Apple Virtualization.framework driver bundle for weft.
Implements:

- `HypervisorDriver` — VM lifecycle on Apple VZ
- `NetworkDriver` — bridged / NAT / isolated networking via VZ's
  built-in network device, plus a future WireGuard backend for
  mesh-type networks
- `VolumeDriver` — host-local raw / qcow2 backing files
- `ImageDriver` — OCI cache via the existing `imagestore`
  package

Build-tagged `darwin` only — Apple VZ doesn't exist on Linux.

## Status

**Scaffold phase.** Today this module just declares the four
driver interfaces and a shared `HostInfo` helper. Action methods
return `drivers.ErrUnsupported` — the actual implementation lives
in [`../weft`](../weft) (runvm.go, adapter.go's
provisionVMDir/CloneVM/StartVM/StopVM/DeleteVM, etc.).

Subsequent commits port one method at a time from `weft` into
this module, replacing the corresponding direct-syscall code in
weft's Adapter with a call through the driver interface.

## Build

This module only depends on `github.com/openweft/weft-drivers`
(driver interfaces). It must NEVER import the weft control-plane
module — that would create a cycle and defeat the per-driver
isolation goal.
