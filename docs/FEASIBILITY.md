# Feasibility: a VM-grade agent sandbox on Lima

**Verdict: feasible, and already working.** A Lima-based sandbox gives a
*stronger* isolation boundary than Yolobox (a dedicated VM per project instead
of a container inside a shared Docker VM), removes the third-party supply chain
that Yolobox depends on, and can be a drop-in replacement for the daily
`yolobox claude` workflow. The measured costs are a one-time 2–4 minute box
build per project, ~20 s to resume a stopped box, and 2–4 GiB of RAM per
running box.

This document records the analysis and the measurements behind the v0.1.0
release, so the decision can be re-checked later.

---

## 1. Requirements

Corral was built after a real supply-chain incident involving AI tooling. The
requirement is therefore not "stop the agent from making mistakes"
(Yolobox's stated goal) but:

1. **Contain a hostile process.** A malicious package, a poisoned MCP server or
   a prompt-injected agent must not reach the developer's home directory,
   SSH keys, cloud credentials, Keychain, browser sessions or other repos.
2. **Do not introduce a new supply chain while doing so.** The sandbox tool
   itself, and everything it puts inside the sandbox, must be auditable and
   come from sources already trusted.
3. **Zero friction**, or people will bypass it: `cd project && <tool> claude`
   must keep working, with persistent login and a persistent toolchain.
4. **Visibility**: who ran what, where, with which credentials.

## 2. Assessment of Yolobox against those needs

Facts gathered on 2026-08-25 from the repository and docs
(github.com/finbarr/yolobox, v0.19.0 released 2026-08-17):

| Aspect | Finding |
|---|---|
| Ownership | Single-maintainer project; repository created 2026-01-09; 635 stars; MIT. |
| Isolation | A Docker/Podman/Apple-`container` container. On macOS with Docker Desktop, OrbStack or Colima that container runs **inside the runtime's shared Linux VM**, which has the developer's `/Users` file-shared into it. A container escape lands in that VM, next to every shared host directory and every other container. |
| `--docker` flag | Mounts the **host Docker socket** into the sandbox. That is root on the Docker VM and therefore read/write access to everything Docker Desktop shares from the Mac. The docs say so, but the flag is one keystroke away. |
| Sandbox contents | `ghcr.io/finbarr/yolobox:latest` — a ~2 GB image built by the maintainer bundling Node, Go, Bun, Python, 8 AI CLIs and their installers (`curl \| bash` from several vendors, npm global installs). The tag is **mutable**; `yolobox upgrade` and `--ensure-latest` pull whatever `latest` points to. |
| Installer | `curl -fsSL …/install.sh \| bash` or a personal Homebrew tap. |
| Credentials | Copies the host Claude OAuth login out of macOS Keychain into the container by default when `--claude-config` is used; forwards `ANTHROPIC_API_KEY`, `GH_TOKEN`, … automatically. |
| Own security page | "Protection from accidents, not a magic anti-container-escape theorem… If you are defending against hostile code rather than careless code, move up to stronger isolation. macOS: UTM, Parallels, **Lima**." |

Yolobox is a good tool for its stated purpose — its own security page is candid
about the boundary. For the hostile-code threat model above it has three
structural mismatches: the boundary is a container in a VM that also holds the
home directory; the sandbox image is a large, mutable, third-party artifact;
and the tool sits in the most security-sensitive position on a developer's
machine while adding new trust in its distribution path. These follow from its
design goals rather than from any bug a PR could fix.

## 3. Underlying technology options for macOS

| Option | Boundary | macOS req. | Notes |
|---|---|---|---|
| **Lima (vz driver)** | Dedicated Linux VM per box, Apple Virtualization.framework, virtiofs mounts | 13.5+ | CNCF Incubating (Oct 2025); v2.2.0 (2026-07-21); foundation of Colima, Rancher Desktop, AWS Finch; `brew install lima`; digest-pinned cloud images; declarative YAML; pluggable drivers (vz/qemu/krunkit). |
| Docker Desktop / OrbStack / Colima container | Container inside a shared VM | any | What Yolobox does. Shares one kernel and the host file shares among all containers. |
| Apple `container` | One micro-VM per container | **26+** | Attractive long-term (Apple-maintained, VM per container), but it excludes every Mac below macOS 26. Revisit as adoption grows. |
| Tart (Cirrus Labs) | VM via Virtualization.framework | 13+ | Excellent for CI images; Fair Source licence with commercial limits; no first-class provisioning story for a dev-loop tool. |
| UTM / Parallels / VMware | Full desktop VM | any | GUI-first, heavy, not scriptable enough for `cd && run`. |
| Direct Virtualization.framework (Go `Code-Hex/vz`) | VM | 13+ | Maximum control, but we would re-implement cloud-init, virtiofs, port-forwarding, SSH and image caching that Lima already has and tests. Months, not days. |
| krunkit / libkrun | VM (GPU-capable) | 14+ | Experimental Lima driver; only worth it for GPU workloads. |

**Recommendation: Lima with the `vz` driver and `virtiofs` mounts.** It is the
only option that is simultaneously VM-grade, mature, foundation-governed,
Homebrew-installable, macOS-13-compatible and scriptable. The architecture is
kept driver-agnostic so that Apple `container` can be added later without
rewriting the tool (see `docs/ARCHITECTURE.md`).

## 4. Proof of concept — measured on this machine

MacBook (Apple M-series, 10 cores, 24 GiB), macOS 15.7.7, Lima 2.2.0, Ubuntu 24.04 cloud image.

| Measurement | Result |
|---|---|
| First `limactl start` incl. 600 MB image download, apt base, Claude Code install | **2 min 44 s** |
| `limactl stop` | 1 s |
| Warm `limactl start` (existing box) | **24 s** |
| Golden image build (base + node + Claude Code), once per toolchain set | ≈ the from-scratch build above, **2 min 44 s** |
| New project box from the golden: `limactl clone` (APFS copy-on-write) + first boot re-running the idempotent scripts + project delta (`packages = ["sl"]`, `hide`) + session start | **15 s** (measured 2026-08-26) |
| Golden on disk | 3.0 GiB; clones share its blocks until they diverge |
| `rosetta = true`: Go binary cross-compiled for linux/amd64 run inside the arm64 box | works — `/proc/sys/fs/binfmt_misc/rosetta` registered by Lima, prints `hello from amd64` (2026-08-26); later confirmed with a real amd64 Android container |
| Instance on disk after build | 2.8 GiB (sparse; grows with installed tools) |
| **Memory, host-resident (RSS of the `Virtualization.VirtualMachine` process, 8 GiB ceiling)** — idle after boot | **2.2 GiB** (guest `used` 574 MiB; the rest is page cache from provisioning) |
| … after `npm install typescript eslint webpack vite jest` (25 s) | 2.85 GiB (guest `used` 432 MiB + 1.1 GiB cache) |
| … while the guest holds a 3 GiB allocation | 5.5 GiB |
| … 15 s after the guest freed it | **5.5 GiB — unchanged.** The vz guest has no virtio-balloon device (`/sys/bus/virtio/drivers`: net, rng, serial, scsi, virtiofs, vsock), so freed guest memory is at best *partially* returned: a second machine measured 147 MB idle → 1904 MB with 2 GiB allocated → 790 MB twenty seconds after freeing it, and an idle box after a Flutter build at 485 MiB guest `used` / **6 039 MB** host `phys_footprint` with `phys_footprint_peak` equal to current. Plan concurrency by the **high-water mark**, not the idle figure: a box that ran one heavy build stays heavy until it stops, which is why the default is 4 GiB, `idle_stop` exists, and the dashboard shows the host footprint next to the guest figure (`footprint -p <vm pid>`; `ps rss` reads ~2× too high for a VM process). |
| **Overhead under load** (12-core/32 GB Mac, three boxes busy: a Flutter build + two Claude reviews) | **4.2–4.7 s per `corral run` invocation** against 1.4 s idle; load 5.4–7.4, swap stable at 3.2 GB, total VM `phys_footprint` 12.4 GB against 24 GiB of caps. The 1.4 s headline is the best case (2026-08-28). |
| Shared image cache (once per Mac) | 2.5 GiB |
| Project mount (virtiofs, rw) | Files created in the guest appear on the host with the host UID; symlinks and renames work both ways. |
| Guest user | Same username and UID as the host user → git and file ownership "just work". |
| Host SSH keys in the guest | None (`ssh.loadDotSSHPubKeys: false`; Lima generates a per-box key). |
| Claude Code in guest | Installs from `claude.ai/install.sh` (Anthropic's official native installer); `CLAUDE_CONFIG_DIR` on a virtiofs mount works, so one login serves every box. |
| Secret transport | SSH `SendEnv`/`AcceptEnv` — values never appear on a host or guest command line. Verified. |
| Path-length constraint | Lima puts a UNIX socket at `$LIMA_HOME/<box>/ssh.sock.*`, which must be < 104 bytes. Corral therefore keeps state in `~/.corral` and caps box names. |

## 5. What Lima gives us that Yolobox cannot

* **A real VM boundary per project.** Kernel exploit in the sandbox → you own a
  throwaway Ubuntu VM that can see one project directory. Not the Docker VM,
  not `/Users`.
* **Docker inside the box, never the host socket.** `toolchains = ["docker"]`
  installs Ubuntu's `docker.io` in the guest; compose, testcontainers and
  friends work, and an escape from *those* containers still lands inside the box.
* **No third-party sandbox image.** The guest is Lima's digest-pinned Ubuntu
  cloud image plus Ubuntu apt packages plus checksum-verified vendor tarballs
  (Node from nodejs.org, Go from go.dev) plus the agent vendor's own installer.
  Every line is in this repository.
* **Snapshots.** Snapshot the box disk before a risky session, roll back after.
* **Read-only project mode** at the hypervisor level, not `chmod`.

## 6. Costs, limits and risks — and what we do about them

| Risk / limit | Impact | Mitigation in v0.1 | Later |
|---|---|---|---|
| RAM: a running box grows into its ceiling and vz never gives it back | 4 boxes ≈ 16 GiB at the 4 GiB default (measured 2026-08-26) | default lowered 8 → 4 GiB; `idle_stop` (30 m) stops unused boxes; `doctor` prints the box budget; a project may raise `memory` only up to half the host | Balloon/dynamic memory when Lima exposes it |
| First build on a Mac is 2–4 min | Friction on first use | Golden image built once per toolchain set; every further project is a 15 s copy-on-write clone (`corral golden`) | Ship a prebuilt golden image |
| virtiofs is slower than a native disk for huge trees | Slower `go build`/`npm ci` on very large repos | Build caches live on the VM disk (`~/go`, `~/.npm`, `~/.cache`) | Optional per-project cache dir mounts |
| Host→guest inotify events are not propagated | Watchers inside the box do not see edits made on the Mac | Agents poll; most tools re-read on demand | Lima `mountInotify` (experimental) behind a flag |
| The guest can reach the host network (`host.lima.internal`) and the LAN | Same as Docker today; a hostile agent could hit local services | Documented; no host services are exposed by default | Egress allow-list via in-guest proxy + no-sudo profile |
| The agent has `sudo` in the box | It can disable any in-guest control | The boundary is the VM, not in-guest policy | `profile = "strict"` (no sudo, egress proxy) |
| The project directory itself is writable | An agent can still damage the repo | git + `readonly_project` + snapshots; identical to Yolobox | — |
| Lima is a dependency too | Breaking changes upstream | `minimumLimaVersion`; `doctor` reports a Lima other than the tested release; template hash detects drift | Track upstream releases and bump `TestedVersion` deliberately |
| Resuming a stopped box is a boot (10–25 s measured), not a resume | Lima VM save/restore would make it ~13 s | Golden clones (15 s new box) and `idle_stop` keep boxes around; boot time is acceptable | Upstream `vz: implement auto save/restore` ([lima-vm/lima#2900](https://github.com/lima-vm/lima/pull/2900)) is an open **draft, idle since 2025-07**. Re-check it whenever the tested Lima release is bumped; if it lands, evaluate `stop --save` against virtiofs consistency first (a restored guest with a stale view of the mounted project is a data-loss risk) |
| Configuration changes need a rebuild | cpus/memory/mounts are fixed at VM creation | Drift detection warns; `corral rebuild` | `limactl edit` for the subset Lima allows |

## 7. Decision

Build **Corral** on Lima/vz, in Go, as a drop-in for `yolobox claude`, with
the agent layer behind an interface so Codex/Gemini/OpenCode can be added
without touching the VM code. Ship macOS first; Lima's qemu/WSL2 drivers are
the path to Linux and Windows.
