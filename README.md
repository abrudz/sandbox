# sandbox

A web service that runs short Dyalog APL programs, with Try It Online's interface for APL. Every run is jailed by nsjail: it has no network, nothing it writes survives it, and its time, CPU, memory, disk and processes are limited. The public instance is to be served at https://dyalog.run.

The whole service is one container image, `ghcr.io/abrudz/sandbox`, built on Dyalog's official `docker.io/dyalog/dyalog:latest`. It is rebuilt, tested and republished when Dyalog publishes a new image, and at least weekly for updates to its other packages.

## Running it locally

You need Docker with cgroup v2: Docker Desktop on Windows (with WSL 2) or macOS, or Docker Engine on Linux.

```
curl -LO https://raw.githubusercontent.com/abrudz/sandbox/main/deploy/seccomp.json
docker run --rm --cap-add SYS_ADMIN --security-opt apparmor=unconfined \
  --security-opt systempaths=unconfined --security-opt seccomp=seccomp.json \
  --cgroupns private -p 127.0.0.1:8000:8080 ghcr.io/abrudz/sandbox
```

Then open http://localhost:8000, type a program such as `⎕←2+2`, and press Ctrl-Enter to run it. Permalinks have TIO's format, so a tio.run link for APL (Dyalog Unicode) opens here with `https://tio.run/` replaced by `http://localhost:8000/`.

The `127.0.0.1:` in `-p` publishes the port on this machine's loopback address only, so other machines cannot reach the service. Ctrl-C stops it once its runs in progress have finished.

nsjail builds a jail for every run, and each flag gives it something Docker otherwise withholds:

- `--cap-add SYS_ADMIN`: creating namespaces, mounts and cgroups.
- `apparmor=unconfined`: Docker's AppArmor profile denies `mount`.
- `systempaths=unconfined`: the kernel refuses a new `/proc` while parts of the container's are masked.
- `seccomp=seccomp.json`: Docker's default seccomp profile, which refuses `pivot_root`, with `pivot_root` allowed under `CAP_SYS_ADMIN`. It also refuses i386 and x32 system calls on x86_64, which Docker's default allows.

`--cgroupns private`, Docker's default on cgroup v2, gives the container a cgroup namespace of its own. The entry point changes the cgroup at the root of that namespace, so it refuses to start in the host's.

The container keeps Docker's other capability limits, its device filter and its seccomp profile. Root in it still holds `CAP_SYS_ADMIN`, so run only images you trust. `--privileged` in place of the first four flags also works, but gives the container every capability and the host's devices.

To update, `docker pull ghcr.io/abrudz/sandbox`. Each image is also tagged with its Dyalog version, such as `ghcr.io/abrudz/sandbox:21.0.55109.0`.

To check a running instance from a shell with `curl` and `jq`:

```
scripts/smoke http://localhost:8000
```

### Configuration

Environment variables, given to `docker run` with `-e NAME=value`, set the limits:

| Variable | Default | Meaning |
|---|---|---|
| `SANDBOX_TIMEOUT` | `10s` | Wall-clock limit of one run |
| `SANDBOX_OUTPUT_LIMIT` | `131072` | Bytes kept of each of stdout and stderr |
| `SANDBOX_MEMORY` | `512M` | Memory limit of one run; the workspace (`MAXWS`) is half of it, and the .NET heap a quarter |
| `SANDBOX_CPUS` | `1` | CPUs one run may use, such as `0.5` |
| `SANDBOX_PIDS` | `64` | Processes one run may have |
| `SANDBOX_REQUEST_LIMIT` | `65536` | Bytes of code plus input in one request |
| `SANDBOX_CONCURRENCY` | the number of CPUs | Runs in progress at once |
| `SANDBOX_CLIENT_CONCURRENCY` | `2` | Run requests one client may have in progress at once, counted from their arrival |
| `SANDBOX_CLIENT_RATE` | `30` | Runs one client may start per minute |
| `SANDBOX_READ_CONCURRENCY` | `64` | Run requests whose bodies are read at once, across all clients |
| `SANDBOX_TRUSTED_PROXIES` | none | Comma-separated addresses or networks whose `X-Forwarded-For` header names the client |
| `SANDBOX_SOCKET` | none | Absolute path of a Unix socket to listen on, open to root only, instead of TCP port 8080; a request on it names the client in `X-Forwarded-For` |

### API

