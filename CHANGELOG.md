# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project aims to adhere to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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
