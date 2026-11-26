//go:build windows

/*
Copyright 2026 Luther Monson.

Licensed under the MIT License. See LICENSE in the project root for details.
*/

// This file is the native WMI-backed Client implementation. It talks directly
// to the Hyper-V management objects in the root\virtualization\v2 namespace
// (the Msvm_* classes) through github.com/microsoft/wmi, so the agent does not
// spawn a powershell.exe per operation. It is Windows-only: the microsoft/wmi
// package pulls in go-ole and will not build on Linux, so the Linux controller
// build never compiles this file. The PowerShell client (powershell.go, which
// builds on every OS) remains the fallback backend.
//
// Two operations deliberately stay on PowerShell, each marked with a TODO
// below, because doing them cleanly in raw WMI in this pass is not worth
// shipping half-working:
//   - SetFirmware's boot-order step (Gen2 BootSourceOrder is an array of object
//     references that must be reordered; Secure Boot, the Linux-critical part,
//     is done in WMI here).
//   - GetIPAddresses' host-side neighbor fallback (Get-NetNeighbor lives in the
//     root\StandardCimv2 namespace; the KVP path is done in WMI here).
// Both reuse the exact, already-tested PowerShell builders from powershell.go.

package hyperv

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"

	ole "github.com/go-ole/go-ole"

	"github.com/microsoft/wmi/pkg/base/host"
	_ "github.com/microsoft/wmi/pkg/base/session" // registers the local WMI session manager (COM init)
	wmierrors "github.com/microsoft/wmi/pkg/errors"
	"github.com/microsoft/wmi/pkg/virtualization/core/memory"
	"github.com/microsoft/wmi/pkg/virtualization/core/processor"
	"github.com/microsoft/wmi/pkg/virtualization/core/service"
	"github.com/microsoft/wmi/pkg/virtualization/core/virtualsystem"
	"github.com/microsoft/wmi/pkg/virtualization/network/virtualswitch"

	"github.com/klotho-k8s/cluster-api-provider-hyperv/pkg/cloudinit"
)

// microsoftUEFICATemplateID is the GUID of Hyper-V's "Microsoft UEFI
// Certificate Authority" Secure Boot template, the one under which most Linux
// cloud images boot with Secure Boot enabled. It mirrors the PowerShell
// client's -SecureBootTemplate 'MicrosoftUEFICertificateAuthority'.
const microsoftUEFICATemplateID = "272e7447-90a4-4563-a4b9-8e4ab00526ce"

// adapterName is the name given to the single synthetic network adapter that
// CreateVM adds and connects to the requested virtual switch. It is also the
// adapter SetVLAN tags. "Network Adapter" matches the name New-VM assigns, so a
// VM created by either backend looks the same.
const adapterName = "Network Adapter"

// WMIClient is the native Client implementation backed by the Hyper-V WMI
// provider (root\virtualization\v2). It is selected by the agent in preference
// to the PowerShell client; see cmd/hyperv-agent backend selection.
type WMIClient struct {
	host *host.WmiHost
	// isoDir is where AttachCloudInit writes generated cidata ISOs before
	// attaching them, on the machine that runs this client.
	isoDir string
	// vmsDir is where CreateVM writes each VM's own boot disk, copied from the
	// base image so the base is never attached or mutated.
	vmsDir string
	// ps runs the two operations that remain on PowerShell (see the file
	// comment): SetFirmware's boot order and GetIPAddresses' neighbor fallback.
	ps commandRunner
}

var _ Client = (*WMIClient)(nil)

// NewWMIClient returns a Client that drives Hyper-V through WMI. isoDir is where
// generated cloud-init cidata ISOs are written; vmsDir is where each VM's own
// boot disk (a copy of the base image) is written. Both are created on demand.
func NewWMIClient(isoDir, vmsDir string) *WMIClient {
	return &WMIClient{
		host:   host.NewWmiLocalHost(),
		isoDir: isoDir,
		vmsDir: vmsDir,
		ps:     execRunner{},
	}
}