- `POST /api/v1/run` with `{"code": "⎕←2+2", "input": "", "args": []}` runs the program and answers with its `stdout`, `stderr`, `exitCode`, `signal`, `timedOut`, whether each output was truncated, its wall, user and system time, and `dyalogVersion`.
  An argument `NAME=value` sets the interpreter's configuration parameter `NAME`, as on TIO; the other arguments go to the program.
  The request must have `Content-Type: application/json`. The per-client limits treat each IPv4 address, and each IPv6 /64, as one client.
  A run can use .NET classes through `⎕USING`, with the .NET runtime that Dyalog's image installs. A .NET allocation past the heap limit, a quarter of the run's memory limit, throws `OutOfMemoryException`, which the program can trap as `EXCEPTION`.
- `GET /api/v1/version` answers with `dyalogVersion`, `imageDigest` (the digest of the `dyalog/dyalog` image the image was built from), `commit`, and `timeoutMs`, the wall-clock limit of one run.
- `GET /healthz` answers 200 while the service is serving.

The plan, [docs/plans/dyalog-runner.md](docs/plans/dyalog-runner.md), describes the contract in full.

## The public instance

The public instance runs on a dedicated Ubuntu 24.04 VM whose only secret is Caddy's TLS key. Caddy serves the site over TLS and passes requests to the sandbox container through a Unix socket in a volume the two share. The sandbox container has no network but loopback, so it can reach neither the internet nor the VM, and it runs with the four options described under Running it locally. Caddy refuses a request body larger than runner-api accepts. The container's memory limit is the VM's memory less 512 MiB, with no swap; it runs one run per CPU, or fewer so that every run fits within that limit, and has a process limit to match. Before provisioning, point the domain's DNS records (A, and AAAA for IPv6) at the VM. Then, on the VM:

```
git clone https://github.com/abrudz/sandbox
cd sandbox
sudo deploy/setup-host
```

`deploy/setup-host` does the following:
- installs Docker Engine and compose, `ufw` and unattended upgrades;
- masks apport and sets the kernel's core pattern to a file name, so that no crash, which a run can cause at will, starts a helper on the VM;
- allows SSH, HTTP and HTTPS through the firewall and nothing else;
- installs the instance in `/opt/sandbox`, with the seccomp profile and limits sized to the VM, and the daily update as `sandbox-update.service` and `sandbox-update.timer`;
- starts the instance.

It reports each change it makes, and running it again reports `No changes`. Two environment variables change its settings:
- `SITE_ADDRESS`, default `dyalog.run`: the domain Caddy serves.
- `SANDBOX_IMAGE`, default `ghcr.io/abrudz/sandbox:latest`: the image to run.

For example, `sudo SITE_ADDRESS=example.org deploy/setup-host`.

To check the instance from any machine with `curl` and `jq`:

```
scripts/smoke https://dyalog.run
```

Keeping it up to date:
- **Images.** The publish workflow runs daily at 04:23 UTC. At 05:00 UTC the VM's timer pulls any new image and replaces the container. The old container finishes its runs in progress first, and new requests wait or fail for the few seconds the replacement takes.
- **System packages.** Unattended upgrades install them daily and reboot the VM at 03:30 when an upgrade needs it. Docker brings the instance back after a reboot.
- **Changes to `deploy/`.** After `git pull`, run `sudo deploy/setup-host` again.
- **Rollback.** To go back to an earlier Dyalog version, run `setup-host` with `SANDBOX_IMAGE` set to one of the version tags.

## Development

The plan in [docs/plans/](docs/plans/) describes the architecture and the work. Each issue's pull request is recorded in [docs/prs/](docs/prs/).

- `go test ./...` runs the unit tests, with a fake executor and fake services in place of the container.
- `node --test test/ui/codec.test.js` tests the frontend's permalink code.
- Linters: `gofmt`, `go vet` and `staticcheck` for Go, `shellcheck` for the scripts.
- [`.github/workflows/ci.yml`](.github/workflows/ci.yml) runs these on every push. It also builds the image, runs the integration suite (`test/integration`) and the browser suite (`test/ui`) against it, and provisions a runner with `deploy/setup-host` to run the deployment suite (`test/deploy`).
- [`.github/workflows/publish.yml`](.github/workflows/publish.yml) publishes the image. Only its last job can push packages, and it runs nothing from the repository: it pushes the image the build job saved once the suites have passed. `go test ./test/workflows/` checks this, and that every action is pinned to a commit.

## Licence

This project is licensed under the MIT licence; see [LICENSE](LICENSE). The image contains Dyalog APL, whose licence the image keeps at `/LICENSE`. [THIRD_PARTY_NOTICES](THIRD_PARTY_NOTICES) lists the other software in the image and their licences.
