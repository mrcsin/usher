# usher

Container that reads `config/usher.yml`, a map of user name to interface names, and keeps the
AmneziaWG peers, the Xray users and the client files in `clients/` in step with it through the
`awg-grpc` and Xray API unix sockets; `README.md` describes it. Go module `github.com/mrcsin/usher`; entry point
`cmd/usher`. All commands run from the repository root.

## Build

```sh
go build ./...
docker build --build-arg VERSION=dev -t usher:local .
```

| Build argument | Meaning                                     |
| -------------- | ------------------------------------------- |
| `VERSION`      | the version that `usher run` logs at start  |

## Test

```sh
go test -race ./...
```

Table-driven tests sit beside the source as `*_test.go`; filesystem tests use `t.TempDir()`. The
`awg-grpc` API is faked by a struct with func fields that implements
`awgv1.ManagementServiceClient`; the Xray API by a struct that implements
`command.HandlerServiceClient`.

The e2e suite in `test/e2e` carries `//go:build e2e`. It needs Docker and a host with the
`amneziawg` module loaded, so it runs on a Linux host, not on macOS. It uses the published
`awg-grpc` image, builds usher from the tree and runs the production interval constants:

```sh
GOOS=linux GOARCH=amd64 go test -c -tags e2e -o e2e.test ./test/e2e
# copy the tree and e2e.test to the host, then from test/e2e on the host:
./e2e.test -test.count=1 -test.v
```

The suite needs `docker compose` without `sudo`. It wipes and recreates the bind-mount directories
under `.e2e/` (gitignored) at start, and passes `E2E_UID` and `E2E_GID` to compose so usher
writes them as the test user.

The Xray suite in `test/e2e/xray` (`deploy/compose.e2e-xray.yml`) needs only Docker and no kernel
module. The test generates a CA, the server certificates and the Reality key, and starts the
official Xray image. It also kills Xray's process from a helper container in its PID namespace,
because `docker kill` bypasses the restart policy:

```sh
go test -tags e2e -count=1 -v ./test/e2e/xray/
```

It uses `.e2e-xray/` (gitignored) for its bind mounts. CI runs it in the `e2e` job.

To copy the tree from macOS to a Linux host, use `COPYFILE_DISABLE=1 tar --no-xattrs`: plain macOS
tar adds `._*` AppleDouble files. A run killed by a signal leaves containers up; remove them with:

```sh
docker compose -f deploy/compose.e2e.yml --profile handshake down -v --remove-orphans
```

## Format

```sh
scripts/format.sh
```

Run before presenting changes. It checks gofmt and runs `go vet` without tags and with the `e2e`
tag. `gofmt -w .` fixes formatting. The pre-commit hook and the CI `check` job run the same script.

After a change to a shell script or a workflow, also run:

```sh
shellcheck scripts/*.sh scripts/ci/*.sh scripts/git-hooks/pre-commit
go run github.com/rhysd/actionlint/cmd/actionlint@latest
```

Enable the pre-commit hook per clone: `git config core.hooksPath scripts/git-hooks`.

## CI

`.github/workflows/ci.yml` runs on every pull request and every push to `master`.

- `check`: `scripts/format.sh`, `go test -race`, shellcheck.
- `e2e`: loads the pinned kernel module with `scripts/ci/module.sh`, then runs the e2e suite.

The module pin is `AWG_MODULE_REF` in `ci.yml`.

`.github/workflows/release.yml` runs on a `v*` tag push: it builds the runtime image with
`VERSION` set to the tag, pushes `ghcr.io/mrcsin/usher:<tag>` and creates the GitHub release with
the notes of the annotated tag.

## Conventions

- A private key, a preshared key or a `client_params` value is never logged, and an error message
  never quotes one. A public key without an entry is logged as base64.
- A Reality `privateKey` or `mldsa65Seed`, a `certificateFile` or `keyFile`, a PEM key, `sockopt`,
  a server `fallbacks` entry and a drift value never reach `clients/`, a log line or an error. A
  drift reason names the differing part only.
- usher never writes the Xray server config. It owns the users of the managed inbounds and
  `clients/`.
- `state/users.json` is the only source of secrets. The kernel and `clients/` derive from it, and
  usher saves it before `ApplyPeers`.
- A failure of one interface never stops the daemon; the pass logs it and goes on.
- No interface without a second implementation, no exported surface only for tests.

## Layout

```
cmd/usher/             entry point: environment, signals, run loop wiring
internal/config/       usher.yml loader and validation
internal/state/        users.json load and save
internal/atomicfile/   atomic file replace shared by state and the clients mirror
internal/pass/         settings, the Backend/Session seam, the reconcile pass
internal/awg/          awg backend: enrollment, address allocation, .conf rendering
internal/xray/         Xray backend: config reader, drift guard, users, mirror, rendering, stale socket
internal/clients/      client file paths and the clients/ mirror
internal/watch/        file poll, refill ticker and the trigger loop
deploy/                example compose file, example Xray config, e2e compose files
test/e2e/              awg end-to-end test through docker compose (tag e2e)
test/e2e/xray/         Xray end-to-end test (tag e2e)
scripts/format.sh      the Format chain (see Format)
scripts/git-hooks/     pre-commit hook: branch guard, then scripts/format.sh
scripts/ci/module.sh   builds and loads the pinned kernel module in the CI e2e job
.github/workflows/     ci.yml and release.yml
Dockerfile             Go build stage and the runtime image
```

## Docs

Stable design is in `docs/architecture/`, starting at `overview.md`. Dated plans are in
`docs/plans/`, completed ones in `docs/plans/completed/`.
