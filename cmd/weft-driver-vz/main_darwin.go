//go:build darwin

// Command weft-driver-vz is the Apple-VZ hypervisor driver as an external
// weft go-plugin. It is the cgo carrier: it links Virtualization.framework
// (and the AppKit VM window), so the weft core no longer has to. Code-signed
// with the com.apple.security.virtualization entitlement — weft itself isn't.
//
// Modes (distinguished by argv):
//
//	weft-driver-vz                       → go-plugin serve mode (no args)
//	weft-driver-vz vz-vm-run --vmdir ...  → forked per VM; owns the AppKit window
//	weft-driver-vz provision --vmdir ...  → lay out a vmDir from prebuilt artefacts
package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/spf13/cobra"

	weftplugin "github.com/openweft/weft-driver-plugin"
	builtin "github.com/openweft/weft-driver-vz/builtin"
	weftslognats "github.com/openweft/weft-slognats"
)

func main() {
	// The vz-vm-run subcommand opens an AppKit window via
	// StartGraphicApplication, which must run on the process main thread.
	// Lock it before cobra touches any flags.
	if len(os.Args) > 1 && os.Args[1] == "vz-vm-run" {
		runtime.LockOSThread()
	}

	hostUUID := os.Getenv(weftplugin.EnvHostUUID)
	log, logCloser := weftslognats.SetupFromEnv("weft.driver.vz." + hostUUID + ".log")
	defer logCloser.Close()
	slog.SetDefault(log)

	root := &cobra.Command{
		Use:           "weft-driver-vz",
		Short:         "weft Apple-VZ driver plugin (cgo). Run with no arguments to serve over go-plugin.",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(*cobra.Command, []string) error {
			serve(hostUUID) // blocks until the host disconnects
			return nil
		},
	}
	root.AddCommand(builtin.NewRunVMCommand(recordTimingEvent))
	root.AddCommand(newProvisionCommand())

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "weft-driver-vz:", err)
		os.Exit(1)
	}
}

// serve builds the Apple-VZ driver bundle for this host (config from env, set
// by the launching weft process) and serves all four driver services.
func serve(hostUUID string) {
	opts := builtin.Options{
		HostUUID: hostUUID,
		Hostname: os.Getenv(weftplugin.EnvHostname),
		AZ:       os.Getenv(weftplugin.EnvAZ),
		// Fork this same binary as `vz-vm-run` for each VM — the window and
		// VZ object graph live in the child, exactly as when weft owned them.
		SpawnVMCommand: func(vmDir string) (*exec.Cmd, error) {
			exe, err := os.Executable()
			if err != nil {
				return nil, err
			}
			return exec.Command(exe, "vz-vm-run", "--vmdir="+vmDir, "--name="+filepath.Base(vmDir)), nil
		},
		OnVMExit: func(vmDir string) { recordTimingEvent(vmDir, "vz_vm_run.exited", nil) },
		OnEvent:  recordTimingEvent,
	}
	b := builtin.New(builtin.BundleOptions{Options: opts, StateDir: os.Getenv(weftplugin.EnvStateDir)})
	weftplugin.Serve(weftplugin.DriverSet{
		Hypervisor: b.Hypervisor,
		Network:    b.Network,
		Volume:     b.Volume,
		Image:      b.Image,
	})
}

// timingEvent mirrors weft.TimingEvent's JSON shape so events the plugin
// records land in the same <vmDir>/timings.jsonl stream weft reads.
type timingEvent struct {
	Name       string            `json:"name"`
	TsUnixNano int64             `json:"ts_unix_ns"`
	Meta       map[string]string `json:"meta,omitempty"`
}

// recordTimingEvent appends one event to <vmDir>/timings.jsonl, the same
// per-VM file + format weft.RecordEvent uses. The plugin shares the host
// filesystem and is handed weft's StateDir, so vmDir paths align and
// weft.ReadTimings picks these up — exactly as when vz-vm-run lived in weft.
// (The live in-process event bus stays weft-side; the durable log is the
// cross-process contract, as it always was.)
func recordTimingEvent(vmDir, kind string, meta map[string]string) {
	if vmDir == "" || kind == "" {
		return
	}
	ev := timingEvent{Name: kind, TsUnixNano: time.Now().UnixNano(), Meta: meta}
	b, err := json.Marshal(ev)
	if err != nil {
		return
	}
	b = append(b, '\n')
	f, err := os.OpenFile(filepath.Join(vmDir, "timings.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "weft-driver-vz: timings %s: %v\n", kind, err)
		return
	}
	_, _ = f.Write(b)
	_ = f.Close()
}
