//go:build darwin && cgo

package builtin

// vmrun.go contains the logic for running a single VM with a native graphical
// window (VZVirtualMachineView). It is invoked as a hidden subcommand
// ("vz-vm-run") that is forked by the Hypervisor driver's StartVM so that:
//  1. Each VM window lives in its own process (like `tart run`).
//  2. The parent process can exit after provisioning without killing the VMs.
//  3. StartGraphicApplication runs on the main thread of the subprocess.
//
// Events:
//
// The subprocess emits fine-grained lifecycle events (`vz_vm_run.*`,
// `vz.state.*`, `guest.<stage>` from WEFT_MARK console lines) via the
// onEvent closure the caller passes into NewRunVMCommand. In weft-control
// the closure is wired to weft.RecordEvent so events land in the same
// `timings.jsonl` stream as control-plane events. nil disables the
// bridge — events still appear as plain stderr lines for ops visibility.
//
// Migrated from `pkg/openweft/weft/runvm.go` as part of the
// driver-extraction work — see [[weft-driver-registry-split]] and
// [[weft-one-repo-per-driver]] for the broader plan.

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	vz "github.com/Code-Hex/vz/v3"
	"github.com/spf13/cobra"
)

// EventFunc matches Options.OnEvent's signature — declared as its
// own type so the vz-vm-run subprocess can carry the closure
// without re-importing weft-drivers.
type EventFunc func(vmDir, kind string, meta map[string]string)

// NewRunVMCommand returns the hidden "vz-vm-run" cobra command
// forked by the Hypervisor driver's StartVM. The onEvent closure
// receives every lifecycle event the subprocess emits; pass nil
// to disable the bridge (stderr-only mode).
func NewRunVMCommand(onEvent EventFunc) *cobra.Command {
	cmd := &cobra.Command{
		Use:    "vz-vm-run",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			vmDir, _ := cmd.Flags().GetString("vmdir")
			name, _ := cmd.Flags().GetString("name")
			if vmDir == "" || name == "" {
				return fmt.Errorf("vz-vm-run: --vmdir and --name are required")
			}
			return RunVMGraphic(vmDir, name, onEvent)
		},
	}
	cmd.Flags().String("vmdir", "", "Path to the VM directory")
	cmd.Flags().String("name", "", "VM name (used as window title)")
	return cmd
}

