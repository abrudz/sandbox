# Sandboxed Dyalog APL runner

## Context

A small public service is needed that runs short Dyalog APL programs submitted by untrusted users, in the style of Try It Online (TIO) and Attempt This Online (ATO), but for a single version of Dyalog APL: the latest release. Every run must be fully sandboxed: no network, no state that survives the run, and hard limits on CPU, wall-clock time, memory, disk and process count. The user interface copies TIO's interpreter page for `apl-dyalog` (https://tio.run/#apl-dyalog).

The deliverable is one container image that runs the whole service:

```
docker run --rm --cap-drop ALL --cap-add SYS_ADMIN --cap-add SETUID --cap-add SETGID \
  --security-opt apparmor=unconfined --security-opt systempaths=unconfined \
  --security-opt seccomp=deploy/seccomp.json --cgroupns private \
  --read-only --tmpfs /tmp:size=16m -p 127.0.0.1:8000:8080 ghcr.io/abrudz/sandbox
```

The backend is new; the frontend is taken from TIO, which is MIT-licensed:

- TIO (`TryItOnline/tryitonline`, `TryItOnline/TioSetup`) has had no commits since 2019 and targets Fedora 26. Its sandbox depends on SELinux `seunshare` and cgroups v1 (`cgset cpu.shares`, `memory.soft_limit_in_bytes` in `bin/run`), which current systemd releases no longer support. Its frontend (`usr/share/tio.run/`) is reusable as it stands. Its Dyalog wrapper (`wrappers/apl-dyalog`) predates `dyalogscript`, which replaces it here.
- ATO (`attempt-this-online/attempt-this-online`) is maintained and already runs `docker.io/dyalog/dyalog:latest`, but it installs only on a fresh Arch Linux VM and is licensed AGPL-3.0. Its sandbox (`src/sandbox.rs`) sets no per-run memory limit (`memory.high` is commented out), no CPU limit and no seccomp filter.

The image is built on the official `docker.io/dyalog/dyalog:latest` image and is rebuilt, tested and republished automatically when upstream publishes a new `latest`. As of 2026-11-18, that is Dyalog 21.0 (build 21.0.55109), Unicode edition, on `debian:trixie-slim`.

The service is non-commercial, so Dyalog's default Run-Time Licence covers it.

Repository: https://github.com/abrudz/sandbox (MIT licence). The first deployment is a local instance; the public instance will be served at `dyalog.run`.

## Architecture

```
browser --> runner-api (Go, port 8080, inside the sandbox container)
              |   GET  /             TIO frontend, adapted (web/)
              |   POST /api/v1/run   validation, size limits, concurrency and rate limits
              v
            nsjail --> dyalog      one jailed process tree per run, in its own namespaces and cgroup
```

The public instance puts Caddy in front of the container for TLS.

### Container

- `image/Containerfile` is a multi-stage build:
  1. Build nsjail from source, at a commit named by its hash; Debian does not package it. A weekly workflow (`.github/workflows/nsjail.yml`) keeps an issue open while nsjail's master has moved past the pinned commit, and the pin moves only after the changes are reviewed against `image/nsjail.cfg` and `image/nsjail.patch`.
  2. Build `runner-api` with Go 1.27.
  3. Final stage `FROM docker.io/dyalog/dyalog:latest`, adding nsjail's runtime libraries, nsjail, `runner-api`, `web/`, `image/run`, `image/nsjail.cfg`, the seccomp policy and `image/entrypoint`.
