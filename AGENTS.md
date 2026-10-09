# usher

Container that reads `config/usher.yml`, a map of user name to interface names, and keeps the
peers of AmneziaWG interfaces, the users of Xray VLESS inbounds and the client files in `clients/`
in step with it through the `awg-grpc` and Xray API unix sockets; `README.md` describes it. Go
module `github.com/mrcsin/usher`; entry point `cmd/usher`. All commands run from the repository
root.

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
`awgv1.ManagementServiceClient`; the Xray API is faked the same way for
`command.HandlerServiceClient`.

The e2e suite in `test/e2e` carries `//go:build e2e`. It needs Docker and a host with the
`amneziawg` module loaded, so it runs on a Linux host, not on macOS. It uses the published
`awg-grpc` image, builds usher from the tree and runs the production interval constants:

```sh
GOOS=linux GOARCH=amd64 go test -c -tags e2e -o e2e.test ./test/e2e
# copy the tree and e2e.test to the host, then from test/e2e on the host:
./e2e.test -test.count=1 -test.v
```

The Xray suite in `test/e2e/xray` carries the same tag and needs Docker only, no kernel module. It
has its own `TestMain`, compose project
(`deploy/compose.e2e-xray.yml`, network `172.30.98.0/24`) and scratch root `.e2e-xray/`
(gitignored). It starts the official Xray image with a self-signed nginx as the Reality target,
builds usher from the tree and drives two users through client containers built from the rendered
links. The server config allows the target address in the freedom outbound `finalRules`, because
Xray 26.x blocks private destinations for VLESS by default. Run it with `-count=1`:

```sh
go test -tags e2e -count=1 -v ./test/e2e/xray/
```

Both suites need `docker compose` without `sudo`. Each wipes and recreates its own bind-mount root
at start (`.e2e/`, `.e2e-xray/`, both gitignored) and passes `E2E_UID` and `E2E_GID` to compose so
usher writes them as the test user.

To copy the tree from macOS to a Linux host, use `COPYFILE_DISABLE=1 tar --no-xattrs`: plain macOS
tar adds `._*` AppleDouble files. A run killed by a signal leaves containers up; remove them with:

```sh
docker compose -f deploy/compose.e2e.yml --profile handshake down -v --remove-orphans
docker compose -f deploy/compose.e2e-xray.yml --profile client down -v --remove-orphans
```

## Generate

`gen/xray` comes from `proto/xray`; edit the protos only to refresh them (see
`proto/xray/README.md`). Regenerate with:

```sh
go tool -modfile=tools/go.mod buf generate
```

`scripts/format.sh` fails when `gen/xray` differs from the output of that command.

## Format

```sh
scripts/format.sh
```

Run before presenting changes. It checks gofmt, runs `go vet` without tags and with the `e2e`
tag, and checks that `gen/xray` equals the output of `buf generate`. `gofmt -w .` fixes formatting. The pre-commit hook and the CI `check` job run the same script.

After a change to a shell script or a workflow, also run:

```sh
shellcheck scripts/*.sh scripts/ci/*.sh scripts/git-hooks/pre-commit
go run github.com/rhysd/actionlint/cmd/actionlint@latest
```

Enable the pre-commit hook per clone: `git config core.hooksPath scripts/git-hooks`.

## CI

`.github/workflows/ci.yml` runs on every pull request and every push to `master`.

- `check`: `scripts/format.sh`, `go test -race`, shellcheck.
- `e2e`: loads the pinned kernel module with `scripts/ci/module.sh`, then runs the e2e suite and
  the Xray e2e suite.

The module pin is `AWG_MODULE_REF` in `ci.yml`.

`.github/workflows/release.yml` runs on a `v*` tag push: it builds the runtime image with
`VERSION` set to the tag, pushes `ghcr.io/mrcsin/usher:<tag>` and creates the GitHub release with
the notes of the annotated tag.

## Conventions

- A private key, a preshared key, a `client_params` value, an Xray UUID or a Reality private key is
  never logged, and an error message never quotes one. A public key without an entry is logged as
  base64.
- `state/users.json` is the only source of secrets. The kernel, Xray and `clients/` derive from it,
  and usher saves it before the first `Apply` of any backend (`ApplyPeers` for awg,
  `AlterInbound` for Xray).
- A failure of one interface never stops the daemon; the pass logs it and goes on.
- No interface without a second implementation, no exported surface only for tests.

## Layout

```
cmd/usher/             entry point: environment, signals, run loop wiring
internal/config/       usher.yml loader and validation
internal/state/        users.json load and save
internal/atomicfile/   atomic file replace shared by state and the clients mirror
internal/pass/         settings, the Session and Backend seam, the reconcile pass
internal/awg/          AmneziaWG backend: enrollment, address allocation, config rendering, apply
internal/xray/         Xray backend: decoder tables, enrollment, user reconcile, vless:// links
internal/clients/      the clients/ mirror
internal/watch/        file poll, refill ticker and the trigger loop
deploy/                example compose file and Xray config (deploy/xray), e2e compose files
                       of both suites, e2e fixtures (deploy/e2e)
proto/xray/            vendored Xray protos (see proto/xray/README.md)
gen/xray/              Go code generated from proto/xray, committed
tools/                 separate Go module that pins buf and the protobuf plugins
test/e2e/              end-to-end test of AmneziaWG through docker compose (tag e2e)
test/e2e/xray/         end-to-end test of Xray through docker compose (tag e2e)
scripts/format.sh      the Format chain (see Format), including the gen/xray check
scripts/git-hooks/     pre-commit hook: branch guard, then scripts/format.sh
scripts/ci/module.sh   builds and loads the pinned kernel module in the CI e2e job
.github/workflows/     ci.yml and release.yml
Dockerfile             Go build stage and the runtime image
```

## Adding an Xray inbound shape

Add one entry to `protocols`, `transports` or `securities` in `internal/xray`, vendor the proto
that describes its settings into `proto/xray`, run the generate command and write the entry's
tests. A protocol whose credential is not a UUID also needs a field in `state.XrayEntry` and its
own enrollment. `docs/architecture/overview.md`, "The Xray backend", has the details.

## Docs

Stable design is in `docs/architecture/`, starting at `overview.md`. Dated plans are in
`docs/plans/`, completed ones in `docs/plans/completed/`.