// RunVMGraphic builds the VZ configuration for the VM at vmDir, starts it,
// then opens a native macOS graphical window via VZVirtualMachineView.
// This call blocks until the window is closed.
// The caller (main) must have already called runtime.LockOSThread on the
// main goroutine before cobra parses any flags.
func RunVMGraphic(vmDir, name string, onEvent EventFunc) error {
	record := func(kind string, meta map[string]string) {
		if onEvent != nil {
			onEvent(vmDir, kind, meta)
		}
	}
	// Ignore SIGHUP so this process survives when the parent (mock up) exits.
	signal.Ignore(syscall.SIGHUP)
	record("vz_vm_run.entered", nil)
	consolePath := filepath.Join(vmDir, "console.log")
	// Truncate console.log before each start so the tab shows only the current
	// boot sequence (not accumulated output from previous runs).
	_ = os.Truncate(consolePath, 0)

	// Detect microvm mode from config.json. weft-microvm's
	// `weft-microvm run` populates `"microvm": true` so the runner can pick the
	// headless code path: no virtio-gpu, no NSWindow, no input
	// devices, just kernel + virtio-fs + virtio-net + virtio-
	// console, and the subprocess blocks on the Stopped state
	// transition rather than running an AppKit event loop. Saves
	// ~30 ms of VZ instantiate time on top of the kernel cuts and
	// removes the "VM opened a window" surprise during scripted /
	// CI flows.
	micro := readMicroVMFlag(vmDir)
	withGraphics := !micro

	cfg, err := buildVZConfigFromDir(vmDir, consolePath, withGraphics)
	if err != nil {
		record("vz_vm_run.config_failed", map[string]string{"err": err.Error()})
		return fmt.Errorf("vz-run %s: build config: %w", name, err)
	}
	record("vz_vm_run.vm_configured", nil)
	machine, err := vz.NewVirtualMachine(cfg)
	if err != nil {
		record("vz_vm_run.new_vm_failed", map[string]string{"err": err.Error()})
		return fmt.Errorf("vz-run %s: new vm: %w", name, err)
	}
	record("vz_vm_run.vm_instantiated", nil)
	// Store for use by the Reboot button in the console tab (no-op
	// in microvm mode — kept for symmetry with the GUI path).
	gVMInstance = machine

	// stoppedCh fires when VZ transitions to Stopped. The headless
	// branch below blocks on it; the graphical branch ignores it
	// (the AppKit loop owns the lifecycle there).
	stoppedCh := make(chan struct{}, 1)

	// Log every state transition so we can diagnose crashes — and
	// fold each into the timings.jsonl. The VZ state machine is the
	// truth of "VM running" vs "still booting EFI" so these
	// transitions are the most useful host-side markers we have
	// without instrumenting the guest.
	stateCh := machine.StateChangedNotify()
	go func() {
		for s := range stateCh {
			fmt.Fprintf(os.Stderr, "vz-run %s: state → %s\n", name, s)
			record("vz.state."+fmt.Sprint(s), nil)
			if fmt.Sprint(s) == "VirtualMachineStateStopped" {
				select {
				case stoppedCh <- struct{}{}:
				default:
				}
			}
		}
	}()

	// Start a console-tail watcher: every line containing `WEFT_MARK
	// <stage>` (written by guest-side weft-microvm-init to /dev/console) is
	// folded into timings.jsonl as `guest.<stage>`. This is the
	// bridge that lets us measure end-to-end times — from
	// server-side `start_attempted` to guest-side `weft_exec_ready`
	// — in a single sorted timeline.
	go watchConsoleForMarks(vmDir, consolePath, record)

	if !micro {
		// Graphical path: register the console tab observer before
		// StartGraphicApplication launches NSApp so the
		// NSApplicationDidFinishLaunchingNotification is caught.
		registerConsoleTab(name, consolePath)
	}

	if err := machine.Start(); err != nil {
		record("vz_vm_run.start_failed", map[string]string{"err": err.Error()})
		return fmt.Errorf("vz-run %s: start: %w", name, err)
	}
	record("vz_vm_run.start_returned", nil)

	if micro {
		// Headless microvm: block until the VM stops. Same
		// SIGTERM/SIGINT-driven shutdown the GUI path benefits from
		// via NSApp, but driven by signal channels here. The
		// subprocess exits cleanly so the parent's wait goroutine
		// fires `server.vz_vm_run_exited` and the timings.jsonl
		// gets the closing bracket.
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
		select {
		case <-stoppedCh:
		case <-sigCh:
			fmt.Fprintf(os.Stderr, "vz-run %s: shutdown signal — requesting stop\n", name)
			_, _ = machine.RequestStop()
			<-stoppedCh
		}
		record("vz_vm_run.microvm_exited", nil)
		return nil
	}

	return machine.StartGraphicApplication(1280, 800,
		vz.WithWindowTitle("mock: "+name),
		vz.WithController(true),
	)
}

