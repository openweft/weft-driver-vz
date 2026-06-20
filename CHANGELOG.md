# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project aims to adhere to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Limitation lifted

- **AF_VSOCK CID readback now arrives from the guest, not the host.**
  Apple's `Virtualization.framework` still doesn't expose the
  assigned CID on the host side (re-audited 2026-06-20 — see the
  archived note below), but we no longer need to ask Apple : the
  guest reads its own CID via `IOCTL_VM_SOCKETS_GET_LOCAL_CID` on
  `/dev/vsock` and ships it on every `GuestHello.reported_cid`
  (weft-proto v0.16.0). The host (weft v0.4.51) cross-checks that
  value against the kernel-level `peer.CID()` of the AF_VSOCK
  socket — two independent kernel observations of the same CID,
  so disagreement = spoofing. After agreement, the host
  autoregisters the pod in its `podCIDs` registry, arming
  strict-when-known for every subsequent stream. VZ-backed
  microVMs are now protected at the same level as QEMU-backed
  ones.

### Documented (archived)

- **No host-side CID accessor in Apple's framework.**
  `VZVirtioSocketDevice` exposes only
  `setSocketListener:forPort:`, `removeSocketListenerForPort:`
  and `connectToPort:completionHandler:` ;
  `VZVirtioSocketConnection` exposes only `sourcePort`,
  `destinationPort`, `fileDescriptor`. No
  `contextID`/`guestCID` anywhere on the public surface (verified
  against the macOS SDK headers, copyright through 2025, and
  against `github.com/Code-Hex/vz/v3@v3.7.1`). This motivated the
  guest-reported path above ; if a future macOS release ever adds
  a host-side accessor, the guest report becomes redundant
  defense-in-depth rather than the primary signal.

### Fixed

- **`builtin.Volume` satisfies the grown `drivers.VolumeDriver`
  interface** : the upstream interface added Create/List/Delete/
  RevertSnapshot + Create/List/Delete/RestoreBackup ; without
  the matching stubs the package no longer compiled (the
  compile-time assertion at the bottom of `volume.go` failed).
  Stubs return `drivers.ErrUnsupported` ; `weft-block` remains
  the documented home for the snapshot/backup surface on this
  backend. Commit `d180ccc`.

## [0.2.0] - 2026-06-02

### Changed

- **Explicit PCI passthrough reject** : when the agent forwards a
  start request carrying `RequestedPCI`, the VZ driver now returns
  a typed `ErrPCIPassthroughUnsupported` instead of silently
  ignoring the field. Apple Virtualization.framework does not
  expose an IOMMU passthrough surface ; the right answer is to
  fail fast so the operator picks a host with a QEMU driver.
  Commit `e2cb5b3`.

## [0.1.0] - 2026-05-31

Initial release. Apple Virtualization.framework driver plugin for
`weft agent` on macOS. Requires the
`com.apple.security.virtualization` entitlement on the signed
binary. Implements the `weft-driver-plugin` gRPC contract over
go-plugin stdio. BSD 3-Clause LICENSE (`6875f4f`).