- `image/entrypoint` replaces the base image's entry point, which prints a banner. It moves itself into a leaf cgroup, enables the `cpu`, `memory` and `pids` controllers for child cgroups, and then starts `runner-api`.
- `runner-api` runs as root inside the container because nsjail needs that to create namespaces and cgroups. Every run executes as an unprivileged uid inside its own user namespace.
- The host needs cgroup v2 and Docker Engine, Docker Desktop or rootful Podman; the flags in Context are Docker's, and untested with Podman. The container runs with those flags, each of which nsjail needs: every capability dropped but three, `CAP_SYS_ADMIN` to create namespaces, mounts and cgroups, and `CAP_SETUID` and `CAP_SETGID` to map the jail's uid and gid, which are not root's; AppArmor unconfined, because Docker's AppArmor profile denies `mount`; Docker's masked paths unconfined, because the kernel refuses a new `/proc` while parts of the container's are masked; and `deploy/seccomp.json`, Docker's default seccomp profile with `pivot_root` allowed under `CAP_SYS_ADMIN`, because the default refuses it, and with no i386 or x32 calls on x86_64. `image/entrypoint` remounts `/sys/fs/cgroup` read-write, which Docker mounts read-only. The container keeps Docker's device filter and seccomp profile. `--privileged` must not replace these flags: it gives the container every capability and drops both, and without the profile x32 calls pass the jail's own filter, which matches x86_64 numbers only. The profile applies inside the jail too, but cannot tell the jail from nsjail, so the calls it allows nsjail under `CAP_SYS_ADMIN` pass it for the jail as well, and nsjail's own policy refuses them. Root in the container holds `CAP_SYS_ADMIN`, so the container is still not the security boundary: the jail is, and on the public instance a dedicated VM whose only secret is Caddy's TLS key is the outer boundary.
- The image keeps Dyalog's `LICENSE` file, as the base image's README requires for derived images. `THIRD_PARTY_NOTICES` lists TIO's frontend (MIT), pako (MIT and zlib), `abrudz/lb` (MIT), the APL387 and APL335 fonts (Unlicense), the APL logo (CC0) and nsjail (Apache-2.0); it ships in the repository and in the image.
- The build sets three labels: the upstream image digest, the full Dyalog version (read from the interpreter during the build) and the source commit.
- Published images cover amd64 only.

### Request flow

1. The frontend joins header, code and footer, as TIO does, and sends `POST /api/v1/run` with the code, the input and the arguments.
2. Before reading the body, `runner-api` counts the request against its client (or answers 429) and against the bodies being read at once (or answers 503). It reads and validates the request, takes a concurrency slot (or answers 429), and writes the code and the input into a fresh per-run directory.
3. The executor starts nsjail with that directory mounted read-only at `/job` and the arguments on its command line. There is no shell, so arguments are never interpreted.
4. Inside the jail, `image/run` runs the program with `dyalogscript` (below).
5. The executor collects up to the output cap from each stream and keeps draining past it. It enforces the wall-clock limit. Each run has a cgroup of its own below the container's, inside which nsjail creates the limited one; once nsjail has exited, however it exited, the executor kills everything left in that cgroup, waits until it is empty, removes it and the per-run directory, and only then returns the result and frees the slot.

### Execution semantics

`image/run` copies the code into the jail's home directory as a script file, appending a final newline when the code lacks one (`dyalogscript` ignores an unterminated last line). It then runs `dyalogscript <parameters...> <script> <args...>` with the input on stdin, where the parameters are the arguments of the form `NAME=value` and the args are the rest. Observed with Dyalog 20.0 locally, configuration parameters take effect in script mode only there: the environment has no effect on `MAXWS`, and a `#!` line none unless the script is executed as a program. `dyalogscript` expands the parameters unquoted and evaluates them, so each value is passed as ANSI-C hexadecimal escapes, which that evaluation turns back into the value. Observed with Dyalog 20.0 locally; item 2 confirms each point on the current image:

- Statements run in order as a Dyalog script; multi-line dfns are allowed.
- `⍞` and `⎕` read successive lines of the input.
- `⎕PW` is 32767, so wide results are not wrapped.
- An untrapped error writes the error message to stderr, stops the script and ends the run with a non-zero exit status (4 in Dyalog 20.0).
- `⎕OFF n` ends the run with exit status `n`.
- `2⎕NQ#'GetCommandLineArgs'` returns the script name followed by the arguments that are not `NAME=value`.
- An argument `NAME=value` sets the configuration parameter `NAME`, as on TIO, which passes the arguments to the interpreter's command line. `2⎕NQ#'GetEnvironment' 'NAME'` returns it, and parameters read at startup, such as `DEFAULT_IO`, take effect. Dyalog 20.0 does not keep non-ASCII characters in a parameter given on the command line.
- Output lines end with CR.
- `MAXWS` is set below the memory limit, so APL reports `WS FULL` before the cgroup limit is reached. A `MAXWS` argument replaces it, because the interpreter takes the first value of a parameter given twice and the run's parameters come first.
- .NET classes are available through `⎕USING`. The jail mounts the runtime that Dyalog's image installs in `/opt/dotnet` read-only and sets `DOTNET_ROOT`, through which the interpreter finds it. The runtime cannot see the run's cgroup, so `DOTNET_GCHeapHardLimit` limits its heap to a quarter of the memory limit: a .NET allocation past it throws `OutOfMemoryException`, which the program can trap as `EXCEPTION`, before the cgroup limit is reached. `DOTNET_EnableDiagnostics=0` keeps the runtime from creating a diagnostics socket and debugger pipes and from accepting a debugger or profiler.

Programs that rely on TIO's own wrapper (code fixed as a namespace script, unread input lines executed as statements) can behave differently here.

### Sandbox limits

| Requirement | Mechanism (nsjail, cgroups v2) | Default |
|---|---|---|
| No network | new network namespace (only loopback exists) | always on |
| No persistent state | new mount namespace; interpreter and system directories bind-mounted read-only, with `/etc` a copy of the image's own made at build time, so that nothing Docker writes into the container's `/etc` (resolvers, address, name) reaches a run; size-limited tmpfs for `/tmp` and the home directory; per-run directory removed afterwards | always on |
| CPU | cgroup `cpu.max` | 1 CPU |
| Wall-clock time | nsjail time limit, plus a kill from the executor when its context is cancelled | 10 s |
| Memory | cgroup `memory.max`, `memory.swap.max` of 0; `MAXWS` and the .NET heap limit below the limit | 512 MiB; `MAXWS=256M`; .NET heap 128 MiB |
| Disk | tmpfs `size=` per writable mount, counted against the memory limit | 16 MiB each |
| Processes and threads | cgroup `pids.max` | 64 |
| Privileges | unprivileged uid and gid in a new user namespace, no capabilities, nsjail's seccomp-bpf denylist (namespace creation, `mount`, `ptrace`, `bpf`, `keyctl`, `perf_event_open`, `fanotify_init`, sockets other than Unix and IP, IP protocols other than TCP and UDP, `socketpair` other than Unix, the ioctls that load modules, and similar) inside the container's seccomp profile, Docker's default allowlist without i386 or x32 calls, rlimits for core files and open files | always on |
| Output | per-stream cap; truncation reported in the response | 128 KiB |
| Request size | code plus input | 64 KiB |
| Concurrency | global slots; per-client limits on requests in progress, counted from their arrival, and on runs started per minute; request bodies read at once | CPU count; 2; 30; 64 |

Every numeric value is configuration. `runner-api` reads it from environment variables (`SANDBOX_TIMEOUT`, `SANDBOX_MEMORY`, and so on), passed with `docker run -e` or from the compose file. The defaults above apply when a variable is unset.

The executor sits behind a Go interface (`Executor.Run(ctx, Job) (Result, error)`), so the HTTP layer can be tested with a fake executor.

Each run shares the host kernel, and `⎕NA` lets user code make arbitrary system calls. Language-level restrictions on `⎕SH`, `⎕NA`, file functions, Conga or .NET therefore add nothing; isolation comes entirely from the jail, the seccomp policy and the VM.

### API contract