// vmDiskPath is the path of a VM's own boot disk, the per-VM copy of the base
// image under vmsDir. It matches the PowerShell client's layout so either
// backend can clean up after the other.
func (c *WMIClient) vmDiskPath(name string) string {
	return filepath.Join(c.vmsDir, name+".vhdx")
}

// isoPathFor is the path of a VM's cidata ISO under isoDir.
func (c *WMIClient) isoPathFor(name string) string {
	return filepath.Join(c.isoDir, name+"-cidata.iso")
}

// withCOM runs f on a dedicated, COM-initialized OS thread. WMI calls go through
// COM, which is apartment-affine, so the goroutine is pinned to its thread for
// the duration and the thread is initialized into the (process-wide)
// multithreaded apartment first. The microsoft/wmi session manager already put
// the process in an MTA at init, so a second CoInitializeEx here returns S_FALSE
// rather than changing the mode; either way the apartment is correct, so any
// error is ignored and balanced by CoUninitialize.
func withCOM(f func() error) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	_ = ole.CoInitializeEx(0, ole.COINIT_MULTITHREADED)
	defer ole.CoUninitialize()
	return f()
}

// copyFile copies src to dst, creating dst's parent directory. It is the WMI
// backend's equivalent of the PowerShell client's copyBaseDiskScript: CreateVM
// runs it before DefineSystem so the shared base image is never attached or
// booted; the VM only ever touches its own copy. An existing dst is truncated,
// making create retry-safe.
func copyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// stateFromWMI maps a Hyper-V Msvm_ComputerSystem EnabledState to the normalized
// power-state strings the Client interface reports (the same values the
// PowerShell client returns).
func stateFromWMI(s virtualsystem.VirtualMachineState) string {
	switch s {
	case virtualsystem.Running, virtualsystem.RunningCritical:
		return StateRunning
	case virtualsystem.Off, virtualsystem.OffCritical:
		return StateOff
	case virtualsystem.Starting, virtualsystem.StartingCritical:
		return StateStarting
	case virtualsystem.Stopping, virtualsystem.StoppingCritial:
		return StateStopping
	case virtualsystem.Paused, virtualsystem.PausedCritical:
		return StatePaused
	case virtualsystem.Saved, virtualsystem.SavedCritical:
		return StateSaved
	default:
		return fmt.Sprintf("%d", int32(s))
	}
}

// getService returns the Hyper-V virtual-system management service. Callers must
// already be inside withCOM.
func (c *WMIClient) getService() (*service.VirtualSystemManagementService, error) {
	vmms, err := service.GetVirtualSystemManagementService(c.host)
	if err != nil {
		return nil, fmt.Errorf("hyperv/wmi: management service: %w", err)
	}
	return vmms, nil
}

// lookupVM finds a VM by name, translating the library's not-found error into
// ErrVMNotFound. Callers must already be inside withCOM. The returned VM must be
// Closed by the caller.
func (c *WMIClient) lookupVM(vmms *service.VirtualSystemManagementService, name string) (*virtualsystem.VirtualMachine, error) {
	vm, err := vmms.GetVirtualMachineByName(name)
	if err != nil {
		if wmierrors.IsNotFound(err) {
			return nil, ErrVMNotFound
		}
		return nil, err
	}
	return vm, nil
}

// Exists reports whether a VM with the given name exists.
func (c *WMIClient) Exists(ctx context.Context, name string) (bool, error) {
	found := false
	err := withCOM(func() error {
		vmms, err := c.getService()
		if err != nil {
			return err
		}
		vm, err := c.lookupVM(vmms, name)
		if err == ErrVMNotFound {
			return nil
		}
		if err != nil {
			return err
		}
		defer vm.Close()
		found = true
		return nil
	})
	return found, err
}

