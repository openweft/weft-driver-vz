# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project aims to adhere to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Documented

- **AF_VSOCK CID readback is not implementable on this backend.**
  Apple's `Virtualization.framework` does not expose the guest CID
  the framework assigns at boot. `VZVirtioSocketDevice` exposes
  only `setSocketListener:forPort:`, `removeSocketListenerForPort:`
  and `connectToPort:completionHandler:` ; `VZVirtioSocketConnection`
  exposes only `sourcePort`, `destinationPort`, `fileDescriptor`.
  There is no `contextID`/`guestCID` property anywhere on the public
  surface (verified against the current macOS SDK headers, copyright
  through 2025, and against `github.com/Code-Hex/vz/v3@v3.7.1` which
  faithfully mirrors that surface). Consequence : `weft`'s
  `GuestPodPlane.Attach` strict-when-known peer check stays in its
  permissive-fallback mode for VZ-backed microVMs ; QEMU-backed
  microVMs remain fully protected because their CID is host-allocated
  (`weft-driver-qemu` v0.5.0+ binds `-device vhost-vsock-pci,guest-cid=N`).
  If a future macOS release adds a CID accessor, revisit this :
  read it post-`Start()`, persist to `<vmDir>/vsock_actual_cid`, and
  have the agent call `Adapter.RegisterPodCID` from the StartVM
  dispatch site.

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
