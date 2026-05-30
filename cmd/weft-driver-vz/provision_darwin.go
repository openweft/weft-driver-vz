//go:build darwin

package main

// provision_darwin.go is the `weft-driver-vz provision` subcommand: lay out a
// fresh vmDir from prebuilt artefacts (boot disk + data disks + cloud-init
// ISO), then materialise nvram/machine-id/mac via the VZ Hypervisor driver's
// CreateVM. Moved here verbatim from weft's darwin-only provision.go as part
// of externalising the cgo datapath — weft no longer links the VZ builtin.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"golang.org/x/sys/unix"

	builtin "github.com/openweft/weft-driver-vz/builtin"
	drivers "github.com/openweft/weft-drivers"
)

func newProvisionCommand() *cobra.Command {
	var (
		vmDir     string
		image     string
		dataDisks []string
		cidata    string
		cpu       uint
		memGiB    uint64
		copyMode  bool
	)
	cmd := &cobra.Command{
		Use:   "provision",
		Short: "Lay out a vmDir from pre-built artefacts (no OCI / no clone).",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if vmDir == "" || image == "" {
				return fmt.Errorf("provision: --vmdir and --image are required")
			}
			if err := os.MkdirAll(vmDir, 0o755); err != nil {
				return fmt.Errorf("mkdir %s: %w", vmDir, err)
			}
			if err := placeArtefact(image, filepath.Join(vmDir, "disk.img"), copyMode); err != nil {
				return fmt.Errorf("place disk.img: %w", err)
			}
			type dataDiskEntry struct {
				Name string `json:"name"`
			}
			disks := make([]dataDiskEntry, 0, len(dataDisks))
			for i, src := range dataDisks {
				name := fmt.Sprintf("data-%d.img", i)
				if err := placeArtefact(src, filepath.Join(vmDir, name), copyMode); err != nil {
					return fmt.Errorf("place %s: %w", name, err)
				}
				disks = append(disks, dataDiskEntry{Name: name})
			}
			if cidata != "" {
				if err := placeArtefact(cidata, filepath.Join(vmDir, "cloud-init.iso"), copyMode); err != nil {
					return fmt.Errorf("place cloud-init.iso: %w", err)
				}
			}
			// Wipe stale EFI state then delegate to the Hypervisor driver's
			// CreateVM, which materialises nvram.bin + machine-id.bin + mac.txt
			// with the same vz.* primitives weft uses for every other VM.
			_ = os.Remove(filepath.Join(vmDir, "nvram.bin"))
			_ = os.Remove(filepath.Join(vmDir, "machine-id.bin"))
			_ = os.Remove(filepath.Join(vmDir, "mac.txt"))
			bundle := builtin.New(builtin.BundleOptions{
				Options:  builtin.Options{HostUUID: "provision-cli"},
				StateDir: vmDir,
			})
			if err := bundle.Hypervisor.CreateVM(context.Background(), drivers.VMSpec{UUID: vmDir}); err != nil {
				return fmt.Errorf("create vm state: %w", err)
			}
			cfg := map[string]any{
				"image":      filepath.Base(image),
				"data_disks": disks,
				"cpu":        cpu,
				"mem_gib":    memGiB,
			}
			b, _ := json.MarshalIndent(cfg, "", "  ")
			if err := os.WriteFile(filepath.Join(vmDir, "config.json"), b, 0o600); err != nil {
				return fmt.Errorf("write config.json: %w", err)
			}
			fmt.Printf("provision: ready: %s\n", vmDir)
			fmt.Printf("  next: %s vz-vm-run --vmdir %s --name cloud-boot\n", os.Args[0], vmDir)
			return nil
		},
	}
	cmd.Flags().StringVar(&vmDir, "vmdir", "", "Output VM directory (created if absent)")
	cmd.Flags().StringVar(&image, "image", "", "Boot disk (ISO or raw/qcow2) — placed at <vmdir>/disk.img")
	cmd.Flags().StringArrayVar(&dataDisks, "data-disk", nil, "Additional virtio-blk data disk (may be repeated); placed as data-N.img")
	cmd.Flags().StringVar(&cidata, "cidata", "", "cloud-init NoCloud ISO (optional); placed at <vmdir>/cloud-init.iso")
	cmd.Flags().UintVar(&cpu, "cpu", 2, "CPU count")
	cmd.Flags().Uint64Var(&memGiB, "mem-gib", 2, "Memory size in GiB")
	cmd.Flags().BoolVar(&copyMode, "copy", false, "Copy artefacts into vmDir instead of symlinking")
	return cmd
}

// placeArtefact stages src at dst: a relative-resolved symlink by default, or
// (in copy mode) a clonefile(2) CoW copy with a byte-copy fallback on
// ENOTSUP/EXDEV.
func placeArtefact(src, dst string, copyMode bool) error {
	abs, err := filepath.Abs(src)
	if err != nil {
		return err
	}
	if _, err := os.Stat(abs); err != nil {
		return fmt.Errorf("source %s: %w", abs, err)
	}
	_ = os.Remove(dst)
	if !copyMode {
		return os.Symlink(abs, dst)
	}
	if err := unix.Clonefile(abs, dst, 0); err == nil {
		return nil
	} else if !errors.Is(err, unix.ENOTSUP) && !errors.Is(err, unix.EXDEV) {
		return fmt.Errorf("clonefile %s → %s: %w", abs, dst, err)
	}
	in, err := os.Open(abs)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}