`POST /api/v1/run`

```json
{ "code": "⎕←2+2", "input": "", "args": [] }
```

- `code` is the header, code and footer already joined; it runs as described under Execution semantics.
- Code plus input is limited by the request-size setting. A request may carry at most 16 arguments of at most 1 KiB each.
- Response `200` for every completed run, including APL errors and timeouts:

```json
{
  "stdout": "4\r", "stderr": "", "exitCode": 0, "signal": null,
  "timedOut": false, "stdoutTruncated": false, "stderrTruncated": false,
  "wallTimeMs": 112, "userTimeMs": 60, "sysTimeMs": 20,
  "dyalogVersion": "21.0.55109"
}
```

- `stdout` and `stderr` hold exactly what the interpreter wrote, as UTF-8 strings (invalid byte sequences replaced by U+FFFD). The API does not alter line endings; presentation is the client's concern.
- The request must have `Content-Type: application/json`. A browser sends a cross-origin JSON request only after a CORS preflight, which is never granted, so other web pages cannot make their visitors' browsers start runs.
- The per-client limits treat each IPv4 address, and each IPv6 /64, as one client.
- Errors: `400` malformed request, including anything but whitespace after the JSON value and an argument containing NUL; `408` request not received in full within 15 s; `413` request too large; `415` any other `Content-Type`; `429` no free concurrency slot, or client over its concurrent or per-minute limit; `503` too many request bodies being read, sandbox unavailable, or shutting down.
- A run's slot is freed when the run ends. A response the client has not read within 10 s is abandoned, and a connection idle for 3 minutes is closed; Caddy keeps an idle upstream connection for 2 minutes, so it never sends a request on a connection being closed.
- `GET /api/v1/version` returns `{"dyalogVersion": "...", "imageDigest": "sha256:...", "commit": "...", "timeoutMs": 10000}`: the first three from the image labels, and the run time limit.
- `GET /healthz` returns `200` when the API is serving.
- On `SIGTERM`, `runner-api` stops accepting runs, lets runs in progress finish, then exits.

### Frontend

`web/` is a copy of TIO's frontend from `usr/share/tio.run/` (`index.html`, `frontend.js`, `frontend.css`, fonts, icons, `pako.min.js`), reduced to TIO's `apl-dyalog` language, whose name, link and Hello World program `frontend.js` holds in place of TIO's `languages.json`. TIO's build step (`sbin/tiobuild`) filled `${tio_*}` placeholders with content-hashed file names; the copy uses fixed paths instead.

runner-api serves the page with a Content Security Policy that allows its own origin only, no plugins, no `<base>`, no form submissions and no framing, and with `X-Content-Type-Options: nosniff` on every response. The page has no inline scripts, handlers or styles, so the policy needs no `'unsafe-inline'`: TIO's inline `onclick` attributes are one delegated listener in `frontend.js`.

Kept from TIO's interpreter page:

- The toolbar: Run (Ctrl-Enter; clicking again cancels) and Permalink.
- The language heading with its "Hello World" example.
- Collapsible Header, Code (with TIO's character and byte count), Footer, Input, Arguments, Output and Debug sections.
- The permalink copy wizard and its formats: Plain URL, HTML, Wiki markup, Markdown, Code Golf submission (Stack Exchange) and BBCode.
- TIO's permalink format, stored in the URL fragment. A tio.run `apl-dyalog` permalink opens with all its fields when its host is replaced by this site's; the program then runs under `dyalogscript`, not TIO's wrapper.

Changed:

- The page opens directly on `apl-dyalog`. TIO's home page, language list, search and "switch languages" control are removed. A permalink for another language shows that the language could not be found.
- Input is sent with a line break added at its end, so that its last line can be read with `⍞`.
- Text outside the code fields and the language bar uses APL335; bold text is drawn with a text stroke and letter spacing, since APL335 has one weight.
- Run sends JSON to `/api/v1/run` instead of TIO's compressed `cgi-bin` protocol. Cancelling aborts the request, which ends the run on the server.
- A run whose request gets no response is given up once the run time limit and a margin of 10 s have passed. A response that is not a run result, an error status and a connection failure each end the run with their own message, and leave Output and Debug empty.
- Output shows stdout. Debug shows stderr followed by TIO's timing lines (Real time, User time, Sys. time, CPU share, Exit code), built from the response. Truncation and time-limit notices use TIO's wording.
- TIO's branding, sponsor, donation and social links are removed, as is its unused GitHub sign-in and cache-probe code. The site name `dyalog.run` replaces "Try It Online", including in the permalink formats.

Added:

- APL glyph input through `abrudz/lb`: a language bar and backtick key bindings.
- The Dyalog version, from `/api/v1/version`, in the language heading.
- An APL Wiki button at the right end of the toolbar (hotkey W), which opens the APL Wiki in a new tab.
- A favicon showing the Run button's logo in colours that follow the browser's theme.

### Publishing and updates

- `.github/workflows/publish.yml` runs on every push to `main` and daily. It resolves the digest of `docker.io/dyalog/dyalog:latest`.
- It builds and tests on every push, and on the daily run when the upstream digest differs from the label on the published image or that image is more than six and a half days old (`scripts/needs-build`), so the image's other packages are refreshed at least weekly. `.github/workflows/image.yml` builds the image in one job and saves it as a run artifact; a second job loads it, starts it with the flags in Context on the runner, and runs the integration and UI suites against it.
- If every suite passes, a third job pushes `ghcr.io/abrudz/sandbox:latest` and `ghcr.io/abrudz/sandbox:<dyalog-version>`. It is the only job that can write packages, and it runs nothing from the repository or the suites' dependencies: it loads the artifact and checks its image ID against the build job's before pushing. The build and test jobs have read-only tokens, no checkout keeps its token, and every action is pinned to a commit. If any suite fails, nothing is published and the workflow fails.
- There is no human approval step; the test suites are the gate. The version tags allow manual rollback.
- The local instance updates with `docker pull`. The public instance runs a daily systemd timer that runs `docker compose pull` and `docker compose up -d`. The compose file's stop grace period is longer than the wall-clock limit, so runs in progress finish before the old container exits.

### Deployments

- **Local instance:** any machine with Docker and cgroup v2, including WSL2 with Docker Desktop or Docker Engine. The `docker run` command in Context publishes the port on `127.0.0.1` only, so the instance is reachable from that machine alone.
- **Public instance:** a dedicated Ubuntu 24.04 VM whose only secret is Caddy's TLS key. `deploy/compose.yaml` runs the sandbox container, with the flags in Context, and Caddy, which obtains the TLS certificate for `dyalog.run`, refuses request bodies larger than runner-api accepts, and proxies to the container through a Unix socket in a volume the two share (`SANDBOX_SOCKET`). The sandbox container has no network but loopback (`network_mode: none`): nothing in it connects out, and runs have network namespaces of their own. `deploy/setup-host` sizes the container to the VM: a memory limit of the VM's memory less 512 MiB, with no swap; one run per CPU, or fewer so that every 512 MiB run and 128 MiB for runner-api fit within that limit; and 64 processes per run plus 256. `runner-api` takes the client from `X-Forwarded-For` only on its socket, which is open to root only, for per-client limits. The inbound firewall allows 22, 80 and 443 only, and unattended security upgrades keep the kernel patched. apport is masked and the core pattern names a file, so no crash starts a helper on the host; modprobe refuses the socket protocol and family modules the host does not use, so that no gap in the seccomp policy loads one; and `runner-api` removes job directories left by an earlier process when it starts.

### Repository layout

```
cmd/runner-api/        HTTP server, static file serving, configuration
internal/api/          request validation, JSON contract, concurrency and rate limits
internal/sandbox/      Executor interface and the nsjail implementation
image/                 Containerfile, entrypoint, run (dyalogscript wrapper), nsjail.cfg, seccomp policy
web/                   TIO frontend (adapted), abrudz/lb, fonts
test/integration/      black-box HTTP tests against a running container
test/ui/               Playwright tests against a running container
scripts/               smoke (end-to-end check against a URL)
deploy/                setup-host, compose.yaml, Caddyfile, update timer and service
.github/workflows/     ci.yml (every push), publish.yml (main and daily)
THIRD_PARTY_NOTICES
```

Go 1.27 with standard library HTTP. Linters: `gofmt`, `go vet`, `staticcheck` for Go; `shellcheck` for shell scripts. Node 26 for the UI tests.

## Work breakdown

Before item 1, the first commit on `main` of `abrudz/sandbox` adds the project kit (`CLAUDE.md`, `PROCESS.md`, `.claude/`, `.devcontainer/`), the MIT `LICENSE` and this plan, so issues can link to it.

Each item is one GitHub issue, in dependency order. Go unit tests run anywhere. Integration tests (`go test -tags integration ./test/integration/...`) and UI tests are black-box tests against a running container whose address is in `SANDBOX_URL`; they run in CI only (see Development environment).

**1. Container skeleton and CI**
- Building `image/Containerfile` produces an image, and running it with the flags in Context serves `GET /healthz` with 200.
- The image contains Dyalog's `LICENSE` and `THIRD_PARTY_NOTICES`.
- CI runs `gofmt`, `go vet`, `staticcheck`, `shellcheck` and `go test ./...`, builds the image and runs the integration suite against it on every push. Any failure fails the build.

**2. Run a Dyalog program with dyalogscript**
- `⎕←'Hello, World!'` returns stdout `Hello, World!` followed by the interpreter's line ending, with exit code 0.
- A multi-line dfn defined in the code can be called later in the code.
- Two `⍞` reads return the first and second input lines.
- A result wider than 80 characters prints on one line.
- `1÷0` writes the `DOMAIN ERROR` message to stderr, later lines do not run, and the exit status is non-zero.
- `⎕OFF n` ends with exit status `n`.
- The arguments are visible through `2⎕NQ#'GetCommandLineArgs'`.
- An argument `NAME=value` sets the configuration parameter `NAME`, keeping its value exactly; parameters read at startup and `MAXWS` take effect; such arguments are not program arguments.
- Code whose last line has no trailing newline still runs that line.
- stderr never repeats what was written to stdout.
- The response reports wall, user and system time.
- `/api/v1/version` reports the same Dyalog version that `'.'⎕WG'APLVersion'` reports inside a run.
- A run calls a .NET method through `⎕USING` and finishes in under 3 s; a .NET allocation past the heap limit, a quarter of the memory limit, throws an exception the run can trap; the runtime creates no diagnostics socket.

**3. Limits and cleanup**
- A program that runs past the wall-clock limit is killed, and the response reports `timedOut`.
- Output past the per-stream cap is truncated and flagged, and the run still ends normally.
- Closing the HTTP connection mid-run kills the run within one second.
- After any run, including timeouts and cancellations, no process, cgroup or per-run directory remains.
- Setting a limit in the environment changes the enforced limit: with `SANDBOX_TIMEOUT=2s`, a three-second program times out.

**4. Isolation**
- An outbound TCP connection from inside a run (for example through `⎕SH` and bash's `/dev/tcp`) fails.
- Writing to the root filesystem fails; writing more than the tmpfs limit to `/tmp` or the home directory fails.
- A file written in one run is absent in the next.
- Allocating past `MAXWS` gives `WS FULL`; a child process that allocates past the memory limit is killed, and the service keeps serving.
- A fork bomb through `⎕SH` cannot exceed the process limit, and the run still ends within the wall-clock limit.
- Two busy processes running for two seconds report CPU time of at most about one CPU-second per wall-clock second.
- A blocked system call made through `⎕NA` (for example `unshare`) fails with `EPERM`.
- The program runs as a non-root user with no effective capabilities, and sees only its own processes.
- Two concurrent runs cannot see each other's files or processes.

**5. API hardening**
- Malformed JSON gives 400; an oversized body gives 413; too many arguments gives 400.
- When every concurrency slot is busy, a new request gets 429 immediately.
- A client over its concurrent or per-minute limit gets 429; other clients are unaffected.
- After `SIGTERM`, runs in progress complete, new requests get 503, and the process exits.

**6. Frontend**
- Opening `/` shows the `apl-dyalog` interpreter page with TIO's sections, and no language list.
- Ctrl-Enter runs the program. Output shows stdout; Debug shows stderr and TIO's timing lines, including the exit code.
- Clicking Run during a run cancels it, and the server stops the run.
- Each permalink format is generated, and reopening a generated permalink restores header, code, footer, input and arguments.
- A permalink captured from tio.run for `apl-dyalog` opens with the same fields when its host is replaced.
- A permalink for any other language shows TIO's "language not found" message.
- CR line endings in output display as line breaks.
- APL glyphs can be entered through the language bar and backtick key bindings.
- The page shows the Dyalog version.
- The page makes no requests to any origin other than its own.

**7. Automated publishing**
- A push to `main` publishes `latest` and the version tag only after every suite passes against the built image.
- A daily run with an unchanged upstream digest and a published image under a week old publishes nothing; otherwise it rebuilds, tests and publishes.
- When any suite fails, nothing is published and the workflow fails.
- The published image's labels and `/api/v1/version` report the same upstream digest, Dyalog version and commit.
- Verified with manual `workflow_dispatch` runs; the workflow takes an optional upstream image override, so the failure path can be exercised with a broken image.

**8. Deployment**
- The README documents the local `docker run`; following it gives a working site at `http://localhost:8000` that other machines cannot reach.
- `deploy/setup-host` turns a fresh Ubuntu 24.04 VM into the public instance (Docker Engine, compose, firewall, unattended upgrades, update timer), and rerunning it changes nothing and succeeds.
- After provisioning, `scripts/smoke https://dyalog.run` passes: hello world, a timeout case and a no-network case.
- The sandbox container has no network: root in it cannot connect out or to the VM, nothing listens on port 8080, and the per-client limits still tell apart clients that reach Caddy from different addresses.
- The update timer brings in a newly published image without failing runs in progress.
- Rebooting the VM brings the service and the timer back without manual steps.

## Verification

- In the dev container: `go test ./...`, the linters, and `node --test` for the frontend's pure functions.
- In CI, on every push and pull request (`ci.yml`), after the steps above:

  ```
  docker build -t sandbox -f image/Containerfile .
  docker run -d --rm --cap-drop ALL --cap-add SYS_ADMIN --cap-add SETUID --cap-add SETGID \
    --security-opt apparmor=unconfined --security-opt systempaths=unconfined \
    --security-opt seccomp=deploy/seccomp.json --cgroupns private \
    --read-only --tmpfs /tmp:size=16m --name sandbox-ci -p 127.0.0.1:8000:8080 sandbox
  SANDBOX_URL=http://localhost:8000 go test -tags integration ./test/integration/...
  (cd test/ui && SANDBOX_URL=http://localhost:8000 npx playwright test)
  ```

  Results: `gh run watch` on the branch; `gh run view --log-failed` for failures.
- Manual, by the user on a machine with Docker (not the dev container): the `docker run` command in Context, then `curl -s localhost:8000/api/v1/run -H 'Content-Type: application/json' -d '{"code":"⎕←2+2","input":"","args":[]}'` returns stdout starting with `4`. Open `http://localhost:8000`, enter `⎕←2+2`, press Ctrl-Enter and see `4`. Open a tio.run `apl-dyalog` permalink with its host replaced by `localhost:8000`.

## Out of scope

- More than one Dyalog version, the Classic edition, or any other language, including TIO's `apl-dyalog-classic` and `apl-dyalog-extended`.
- Version pinning. The service always tracks `latest`; version tags exist for manual rollback only.
- TIO's and ATO's backend protocols. Only TIO's UI and permalink format are kept.
- TIO's `apl-dyalog` execution semantics (code fixed as a namespace script, unread input executed).
- TIO's home page, language list, server-side result cache and GitHub sign-in.
- Streaming output, WebSockets, interactive sessions and RIDE.
- User accounts, saved programs or any server-side storage of submissions.
- More than one host, load balancing and pre-warmed jails.
- arm64 images, gVisor, and a slimmed image without .NET and ODBC.
- SBCS byte counting. The Code section counts characters and bytes as TIO does for `apl-dyalog`.

## Open questions

1. VM provider and size for the public `dyalog.run` instance. This does not block any item before item 8.

## Development environment

The dev container has no access to any Docker daemon and cannot create user namespaces, so the image is never built or run inside it. Root in the sandbox container holds `CAP_SYS_ADMIN` on whichever machine runs it, and the developer's machine holds Windows drives and credentials; container work therefore runs only on GitHub Actions runners.

- **In the dev container:** `go test ./...` (unit tests, with the fake executor), `node --test` for the frontend's pure functions, and the linters.
- **In CI** (`ci.yml`, on every push and pull request): building the image, starting it with the flags in Context, and running the integration and UI suites against it. The deployment job provisions a runner with `deploy/setup-host` and that same image, checked by ID, so that `deploy/` and the image are tested together as publishing would release them.
- **Red and green evidence for container tests** is a CI run. In those cycles the tests are committed and pushed to the issue's branch, opened as a draft pull request, before review. Review changes land as further commits on the branch, never as force pushes. Runs are followed with `gh run watch` and failures read with `gh run view --log-failed`.

## Risks

- **Root with `CAP_SYS_ADMIN` in the container.** `runner-api` runs as root inside the container, and AppArmor and Docker's masked paths are off there. A flaw in `runner-api`'s request handling is therefore nearly as serious as a jail escape. Mitigations: a small Go codebase, strict request validation, no shell anywhere in the request path, Docker's capability limits, device filter and seccomp profile, and the dedicated VM.
- **Kernel escape.** Every run shares the host kernel, and `⎕NA` exposes raw system calls. Mitigations: user namespace, no capabilities, seccomp policy, prompt kernel updates on the VM. gVisor is the next step if the threat model demands it.
- **Seccomp policy.** A policy that is too strict breaks the interpreter; one that is too loose leaves attack surface. Item 2 shows the interpreter works under the policy, and item 4 shows blocked calls fail.
- **cgroups inside the container.** nsjail's cgroup limits depend on the entry point enabling controllers in the container's cgroup namespace. The entry point refuses to start outside a private cgroup namespace on cgroup v2, where those changes would reach the host's cgroups.
- **Interpreter output behaviour.** With Dyalog 20.0, `dyalogscript` echoes input read by `⍞` to stderr, so the Debug pane shows it. In one local test where stdout went to `/dev/null`, stderr also repeated the stdout content. Item 2 pins stderr's content on the current image.
- **Slow feedback on container tests.** Every red or green check of a container test costs a CI run: building the image (including nsjail from source) and running the suites. Layer caching in `ci.yml` (GitHub Actions cache for the nsjail and Go build stages) keeps reruns short, and the unit tests with the fake executor cover the HTTP layer locally.
- **Unreviewed upstream changes.** Tracking `latest` without review means a new release, including a new major version, goes live as soon as it passes the suites. The suites catch breakage in the sandbox contract and the `dyalogscript` behaviour above, but not subtle changes in APL semantics. Version tags allow rollback.
