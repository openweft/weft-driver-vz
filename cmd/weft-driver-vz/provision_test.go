//go:build darwin

package main

// provision_test.go exercises the `provision` subcommand (newProvisionCommand)
// plus its placeArtefact helper. Moved here verbatim from weft when the VZ
// datapath was externalised into this plugin. The happy path delegates
// NVRAM/machine-id/mac creation to the Apple-VZ driver bundle, which works
// headless (no entitlement needed just to materialise those blobs).

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPlaceArtefact_SymlinkMode(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src")
	dst := filepath.Join(tmp, "dst")
	if err := os.WriteFile(src, []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := placeArtefact(src, dst, false); err != nil {
		t.Fatalf("placeArtefact symlink: %v", err)
	}
	link, err := os.Readlink(dst)
	if err != nil {
		t.Fatalf("readlink: %v", err)
	}
	if link == "" {
		t.Errorf("symlink target empty")
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read through symlink: %v", err)
	}
	if string(got) != "payload" {
		t.Errorf("contents = %q", got)
	}
}

func TestPlaceArtefact_CopyMode(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src")
	dst := filepath.Join(tmp, "dst")
	if err := os.WriteFile(src, []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := placeArtefact(src, dst, true); err != nil {
		t.Fatalf("placeArtefact copy: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read dst: %v", err)
	}
	if string(got) != "payload" {
		t.Errorf("contents = %q", got)
	}
	info, err := os.Lstat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Errorf("copy mode should not produce a symlink")
	}
}

func TestPlaceArtefact_OverwritesExisting(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src")
	dst := filepath.Join(tmp, "dst")
	_ = os.WriteFile(src, []byte("new"), 0o600)
	_ = os.WriteFile(dst, []byte("old"), 0o600)
	if err := placeArtefact(src, dst, true); err != nil {
		t.Fatalf("overwrite: %v", err)
	}
	got, _ := os.ReadFile(dst)
	if string(got) != "new" {
		t.Errorf("dst = %q, want new", got)
	}
}

func TestPlaceArtefact_MissingSource(t *testing.T) {
	tmp := t.TempDir()
	if err := placeArtefact("/var/empty/missing", filepath.Join(tmp, "dst"), false); err == nil {
		t.Errorf("missing source should error")
	}
}

// TestPlaceArtefact_CopyDstIsDir covers the dst-open-error branch: pointing
// dst at a non-empty directory makes os.Remove + the copy fail. Moved here
// from weft's fill_test.go with placeArtefact.
func TestPlaceArtefact_CopyDstIsDir(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src")
	_ = os.WriteFile(src, []byte("x"), 0o600)
	dstDir := filepath.Join(tmp, "dstdir")
	_ = os.MkdirAll(dstDir, 0o700)
	_ = os.WriteFile(filepath.Join(dstDir, "child"), []byte("y"), 0o600)
	if err := placeArtefact(src, dstDir, true); err == nil {
		t.Errorf("copy onto a non-empty directory should error")
	}
}

func TestProvisionCommand_RequiresFlags(t *testing.T) {
	cmd := newProvisionCommand()
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err == nil {
		t.Errorf("expected error when --vmdir + --image are missing")
	}
}

func TestProvisionCommand_FlagSurface(t *testing.T) {
	cmd := newProvisionCommand()
	want := []string{"vmdir", "image", "data-disk", "cidata", "cpu", "mem-gib", "copy"}
	for _, name := range want {
		if cmd.Flag(name) == nil {
			t.Errorf("missing flag %q", name)
		}
	}
}

func TestProvisionCommand_HappyPath(t *testing.T) {
	tmp := t.TempDir()
	img := filepath.Join(tmp, "disk.raw")
	dataDisk := filepath.Join(tmp, "data.raw")
	cidata := filepath.Join(tmp, "cidata.iso")
	for _, f := range []string{img, dataDisk, cidata} {
		if err := os.WriteFile(f, make([]byte, 512), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	vmDir := filepath.Join(tmp, "vm")

	cmd := newProvisionCommand()
	cmd.SetArgs([]string{
		"--vmdir", vmDir,
		"--image", img,
		"--data-disk", dataDisk,
		"--cidata", cidata,
		"--cpu", "4",
		"--mem-gib", "8",
		"--copy",
	})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	for _, want := range []string{"disk.img", "data-0.img", "cloud-init.iso", "config.json"} {
		if _, err := os.Stat(filepath.Join(vmDir, want)); err != nil {
			t.Errorf("missing %s: %v", want, err)
		}
	}
	cfg, err := os.ReadFile(filepath.Join(vmDir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(cfg), "data-0.img") {
		t.Errorf("config.json should list the data disk: %s", cfg)
	}
}

func TestProvisionCommand_MissingImageSource(t *testing.T) {
	tmp := t.TempDir()
	cmd := newProvisionCommand()
	cmd.SetArgs([]string{
		"--vmdir", filepath.Join(tmp, "vm"),
		"--image", "/var/empty/does-not-exist",
	})
	if err := cmd.Execute(); err == nil {
		t.Errorf("missing image source should error")
	}
}
