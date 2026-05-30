//go:build darwin

package builtin

// bundle.go assembles the four driver instances a weft-agent
// running on an Apple VZ host needs. One bundle per agent
// process: the four drivers share the same HostInfo so the
// control plane consistently sees them as "the host's services".
//
// The Bundle type is what weft-agent will hand to its dispatch
// layer (registers each driver with the right type so
// `EnsureNetwork` calls the right thing, etc.). For now nobody
// consumes the Bundle yet — but defining it makes the wiring
// shape explicit before weft-agent exists.

import (
	"path/filepath"
)

// Bundle holds the four driver instances backing one Apple VZ
// host. Field names match the driver type names so the agent's
// dispatch table can use struct-field reflection if it wants.
type Bundle struct {
	Hypervisor *Hypervisor
	Network    *Network
	Volume     *Volume
	Image      *Image
}

// BundleOptions wraps the construction inputs for all four
// drivers in one struct. StateDir is the on-host root for
// per-volume and per-image-cache directories (defaults align
// with weft's existing layout: <stateDir>/volumes, <stateDir>/cache).
type BundleOptions struct {
	Options
	StateDir string
}

// New returns the driver bundle for one Apple VZ host. All four
// drivers receive the same HostInfo + a derived per-driver
// state-directory.
func New(o BundleOptions) *Bundle {
	stateDir := o.StateDir
	if stateDir == "" {
		stateDir = ".weft-agent" // sensible single-host dev default
	}
	return &Bundle{
		Hypervisor: NewHypervisor(o.Options),
		Network:    NewNetwork(o.Options),
		Volume: NewVolume(VolumeOptions{
			Options:  o.Options,
			StateDir: filepath.Join(stateDir, "volumes"),
		}),
		Image: NewImage(ImageOptions{
			Options:  o.Options,
			CacheDir: filepath.Join(stateDir, "cache"),
		}),
	}
}