// GetVM returns the named VM or ErrVMNotFound.
func (c *WMIClient) GetVM(ctx context.Context, name string) (*VM, error) {
	var out *VM
	err := withCOM(func() error {
		vmms, err := c.getService()
		if err != nil {
			return err
		}
		vm, err := c.lookupVM(vmms, name)
		if err != nil {
			return err
		}
		defer vm.Close()
		out, err = c.readVM(vm)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// readVM reads the provider's view off a live VM object. Callers must be inside
// withCOM.
func (c *WMIClient) readVM(vm *virtualsystem.VirtualMachine) (*VM, error) {
	st, err := vm.State()
	if err != nil {
		return nil, err
	}
	id, err := vm.GetVirtualMachineHyperVVmId()
	if err != nil {
		return nil, err
	}
	out := &VM{
		Name:  vm.Name(),
		ID:    id,
		State: stateFromWMI(st),
	}
	// Generation, CPU, memory and notes come off the VM's active setting data.
	// They are best-effort: a VM is still usable to the provider without them,
	// and the identity fields above are what the controller keys on.
	if setting, err := vm.GetVirtualSystemSettingData(); err == nil {
		defer setting.Close()
		if gen, gerr := setting.GetProperty("VirtualSystemSubType"); gerr == nil {
			if s, ok := gen.(string); ok && s == virtualsystem.HyperVGeneration_V2 {
				out.Generation = 2
			} else if ok && s == virtualsystem.HyperVGeneration_V1 {
				out.Generation = 1
			}
		}
		if notes, nerr := setting.GetProperty("Notes"); nerr == nil {
			out.Notes = firstNote(notes)
		}
	}
	if proc, err := vm.GetProcessor(); err == nil {
		defer proc.Close()
		if n, perr := proc.GetCPUCount(); perr == nil {
			out.CPUCount = int(n)
		}
	}
	if mem, err := vm.GetMemory(); err == nil {
		defer mem.Close()
		if mb, merr := mem.GetProperty("VirtualQuantity"); merr == nil {
			out.MemoryStartupBytes = toInt64(mb) * 1024 * 1024
		}
	}
	return out, nil
}

// CreateVM copies the base image to a per-VM boot disk under vmsDir and defines
// a Generation 2 VM against that copy (DefineSystem), so the shared base image
// is never attached or booted. It sets startup memory (dynamic memory off),
// processor count and the Notes marker, attaches the boot disk on a SCSI
// controller, and adds a network adapter connected to the requested switch.
func (c *WMIClient) CreateVM(ctx context.Context, in CreateVMInput) (*VM, error) {
	disk := c.vmDiskPath(in.Name)
	if err := copyFile(in.VHDPath, disk); err != nil {
		return nil, fmt.Errorf("hyperv/wmi: copy base image: %w", err)
	}
	err := withCOM(func() error {
		vmms, err := c.getService()
		if err != nil {
			return err
		}

		setting, err := virtualsystem.GetVirtualSystemSettingData(c.host, in.Name)
		if err != nil {
			return fmt.Errorf("hyperv/wmi: new VM settings: %w", err)
		}
		defer setting.Close()
		if err := setting.SetHyperVGeneration(virtualsystem.HyperVGeneration_V2); err != nil {
			return fmt.Errorf("hyperv/wmi: set generation: %w", err)
		}
		if in.Notes != "" {
			// Msvm_VirtualSystemSettingData.Notes is a string array.
			if err := setting.SetProperty("Notes", []string{in.Notes}); err != nil {
				return fmt.Errorf("hyperv/wmi: set notes: %w", err)
			}
		}

		memSettings, err := memory.GetDefaultMemorySettingData(c.host)
		if err != nil {
			return fmt.Errorf("hyperv/wmi: memory settings: %w", err)
		}
		defer memSettings.Close()
		memSettings.SetSizeMB(uint64(in.MemoryStartupBytes / (1024 * 1024)))
		// Fixed memory, matching the PowerShell client (Set-VMMemory
		// -DynamicMemoryEnabled $false), to keep behavior predictable.
		if err := memSettings.SetProperty("DynamicMemoryEnabled", false); err != nil {
			return fmt.Errorf("hyperv/wmi: disable dynamic memory: %w", err)
		}

		procSettings, err := processor.GetDefaultProcessorSettingData(c.host)
		if err != nil {
			return fmt.Errorf("hyperv/wmi: processor settings: %w", err)
		}
		defer procSettings.Close()
		procSettings.SetCPUCount(uint64(in.CPUCount))

		vm, err := vmms.CreateVirtualMachine(setting, memSettings, procSettings)
		if err != nil {
			return fmt.Errorf("hyperv/wmi: DefineSystem: %w", err)
		}
		defer vm.Close()

		// Attach the pre-copied boot disk as the OS disk on a SCSI controller
		// (Gen2 has no IDE controller).
		if err := vmms.AddSCSIController(vm); err != nil {
			return fmt.Errorf("hyperv/wmi: add SCSI controller: %w", err)
		}
		vhd, vhddrive, err := vmms.AttachVirtualHardDisk(vm, disk, virtualsystem.VirtualHardDiskType_OS_VIRTUALHARDDISK)
		if err != nil {
			return fmt.Errorf("hyperv/wmi: attach boot disk: %w", err)
		}
		vhd.Close()
		vhddrive.Close()

		// Add a network adapter and connect it to the requested switch, the
		// equivalent of New-VM -SwitchName.
		if in.SwitchName != "" {
			if _, err := vmms.AddVirtualNetworkAdapter(vm, adapterName); err != nil {
				return fmt.Errorf("hyperv/wmi: add network adapter: %w", err)
			}
			vswitch, err := virtualswitch.GetVirtualSwitch(c.host, in.SwitchName)
			if err != nil {
				return fmt.Errorf("hyperv/wmi: find switch %q: %w", in.SwitchName, err)
			}
			defer vswitch.Close()
			if err := vmms.ConnectAdapterToVirtualSwitch(vm, adapterName, vswitch); err != nil {
				return fmt.Errorf("hyperv/wmi: connect to switch %q: %w", in.SwitchName, err)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return c.GetVM(ctx, in.Name)
}

// StartVM powers the VM on.
func (c *WMIClient) StartVM(ctx context.Context, name string) error {
	return c.withVM(name, func(vm *virtualsystem.VirtualMachine) error {
		return vm.Start()
	})
}

// StopVM powers the VM off, optionally forcibly (TurnOff). A graceful stop is
// requested when force is false.
func (c *WMIClient) StopVM(ctx context.Context, name string, force bool) error {
	return c.withVM(name, func(vm *virtualsystem.VirtualMachine) error {
		return vm.Stop(force)
	})
}

// RemoveVM removes the VM definition (DestroySystem) and cleans up the files the
// agent created for it: the per-VM boot disk and the cidata ISO. The file
// removals are best-effort so a VM created without cloud-init, or a disk already
// gone, does not fail the delete.
func (c *WMIClient) RemoveVM(ctx context.Context, name string) error {
	err := withCOM(func() error {
		vmms, err := c.getService()
		if err != nil {
			return err
		}
		vm, err := c.lookupVM(vmms, name)
		if err == ErrVMNotFound {
			return nil // already gone; still clean up files below
		}
		if err != nil {
			return err
		}
		defer vm.Close()
		return vmms.DeleteVirtualMachine(vm)
	})
	if err != nil {
		return err
	}
	_ = os.Remove(c.vmDiskPath(name))
	_ = os.Remove(c.isoPathFor(name))
	return nil
}

// SetFirmware configures Generation 2 firmware. Secure Boot is set in WMI (the
// Linux-critical part: generic cloud images do not boot with the default
// Microsoft Windows Secure Boot template). The boot-order step is left on
// PowerShell.
func (c *WMIClient) SetFirmware(ctx context.Context, in FirmwareInput) error {
	if err := withCOM(func() error {
		vmms, err := c.getService()
		if err != nil {
			return err
		}
		vm, err := c.lookupVM(vmms, in.Name)
		if err != nil {
			return err
		}
		defer vm.Close()
		setting, err := vm.GetVirtualSystemSettingData()
		if err != nil {
			return err
		}
		defer setting.Close()
		if err := setting.SetPropertySecureBootEnabled(in.EnableSecureBoot); err != nil {
			return fmt.Errorf("hyperv/wmi: set SecureBootEnabled: %w", err)
		}
		if in.EnableSecureBoot {
			// The Microsoft UEFI CA template is what lets most Linux images boot
			// with Secure Boot on; the default template is Windows-only.
			if err := setting.SetPropertySecureBootTemplateId(microsoftUEFICATemplateID); err != nil {
				return fmt.Errorf("hyperv/wmi: set SecureBootTemplateId: %w", err)
			}
		}
		if err := vmms.ModifyVirtualSystemSettings(setting, -1); err != nil {
			return fmt.Errorf("hyperv/wmi: modify firmware settings: %w", err)
		}
		return nil
	}); err != nil {
		return err
	}
	// TODO(wmi): move boot ordering into WMI. Gen2 boot order is the VSSD
	// BootSourceOrder property, an array of Msvm_BootSourceSettingData object
	// references that must be reordered so the OS disk leads; getting that
	// exactly right is not worth shipping half-working in this pass, so the OS
	// disk is put first via the existing, tested PowerShell builder. The cidata
	// DVD is non-bootable, so the firmware falls through to the disk regardless,
	// but this keeps the boot order explicit and matches the PowerShell backend.
	if _, err := c.ps.run(ctx, setBootOrderScript(in.Name)); err != nil {
		return fmt.Errorf("hyperv/wmi: set boot order (powershell): %w", err)
	}
	return nil
}

// AttachCloudInit builds the NoCloud cidata ISO from userData, writes it under
// isoDir on this host, and attaches it to a DVD drive (AddISODisk, which lands
// on the SCSI controller on a Gen2 VM). Building the ISO here, rather than
// accepting a path, is what lets the controller drive a remote agent: the
// user-data bytes cross the wire, the ISO is produced on the host.
func (c *WMIClient) AttachCloudInit(ctx context.Context, name string, userData []byte) error {
	data, err := cloudinit.Build(userData, cloudinit.Metadata{InstanceID: name, Hostname: name})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(c.isoDir, 0o700); err != nil {
		return fmt.Errorf("hyperv/wmi: cidata dir: %w", err)
	}
	isoPath := c.isoPathFor(name)
	if err := os.WriteFile(isoPath, data, 0o600); err != nil {
		return fmt.Errorf("hyperv/wmi: write cidata iso: %w", err)
	}
	return withCOM(func() error {
		vmms, err := c.getService()
		if err != nil {
			return err
		}
		vm, err := c.lookupVM(vmms, name)
		if err != nil {
			return err
		}
		defer vm.Close()
		isodisk, dvddrive, err := vmms.AddISODisk(vm, isoPath)
		if err != nil {
			return fmt.Errorf("hyperv/wmi: attach cidata iso: %w", err)
		}
		isodisk.Close()
		dvddrive.Close()
		return nil
	})
}

// SetVLAN configures access-mode VLAN tagging on the VM's network adapter. A
// zero id clears any VLAN tagging.
func (c *WMIClient) SetVLAN(ctx context.Context, name string, vlanID int) error {
	return withCOM(func() error {
		vmms, err := c.getService()
		if err != nil {
			return err
		}
		vm, err := c.lookupVM(vmms, name)
		if err != nil {
			return err
		}
		defer vm.Close()
		adapters, err := vm.GetVirtualNetworkAdapters()
		if err != nil {
			return err
		}
		defer adapters.Close()
		if len(adapters) == 0 {
			return fmt.Errorf("hyperv/wmi: VM %q has no network adapter", name)
		}
		na := adapters[0]
		if vlanID == 0 {
			return vmms.RemoveVirtualNetworkAdapterVLAN(na)
		}
		return vmms.SetVirtualNetworkAdapterAccessVLAN(na, uint16(vlanID))
	})
}

// GetIPAddresses returns the VM's IP addresses. It first tries the guest-
// reported path (Hyper-V KVP via integration services) through WMI, reading the
// guest network adapter configuration. When that reports nothing — as it does
// for a generic cloud image without hyperv-daemons — it falls back to the
// host-side neighbor lookup. An empty result is valid (the guest may not have
// sent traffic yet).
func (c *WMIClient) GetIPAddresses(ctx context.Context, name string) ([]string, error) {
	var ips []string
	err := withCOM(func() error {
		vmms, err := c.getService()
		if err != nil {
			return err
		}
		vm, err := c.lookupVM(vmms, name)
		if err != nil {
			return err
		}
		defer vm.Close()
		adapters, err := vm.GetVirtualNetworkAdapters()
		if err != nil {
			return err
		}
		defer adapters.Close()
		for _, na := range adapters {
			cfg, cerr := na.GetGuestNetworkAdapterConfiguration()
			if cerr != nil {
				continue // KVP not available for this adapter
			}
			v4s, _, gerr := cfg.GetIPAddresses()
			cfg.Close()
			if gerr != nil {
				continue
			}
			ips = append(ips, v4s...)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if ips = filterEmpty(ips); len(ips) > 0 {
		return ips, nil
	}
	// TODO(wmi): move the neighbor fallback into WMI. Get-NetNeighbor lives in
	// the root\StandardCimv2 namespace (MSFT_NetNeighbor), a separate provider
	// from root\virtualization\v2; wiring that up cleanly is deferred, so the
	// host-side MAC->neighbor fallback reuses the existing, tested PowerShell
	// builder. This is a pure host-networking lookup, not a VM mutation.
	nout, nErr := c.ps.run(ctx, getNeighborScript(name))
	if nErr != nil {
		return nil, nil // KVP simply reported nothing yet; that is valid
	}
	return parseNeighborIP(nout), nil
}

// firstNote renders the Msvm_VirtualSystemSettingData Notes property (a WMI
// string array, which go-ole may hand back as []string, []interface{} or a bare
// string) as the single note the provider stamps.
func firstNote(v interface{}) string {
	switch t := v.(type) {
	case string:
		return t
	case []string:
		if len(t) > 0 {
			return t[0]
		}
	case []interface{}:
		if len(t) > 0 {
			if s, ok := t[0].(string); ok {
				return s
			}
		}
	}
	return ""
}

// toInt64 coerces a WMI numeric property (which arrives as one of several Go
// numeric types, or a string, depending on the CIM type and go-ole) to int64.
func toInt64(v interface{}) int64 {
	switch t := v.(type) {
	case int64:
		return t
	case int32:
		return int64(t)
	case int:
		return int64(t)
	case uint64:
		return int64(t)
	case uint32:
		return int64(t)
	case float64:
		return int64(t)
	case string:
		var n int64
		_, _ = fmt.Sscan(t, &n)
		return n
	}
	return 0
}

// withVM looks up the named VM and runs f against it inside a COM-initialized
// thread, closing the VM afterwards. It is the shared shape of the simple
// single-VM operations.
func (c *WMIClient) withVM(name string, f func(*virtualsystem.VirtualMachine) error) error {
	return withCOM(func() error {
		vmms, err := c.getService()
		if err != nil {
			return err
		}
		vm, err := c.lookupVM(vmms, name)
		if err != nil {
			return err
		}
		defer vm.Close()
		return f(vm)
	})
}
