//go:build darwin

package builtin

// image.go is the scaffold for the host-local ImageDriver. It
// will eventually wrap the existing imagestore package from
// weft/imagestore/ — but that wrapping waits until imagestore is
// extracted into its own module so this driver can depend on it
// without pulling in the entire weft control-plane module.
//
// Today: stubs that return ErrUnsupported. The migration order:
//
//   1. Extract pkg/openweft/weft/imagestore/ → its own go.mod
//      (separate module, importable by anyone, no weft deps).
//   2. Add `require github.com/openweft/imagestore` here.
//   3. Replace the stub bodies with imagestore calls.

import (
	"context"

	drivers "github.com/openweft/weft-drivers"
)

type Image struct {
	opts Options
	// cacheDir is the root the OCI cache lives under. Resolved
	// by the constructor (typically <stateDir>/cache).
	cacheDir string
}

type ImageOptions struct {
	Options
	CacheDir string
}

func NewImage(o ImageOptions) *Image {
	return &Image{opts: o.Options, cacheDir: o.CacheDir}
}

func (i *Image) HostInfo(ctx context.Context) (drivers.HostInfo, error) {
	return hostInfoFor(i.opts), nil
}

func (i *Image) Pull(ctx context.Context, ref string) error {
	return drivers.ErrUnsupported
}

func (i *Image) LocalPath(ctx context.Context, ref string) (string, error) {
	return "", drivers.ErrUnsupported
}

func (i *Image) Delete(ctx context.Context, ref string) error {
	return drivers.ErrUnsupported
}

func (i *Image) InCache(ctx context.Context, ref string) (bool, error) {
	return false, drivers.ErrUnsupported
}

var _ drivers.ImageDriver = (*Image)(nil)
