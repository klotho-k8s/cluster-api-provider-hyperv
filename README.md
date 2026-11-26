# cluster-api-provider-hyperv

A [Cluster API](https://cluster-api.sigs.k8s.io/) infrastructure provider for
**Microsoft Hyper-V**. It reconciles Cluster API `Machine`s into Hyper-V virtual
machines on a Windows Hyper-V host, delivering bootstrap data over a NoCloud
cloud-init ISO.

> **⚠️ Experimental — for single-Windows-node development environments only.**
> This provider is **experimental** and intended for local development on a
> **single Windows Hyper-V host**. It is **not** production-ready, and it does
> **not** target multi-host Hyper-V failover clusters or SCVMM-managed pools.
> The code builds, is unit tested, and implements the Cluster API v1beta2
> provider contract; the host agent and the VM lifecycle have been exercised
> against real Hyper-V, but full self-managed cluster bring-up is still being
> hardened. Use it to experiment on your own box, not to run anything you care
> about.

## What it is

The provider contributes four CRDs in the `infrastructure.cluster.x-k8s.io`
group (API version `v1alpha1`):

| Kind | Purpose |
| --- | --- |
| `HyperVCluster` / `HyperVClusterTemplate` | Cluster-level infrastructure. Holds the control plane endpoint and optional failure domains. |
| `HyperVMachine` / `HyperVMachineTemplate` | A single Hyper-V VM backing a Cluster API `Machine`. |

Two controllers reconcile them:

- **HyperVCluster controller** validates/records the control plane endpoint and
  marks the cluster infrastructure provisioned. Hyper-V has no load balancer, so
  the provider does **not** create one (see [Control plane endpoint](#control-plane-endpoint)).
- **HyperVMachine controller** adopts or creates a VM on the host, attaches a
  cloud-init cidata ISO, powers it on, records the provider ID and addresses, and
  on delete stops then removes the VM.

## The unusual bit: the controller needs Hyper-V host access

Most Cluster API infrastructure providers talk to a remote cloud API over the
network and can run anywhere in the management cluster. Hyper-V has no such API:
VMs are managed through the Hyper-V PowerShell cmdlets (`New-VM`, `Set-VM`,
`Start-VM`, `Get-VM`, ...), which only run on a Windows Hyper-V host. The
provider offers two ways to bridge that gap:

- **Host agent over gRPC (recommended).** Run the small `hyperv-agent` daemon as
  a Windows service on the Hyper-V host. It exposes the VM lifecycle over an
  authenticated, TLS gRPC API, and the controller drives Hyper-V through it. The
  controller then runs **anywhere** — an ordinary Linux pod in the management
  cluster — and executes **no PowerShell itself**. All PowerShell/WMI work
  happens inside the agent, on the host. See [Hyper-V host agent](#hyper-v-host-agent-grpc).
- **Controller on the host (lab).** Run the `manager` binary directly on the
  Windows Hyper-V host (or in a Windows pod pinned to `kubernetes.io/os:
  windows`), where it shells out to `powershell.exe` locally. Simpler for a
  single-host lab, but it departs from the usual "runs purely in-cluster" CAPI
  assumption and ties the controller to Windows.

`cmd/main.go` selects between the two: with `--hyperv-agent-endpoint` set the
controller dials the agent; without it, it uses the local PowerShell client.

## Adoption by name (boot node zero, then adopt)

The machine controller **looks up an existing VM by name first and adopts it if
present**, creating a VM only when none exists. This is deliberate and is the
main reason the provider exists: an external tool can pre-create "node zero"
(for example to bootstrap a self-hosted management cluster) and then hand it off
to Cluster API, which takes ownership without recreating it. The lookup name is,
in order of precedence:

1. `HyperVMachine.spec.vmName` (set this to match an externally created VM), else
2. the owning `Machine`'s name, else
3. the `HyperVMachine`'s name.

Adopted VMs are recorded with `status.adopted: true` and are **not**
re-provisioned (no ISO re-attach, no firmware reset); the provider only ensures
they are running. This mirrors the adopt-by-name guard used by the in-tree
Cluster API Docker provider (CAPD) and the Proxmox provider (CAPMOX).

## Cloud-init delivery

Bootstrap data (the Cluster API bootstrap secret's `value`) is delivered to the
guest as a **NoCloud `cidata` ISO**: an iso9660 image with volume label `CIDATA`
containing `user-data` (the bootstrap secret verbatim) and `meta-data`. The ISO
is built in-process with [`github.com/kdomanski/iso9660`](https://github.com/kdomanski/iso9660)
(no shelling out for ISO creation), written to the host, and attached to a
**SCSI DVD drive**. Generation 2 VMs are UEFI/SCSI and have no IDE controller,
so the DVD necessarily lands on the SCSI controller. The guest OS disk is a
cloud-init enabled Linux VHDX that you provide via `HyperVMachine.spec.image`.

## Control plane endpoint

Hyper-V provides no load balancer, so for v1 the control plane endpoint is
either:

- a **user-provided static VIP** — e.g. a `kube-vip` address advertised from
  inside the control plane (the pattern used by klotho), or
- the **address of the first control plane machine**.

The provider implements and validates the `spec.controlPlaneEndpoint` field but
does **not** build or manage a load balancer.

## Hyper-V client

All Hyper-V access goes through a small, fakeable Go interface
(`pkg/hyperv.Client`). The production implementation builds PowerShell scripts
and runs them via `powershell.exe`; every dynamic value is placed into a
single-quoted PowerShell literal with embedded quotes doubled, so untrusted
input (VM names, paths) cannot break out of its string. Tests use an in-memory
fake (`pkg/hyperv/fake`), so `go test` needs no PowerShell or Hyper-V.

| Interface method | Hyper-V cmdlet(s) |
| --- | --- |
| `Exists` | `Get-VM -ErrorAction SilentlyContinue` |
| `GetVM` | `Get-VM \| Select-Object ... \| ConvertTo-Json` |
| `CreateVM` | `New-VM`, `Set-VMProcessor`, `Set-VMMemory`, `Set-VM` (notes) |
| `StartVM` | `Start-VM` |
| `StopVM` | `Stop-VM [-TurnOff] -Force` |
| `RemoveVM` | `Remove-VM -Force` |
| `SetFirmware` | `Get-VMHardDiskDrive`, `Set-VMFirmware` (boot order + Secure Boot) |
| `AttachISO` | `Add-VMDvdDrive` |
| `SetVLAN` | `Set-VMNetworkAdapterVlan` |
| `GetIPAddresses` | `Get-VMNetworkAdapter \| Select -Expand IPAddresses` |

## Hyper-V host agent (gRPC)

`cmd/hyperv-agent` is a standalone daemon that runs as a Windows service on the
Hyper-V host and exposes `pkg/hyperv.Client` over gRPC. It is what lets the
controller stay pure Go on Linux: the controller is a gRPC client
(`pkg/agent.Client`, which implements `hyperv.Client`), the agent is the server
(`pkg/agent.Server`, wrapping the PowerShell client). The wire contract is
`proto/hypervagent/v1/agent.proto`.

### Cloud-init over the wire

The controller never writes to the host filesystem. The bootstrap secret is sent
to the agent as cloud-init **user-data bytes** (`AttachCloudInit`), and the agent
builds the NoCloud `cidata` ISO on the host (under its own data directory) before
attaching it. No ISO path is ever shared across the Linux/Windows boundary. In
local mode the PowerShell client does the same under `--iso-dir`.

### Security model

- **Transport:** TLS is always on. With `--tls-cert`/`--tls-key` the agent
  serves the given certificate; otherwise it generates a self-signed cert into
  its data directory on first run. The self-signed cert covers `localhost`, the
  host IPs it binds, the machine hostname, and any `--tls-hosts` you add, so the
  controller can verify it **by name**. The controller trusts it via
  `--hyperv-agent-ca`; with no CA given it falls back to the host's system root
  CAs.
- **Authentication:** every RPC must carry a bearer token, checked by a gRPC
  unary interceptor with a constant-time compare; a missing or wrong token is
  rejected `Unauthenticated`. The controller reads the token from
  `HYPERV_AGENT_TOKEN` (shared as a Kubernetes Secret). The token is never placed
  on the service command line. `install` writes it to a file in the service data
  directory and passes only `--token-file`; that directory's DACL is restricted
  to **LocalSystem and Administrators** (inheritance disabled), so the token — and
  the generated TLS private key beside it — are not readable by unprivileged
  local users. Treat any account in the local Administrators group as able to
  read the token and drive the agent: that is the trust boundary.
- **Binding:** the default port is **7443** (deliberately not 5986, which is
  WinRM-over-HTTPS and commonly already bound). With no `--listen` host the agent
  picks the first non-loopback, non-link-local IPv4 and logs it, rather than
  silently listening on every interface. An explicit `0.0.0.0` is honored but
  warned about.
- **Windows identity / RBAC.** The agent needs rights to manage Hyper-V; it does
  **not** need any Kubernetes RBAC of its own (it never talks to the API
  server). By default the service runs as **LocalSystem**, which already holds
  full Hyper-V rights. For least privilege, run it under a dedicated account
  that is a member of the local **Hyper-V Administrators** group instead (and
  grant that account read on the token file / data directory). The only
  Kubernetes RBAC involved is on the controller side: permission to read the
  Secret that holds the token.

### Install (on the Windows host)

```powershell
# one machine-wide token, shared with the controller as a Secret
$env:HYPERV_AGENT_TOKEN = "<a long random token>"

# register + start the service (sc.exe under the hood); depends on vmms + Tcpip.
# The token goes to a locked-down file, not the command line; omit --listen to
# auto-pick the host IP, or pin it (and add it to the cert SANs) explicitly:
.\hyperv-agent.exe install --listen 192.168.1.10:7443 --tls-hosts hv-host.lan

# remove it again (waits for a clean stop first)
.\hyperv-agent.exe uninstall
```

`install` reads the token from `$env:HYPERV_AGENT_TOKEN` (or `--token`).
Reinstalling with a new token or flags stops the running service first so the
change takes effect. Run it in the foreground for local testing with
`hyperv-agent run`.

### Wire the controller to the agent

Create the token Secret and point the controller at the agent:

```sh
kubectl create secret generic hyperv-agent-token \
  --from-literal=token="<the same token>" -n capi-hyperv-system
```

```sh
manager \
  --hyperv-agent-endpoint hv-host.lan:7443 \
  --hyperv-agent-ca /etc/hyperv-agent/ca.crt
# HYPERV_AGENT_TOKEN is injected into the manager pod from the Secret above.
```

With no `--hyperv-agent-endpoint`, the controller falls back to the local
PowerShell client (the lab layout). `--hyperv-agent-insecure` disables TLS for
development only.

### Build / regenerate

```sh
make agent     # build bin/hyperv-agent.exe (windows/amd64)
make proto     # regenerate the gRPC stubs from the .proto (needs protoc)
```

## Repository layout

```
api/v1alpha1/            CRD types + generated deepcopy
internal/controller/     HyperVCluster and HyperVMachine reconcilers
internal/service/vmservice/  adopt-or-create and stop-then-remove VM lifecycle
pkg/hyperv/              Hyper-V client interface, PowerShell impl, fake
pkg/cloudinit/           NoCloud cidata ISO builder
cmd/                     manager entrypoint
config/                  CRDs, RBAC, manager Deployment, kustomize
templates/               example Cluster API topology
metadata.yaml            clusterctl contract mapping (v1beta2)
```

## Developing

Requires Go 1.25+. `controller-gen` and `kustomize` are run via `go run` at
pinned versions, so no kubebuilder install is needed.

```sh
make generate   # DeepCopy
make manifests  # CRDs + RBAC
make build      # manager binary
make test       # unit tests (no Hyper-V required)
```

## What is implemented vs. what you must do to test on a real host

**Implemented and unit tested (faked backend):**

- CRD types and the v1beta2 contract (endpoint, provider ID, conditions,
  `status.initialization.provisioned`, finalizers).
- Cluster and machine reconcilers, including adopt-by-name, stop-then-remove
  delete, and the stuck-finalizer unblock when the owner `Machine` is gone.
- PowerShell command construction + JSON parsing, with injection-safe quoting.
- NoCloud cidata ISO generation.

**Stubbed / simplified / untested on hardware:**

- The provider does not resize the OS disk yet; `spec.diskSizeGB` is accepted
  but not applied (the VM uses the base image size).
- No webhooks/validating admission; validation relies on CRD OpenAPI schema.
- Guest address discovery assumes the Hyper-V integration services report
  addresses via `Get-VMNetworkAdapter`; timing/format on real guests is
  unverified.
- Not yet exercised end to end with `clusterctl`, a real Windows host, or a real
  cloud-init image.

**To try it on a real Hyper-V host you will need to:**

1. Build and run the manager on (or with rights to) the Windows Hyper-V host.
2. Provide a cloud-init enabled Linux VHDX and set `spec.image` to its path.
3. Create a Hyper-V virtual switch and set `spec.switchName`.
4. Supply a control plane endpoint (static VIP or first CP address).
5. Verify VM creation, cloud-init pickup of the cidata ISO, address reporting,
   and deletion, and adjust the cmdlet calls in `pkg/hyperv/powershell.go` to
   match your host's behavior.

## License

MIT (see [LICENSE](LICENSE)). Note that the Cluster API ecosystem norm is
Apache-2.0; switch the license if you prefer to align with upstream conventions.