// readMicroVMFlag returns true when <vmDir>/config.json carries
// `"microvm": true`. Any read or decode failure returns false so
// classic VMs (no flag) keep the graphical path — additive only.
func readMicroVMFlag(vmDir string) bool {
	b, err := os.ReadFile(filepath.Join(vmDir, "config.json"))
	if err != nil {
		return false
	}
	var cfg struct {
		MicroVM bool `json:"microvm"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return false
	}
	return cfg.MicroVM
}

// watchConsoleForMarks tails consolePath and folds any
// `WEFT_MARK <stage>` line (with optional ` key=val ` pairs after)
// into the timings stream as a `guest.<stage>` event.
//
// macOS doesn't expose kqueue/inotify-like primitives via stdlib for
// "wait for more bytes" tailing without polling, so we poll the file
// at 20 ms — fast enough for sub-100 ms boot resolution, light enough
// to be free.
//
// The function returns when the console file disappears (post-shutdown
// cleanup) or when 30 s have passed without growth (guest is idle in
// userspace by then; no further boot marks expected).
func watchConsoleForMarks(vmDir, consolePath string, record func(string, map[string]string)) {
	const idleQuit = 30 * time.Second
	var off int64
	idleStart := time.Now()
	buf := make([]byte, 64*1024)
	pending := []byte{} // line carry-over across reads
	for {
		f, err := os.Open(consolePath)
		if err != nil {
			time.Sleep(50 * time.Millisecond)
			if time.Since(idleStart) > idleQuit {
				return
			}
			continue
		}
		_, _ = f.Seek(off, 0)
		n, _ := f.Read(buf)
		_ = f.Close()
		if n > 0 {
			idleStart = time.Now()
			off += int64(n)
			chunk := append(pending, buf[:n]...)
			for {
				i := indexNL(chunk)
				if i < 0 {
					pending = chunk
					break
				}
				line := chunk[:i]
				chunk = chunk[i+1:]
				if stage, meta, ok := parseNCLMark(line); ok {
					record("guest."+stage, meta)
				}
			}
		} else if time.Since(idleStart) > idleQuit {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// indexNL finds the first '\n' in b. Sub for bytes.IndexByte to keep
// this file's import surface tight (the rest of vmrun.go doesn't
// pull in "bytes" yet).
func indexNL(b []byte) int {
	for i, c := range b {
		if c == '\n' {
			return i
		}
	}
	return -1
}

// parseNCLMark recognises the guest-side instrumentation line shape:
//
//	WEFT_MARK <stage> [key=val ...]
//
// Lines that don't start with the prefix are ignored. The stage
// becomes the timings event suffix; any key=val pairs land in the
// event's Meta map (so the guest can stamp a monotonic-ns counter
// per stage, propagated unmodified into timings.jsonl).
func parseNCLMark(line []byte) (stage string, meta map[string]string, ok bool) {
	// Strip a trailing '\r' (DOS line endings on serial consoles).
	if n := len(line); n > 0 && line[n-1] == '\r' {
		line = line[:n-1]
	}
	const prefix = "WEFT_MARK "
	idx := indexSubstr(line, prefix)
	if idx < 0 {
		return "", nil, false
	}
	rest := string(line[idx+len(prefix):])
	parts := splitFields(rest)
	if len(parts) == 0 {
		return "", nil, false
	}
	stage = parts[0]
	if len(parts) > 1 {
		meta = make(map[string]string, len(parts)-1)
		for _, p := range parts[1:] {
			eq := indexByteStr(p, '=')
			if eq <= 0 {
				continue
			}
			meta[p[:eq]] = p[eq+1:]
		}
	}
	return stage, meta, true
}

// Tiny string helpers — same rationale as indexNL above. Keeps
// vmrun.go's import set untouched.
func indexSubstr(b []byte, s string) int {
	if len(s) == 0 || len(b) < len(s) {
		return -1
	}
	for i := 0; i+len(s) <= len(b); i++ {
		j := 0
		for ; j < len(s); j++ {
			if b[i+j] != s[j] {
				break
			}
		}
		if j == len(s) {
			return i
		}
	}
	return -1
}

func splitFields(s string) []string {
	var out []string
	start := -1
	for i, c := range s {
		if c == ' ' || c == '\t' {
			if start >= 0 {
				out = append(out, s[start:i])
				start = -1
			}
		} else if start < 0 {
			start = i
		}
	}
	if start >= 0 {
		out = append(out, s[start:])
	}
	return out
}

func indexByteStr(s string, c byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return i
		}
	}
	return -1
}

// openConsoleTerminal opens a Terminal.app window that tails consolePath with
// tail -F so the user sees serial console output (kernel boot messages).
// Failures are silently ignored — the VM will still run without it.
func openConsoleTerminal(name, consolePath string) {
	// Use a temp script file to avoid multi-line osascript quoting issues.
	script := fmt.Sprintf(
		"tell application \"Terminal\"\n"+
			"  do script \"printf '\\\\033]0;console: %s\\\\007'; tail -F '%s'\"\n"+
			"  activate\n"+
			"end tell\n",
		name, consolePath,
	)
	tmp, err := os.CreateTemp("", "mock-console-*.applescript")
	if err != nil {
		return
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(script); err != nil {
		tmp.Close()
		return
	}
	tmp.Close()
	exec.Command("osascript", tmp.Name()).Start() //nolint:errcheck
}

// buildVZConfigFromDir builds a VirtualMachineConfiguration from files stored
// in vmDir. When withGraphics is true a VirtIO GPU scanout (1920×1200) is
// added so VZVirtualMachineView has something to display.
// It is a package-level function (no adapter state needed) so it can be
// called both from the Adapter and from the standalone vz-vm-run subprocess.
func buildVZConfigFromDir(dir, consolePath string, withGraphics bool) (*vz.VirtualMachineConfiguration, error) {
	nvramPath := filepath.Join(dir, "nvram.bin")
	var efiOpts []vz.NewEFIVariableStoreOption
	if _, err := os.Stat(nvramPath); os.IsNotExist(err) {
		efiOpts = append(efiOpts, vz.WithCreatingEFIVariableStore())
	}

	// Read config.json once, up front. Other branches below
	// (LinuxBootLoader cmdline override, device-share attach,
	// data-disk attach, cpu/mem overrides) all consume fields from
	// this struct, so reading it here keeps the rest of the
	// function linear and avoids re-reading the file.
	var vmCfgJSON struct {
		DataDisks []struct {
			Name string `json:"name"`
		} `json:"data_disks"`
		CPU     uint   `json:"cpu"`
		MemGiB  uint64 `json:"mem_gib"`
		Cmdline string `json:"cmdline,omitempty"`
		Shares  []struct {
			Tag      string `json:"tag"`
			Path     string `json:"path"`
			ReadOnly bool   `json:"read_only,omitempty"`
		} `json:"shares,omitempty"`
	}
	if b, err := os.ReadFile(filepath.Join(dir, "config.json")); err == nil {
		_ = json.Unmarshal(b, &vmCfgJSON)
	}

	// Prefer direct Linux kernel boot if a host-side kernel file exists in
	// the VM directory (vmDir/kernel). This enables quick testing without
	// requiring an EFI System Partition and a GRUB EFI binary.
	var bootLoader vz.BootLoader
	kernelPath := filepath.Join(dir, "kernel")
	if _, err := os.Stat(kernelPath); err == nil {
		// Optional initrd in dir/initrd
		initrdPath := filepath.Join(dir, "initrd")
		// Build kernel command-line to ensure serial output and proper root
		// device (virtio block appears as /dev/vda). This helps capture the
		// guest serial console in console.log. Use the virtio hvc console so
		// the host virtio-console attachment is visible inside the guest as
		// hvc0.
		//
		// config.json's `cmdline` field overrides this default — needed for
		// microVMs (e.g. weft-microvm's `weft.rootfs=virtiofs:rootfs0`)
		// whose init has nothing to do with /dev/vda2 and which want their
		// own kernel knobs. The override is read EARLY (before the loop that
		// builds opts) so the rest of the LinuxBootLoader recipe stays
		// identical for both code paths.
		cmdLine := "console=hvc0 root=/dev/vda2 rw"
		if vmCfgJSON.Cmdline != "" {
			cmdLine = vmCfgJSON.Cmdline
		}
		opts := []vz.LinuxBootLoaderOption{
			vz.WithCommandLine(cmdLine),
		}
		if _, ie := os.Stat(initrdPath); ie == nil {
			opts = append(opts, vz.WithInitrd(initrdPath))
		}
		lb, lbErr := vz.NewLinuxBootLoader(kernelPath, opts...)
		if lbErr == nil {
			bootLoader = lb
		} else {
			// Fallback to EFI loader if LinuxBootLoader creation fails.
			efiStore, err := vz.NewEFIVariableStore(nvramPath, efiOpts...)
			if err != nil {
				return nil, fmt.Errorf("efi store: %w", err)
			}
			bl, err := vz.NewEFIBootLoader(vz.WithEFIVariableStore(efiStore))
			if err != nil {
				return nil, fmt.Errorf("efi boot loader: %w", err)
			}
			bootLoader = bl
		}
	} else {
		efiStore, err := vz.NewEFIVariableStore(nvramPath, efiOpts...)
		if err != nil {
			return nil, fmt.Errorf("efi store: %w", err)
		}
		bl, err := vz.NewEFIBootLoader(vz.WithEFIVariableStore(efiStore))
		if err != nil {
			return nil, fmt.Errorf("efi boot loader: %w", err)
		}
		bootLoader = bl
	}
	midPath := filepath.Join(dir, "machine-id.bin")
	midData, err := os.ReadFile(midPath)
	if err != nil {
		return nil, fmt.Errorf("read machine-id: %w", err)
	}
	mid, err := vz.NewGenericMachineIdentifierWithData(midData)
	if err != nil {
		return nil, fmt.Errorf("machine identifier: %w", err)
	}
	platform, err := vz.NewGenericPlatformConfiguration(vz.WithGenericMachineIdentifier(mid))
	if err != nil {
		return nil, fmt.Errorf("platform config: %w", err)
	}
	// Primary storage: classic VMs have a writable `disk.img` in
	// the VM dir; microVMs (registered by weft-microvm's
	// `weft-microvm run` flow) instead carry a read-only `boot.iso` that
	// holds the weft-microvm-init UKI. The runtime config tells the two
	// apart by which file is present — kept additive so existing
	// CloneVM-created VMs are byte-for-byte unaffected.
	diskPath := filepath.Join(dir, "disk.img")
	bootISOPath := filepath.Join(dir, "boot.iso")
	if ap, aerr := filepath.Abs(diskPath); aerr == nil {
		diskPath = ap
	}
	if ap, aerr := filepath.Abs(bootISOPath); aerr == nil {
		bootISOPath = ap
	}
	var (
		primaryPath string
		primaryRO   bool
	)
	if fi, stErr := os.Stat(diskPath); stErr == nil {
		primaryPath = diskPath
		primaryRO = false
		fmt.Fprintf(os.Stderr, "vz: attaching disk %s (size=%d)\n", diskPath, fi.Size())
	} else if fi, isoErr := os.Stat(bootISOPath); isoErr == nil {
		primaryPath = bootISOPath
		primaryRO = true
		fmt.Fprintf(os.Stderr, "vz: attaching boot.iso %s read-only (size=%d)\n", bootISOPath, fi.Size())
	}
	// Direct-Linux microVM mode tolerates having neither disk.img
	// nor boot.iso — the rootfs comes from a virtio-fs share, not a
	// block device. We just leave storageDevices empty and rely on
	// LinuxBootLoader + Shares to give the guest everything it needs.
	// Classic VMs always have disk.img and hit one of the branches
	// above, so this is byte-for-byte transparent for them.
	var storageDevices []vz.StorageDeviceConfiguration
	if primaryPath != "" {
		diskAtt, err := vz.NewDiskImageStorageDeviceAttachment(primaryPath, primaryRO)
		if err != nil {
			return nil, fmt.Errorf("primary attachment %s: %w", primaryPath, err)
		}
		diskDev, err := vz.NewVirtioBlockDeviceConfiguration(diskAtt)
		if err != nil {
			return nil, fmt.Errorf("block device config: %w", err)
		}
		storageDevices = []vz.StorageDeviceConfiguration{diskDev}
	} else {
		fmt.Fprintf(os.Stderr, "vz: no primary disk — running in direct-Linux microVM mode (rootfs via virtio-fs share)\n")
	}

	// Attach extra data disks listed in config.json. vmCfgJSON was
	// already populated at the top of the function (so the bootloader
	// branch above can read .Cmdline); we just consume it here.
	//
	// vmCfgJSON.Shares are the additive piece weft-microvm's
	// `weft-microvm run` uses: an OCI image rootfs is exposed to the microVM
	// as a virtio-fs share with a known mount tag (typically
	// "rootfs0"), and the guest's weft-microvm-init mounts it on /newroot
	// before pivot_root + exec. Classic VMs leave Shares empty — the
	// device-list stays unchanged for them.
	for _, o := range vmCfgJSON.DataDisks {
		ddPath := filepath.Join(dir, o.Name)
		ddAtt, err := vz.NewDiskImageStorageDeviceAttachment(ddPath, false)
		if err != nil {
			return nil, fmt.Errorf("data disk attachment %s: %w", o.Name, err)
		}
		ddDev, err := vz.NewVirtioBlockDeviceConfiguration(ddAtt)
		if err != nil {
			return nil, fmt.Errorf("data disk config %s: %w", o.Name, err)
		}
		storageDevices = append(storageDevices, ddDev)
	}

	// Attach the cloud-init seed ISO (NoCloud datasource) if present in the
	// VM directory. It is written there by the up command after CloneVM.
	isoPath := filepath.Join(dir, "cloud-init.iso")
	if _, statErr := os.Stat(isoPath); statErr == nil {
		isoAtt, isoErr := vz.NewDiskImageStorageDeviceAttachment(isoPath, true) // read-only
		if isoErr != nil {
			return nil, fmt.Errorf("cloud-init iso attachment: %w", isoErr)
		}
		isoDev, isoErr := vz.NewVirtioBlockDeviceConfiguration(isoAtt)
		if isoErr != nil {
			return nil, fmt.Errorf("cloud-init iso device config: %w", isoErr)
		}
		storageDevices = append(storageDevices, isoDev)
	}

	natAtt, err := vz.NewNATNetworkDeviceAttachment()
	if err != nil {
		return nil, fmt.Errorf("nat attachment: %w", err)
	}
	netDev, err := vz.NewVirtioNetworkDeviceConfiguration(natAtt)
	if err != nil {
		return nil, fmt.Errorf("net device config: %w", err)
	}
	macStr, err := os.ReadFile(filepath.Join(dir, "mac.txt"))
	if err == nil {
		hw, parseErr := net.ParseMAC(strings.TrimSpace(string(macStr)))
		if parseErr == nil {
			if macAddr, macErr := vz.NewMACAddress(hw); macErr == nil {
				netDev.SetMACAddress(macAddr)
			}
		}
	}

	cpuCount := uint(2)
	memBytes := uint64(2 * 1024 * 1024 * 1024)
	if vmCfgJSON.CPU > 0 {
		cpuCount = vmCfgJSON.CPU
	}
	if vmCfgJSON.MemGiB > 0 {
		memBytes = vmCfgJSON.MemGiB * 1024 * 1024 * 1024
	}
	cfg, err := vz.NewVirtualMachineConfiguration(bootLoader, cpuCount, memBytes)
	if err != nil {
		return nil, fmt.Errorf("vm config: %w", err)
	}
	cfg.SetPlatformVirtualMachineConfiguration(platform)
	cfg.SetStorageDevicesVirtualMachineConfiguration(storageDevices)
	cfg.SetNetworkDevicesVirtualMachineConfiguration([]*vz.VirtioNetworkDeviceConfiguration{netDev})

	// Entropy device: required by Linux for kernel RNG (/dev/random) initialisation.
	if entropyDev, entropyErr := vz.NewVirtioEntropyDeviceConfiguration(); entropyErr == nil {
		cfg.SetEntropyDevicesVirtualMachineConfiguration([]*vz.VirtioEntropyDeviceConfiguration{entropyDev})
	}

	// VirtioSocketDeviceConfiguration : the AF_VSOCK transport every
	// microVM uses to reach the host's GuestPodPlane. Apple VZ does
	// NOT let userland pick the guest CID — the framework picks one
	// at boot and exposes it later via the VZVirtioSocketDevice's
	// listenerForPort / connectToPort APIs. So weft's allocator-derived
	// VsockCID is advisory on this backend : strict-when-known will
	// fall through to the permissive guard for VZ-backed VMs until
	// the agent reads the runtime CID back and updates the registry.
	// The device must still be attached or the guest can't dial at all.
	if vsockDev, vsockErr := vz.NewVirtioSocketDeviceConfiguration(); vsockErr == nil {
		cfg.SetSocketDevicesVirtualMachineConfiguration([]vz.SocketDeviceConfiguration{vsockDev})
		fmt.Fprintf(os.Stderr, "vz: attached virtio-vsock device (CID picked by VZ at boot)\n")
	} else {
		// A missing vsock device blocks the guest GuestPodPlane
		// client from working, but doesn't break legacy VM boots ;
		// log and continue rather than failing the whole VM start.
		fmt.Fprintf(os.Stderr, "vz: warn: VirtioSocketDevice unavailable : %v\n", vsockErr)
	}

	// virtio-fs directory shares — populated by weft-microvm (and any future
	// microVM-style consumer) via config.json's `shares` field. The
	// guest mounts each share by tag (e.g. `mount -t virtiofs rootfs0
	// /newroot`). VZ requires macOS 13+ for the underlying API; on
	// older hosts vz.NewVirtioFileSystemDeviceConfiguration returns
	// an error and we surface it rather than silently dropping the
	// share.
	if len(vmCfgJSON.Shares) > 0 {
		var fsDevs []vz.DirectorySharingDeviceConfiguration
		for _, s := range vmCfgJSON.Shares {
			if s.Tag == "" || s.Path == "" {
				return nil, fmt.Errorf("share entry needs both `tag` and `path` (got tag=%q path=%q)", s.Tag, s.Path)
			}
			sharedDir, sdErr := vz.NewSharedDirectory(s.Path, s.ReadOnly)
			if sdErr != nil {
				return nil, fmt.Errorf("shared directory %s: %w", s.Path, sdErr)
			}
			share, shErr := vz.NewSingleDirectoryShare(sharedDir)
			if shErr != nil {
				return nil, fmt.Errorf("single directory share %s: %w", s.Path, shErr)
			}
			fsDev, fsErr := vz.NewVirtioFileSystemDeviceConfiguration(s.Tag)
			if fsErr != nil {
				return nil, fmt.Errorf("virtio-fs device (tag=%s): %w", s.Tag, fsErr)
			}
			fsDev.SetDirectoryShare(share)
			fsDevs = append(fsDevs, fsDev)
			fmt.Fprintf(os.Stderr, "vz: attaching virtio-fs tag=%s path=%s ro=%v\n", s.Tag, s.Path, s.ReadOnly)
		}
		cfg.SetDirectorySharingDevicesVirtualMachineConfiguration(fsDevs)
	}

	// Serial console: guest boot output → consolePath (append mode so the full
	// boot sequence is preserved, not overwritten by the login prompt).
	if serialAtt, serialErr := vz.NewFileSerialPortAttachment(consolePath, true); serialErr == nil {
		if serialCfg, serialErr := vz.NewVirtioConsoleDeviceSerialPortConfiguration(serialAtt); serialErr == nil {
			cfg.SetSerialPortsVirtualMachineConfiguration([]*vz.VirtioConsoleDeviceSerialPortConfiguration{serialCfg})
		}
	}

	// VirtIO GPU: required for VZVirtualMachineView to have a framebuffer.
	// Use 1280×800 as the initial resolution — matches most laptop displays
	// and ensures the EFI/GRUB framebuffer fits on screen without scaling.
	if withGraphics {
		if gpuDev, gpuErr := vz.NewVirtioGraphicsDeviceConfiguration(); gpuErr == nil {
			if scanout, scanoutErr := vz.NewVirtioGraphicsScanoutConfiguration(1280, 800); scanoutErr == nil {
				gpuDev.SetScanouts(scanout)
				cfg.SetGraphicsDevicesVirtualMachineConfiguration([]vz.GraphicsDeviceConfiguration{gpuDev})
			}
		}
		// USB keyboard + pointing device: required for interactive use and for
		// some Linux kernels to properly initialise the VirtIO GPU display.
		if kbd, kbdErr := vz.NewUSBKeyboardConfiguration(); kbdErr == nil {
			cfg.SetKeyboardsVirtualMachineConfiguration([]vz.KeyboardConfiguration{kbd})
		}
		if ptr, ptrErr := vz.NewUSBScreenCoordinatePointingDeviceConfiguration(); ptrErr == nil {
			cfg.SetPointingDevicesVirtualMachineConfiguration([]vz.PointingDeviceConfiguration{ptr})
		}
	}

	valid, err := cfg.Validate()
	if err != nil || !valid {
		return nil, fmt.Errorf("vm config invalid: valid=%v err=%v", valid, err)
	}
	return cfg, nil
}
