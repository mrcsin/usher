# Xray driver

## Overview

- usher gains a second backend: Xray. A delivered Xray container exposes its own gRPC API on a
  unix socket; usher reads the VLESS inbounds from it, keeps one UUID per user and inbound in
  `users.json`, adds and removes users through `HandlerService`, and writes one `vless://` link
  per user and inbound into `clients/`.
- usher still creates no interface and starts no process. Delivery prepares each VPN container;
  usher only moves users into it.
- Backends are selected explicitly: `USHER_AWG_SOCKET` and `USHER_XRAY_SOCKET` name the socket
  paths. A set variable enables the backend; at least one must be set.
- Breaking changes: an existing deployment must add `USHER_AWG_SOCKET` to its compose file, and
  `users.json` moves to version 2, which the previous image refuses to load. The release is
  marked with `!`.
- Scope of the Xray driver: VLESS over raw TCP without a header, with Reality security and
  `decryption: none`. Any other security, transport, header, decryption or a port range fails
  that inbound with an error that names the field. The driver is laid out so that more shapes
  can be added later (Solution Overview, "Room for more inbound shapes").

## Context (from discovery)

- `cmd/usher/main.go:22` hardcodes the awg socket; `main.go:57-63` dials it; `main_test.go:59-75`
  (`TestServe`) calls `serve` with a `pass.Dial`.
- `cmd/usher/env.go:17-39` reads `USHER_HOST` and `USHER_DNS`, both required.
- `internal/pass/pass.go:25` types `Dial` on `awgv1.ManagementServiceClient`; `pass.go:42-119` is
  the pass: `GetStatus`, prepare per interface, save state, apply per interface, mirror
  `clients/`. A dial or `GetStatus` error returns before the mirror (`pass.go:52-65`), so
  `clients/` stays untouched. `pass.go:136-138` fails an interface that is not present in the
  kernel. `pass.go:211` logs "interface is not reported by awg-grpc".
- `internal/pass/enroll.go:23` enrolls AmneziaWG entries (key pair, PSK, address).
- `internal/clients/render.go:18` renders an AmneziaWG config from `awgv1.InterfaceStatus`;
  `render.go:1-2` holds the package doc comment of `clients`; the golden file is
  `internal/clients/testdata/phone.conf`.
- `internal/clients/mirror.go:22-24` hardcodes the `.conf` suffix in `Path`; `mirror.go:48`
  keeps only `.conf` files in `Existing`, which preserves files of failed interfaces.
- `internal/state/state.go:53-57` is the AmneziaWG entry; `state.go:71` documents `EntryName` as
  a key "in State.AWG"; `state.go:77-80` holds only the `awg` group; `state.go:107` rejects every
  version other than 1.
- `internal/config/config.go:13-14`: user names `^[a-z0-9][a-z0-9_-]{0,31}$` (lowercase only),
  interface names `^[A-Za-z0-9_][A-Za-z0-9_=+.-]{0,14}$`; `config.go:55-57` rejects a file
  without any user and interface pair.
- `Dockerfile:8-9` copies only `cmd` and `internal` into the build stage; awg-grpc copies `gen`
  as well (`awg-grpc/Dockerfile:23`).
- `deploy/compose.example.yml:39-41` and `deploy/compose.e2e.yml:35-37` set only `USHER_HOST` and
  `USHER_DNS`; the awg e2e network is `172.30.99.0/24` (`compose.e2e.yml:58`).
- `test/e2e/e2e_test.go:69-107` is a package-wide `TestMain` that starts the awg compose project
  and needs the `amneziawg` module; `e2e_test.go:24,406` uses `state.Entry`, `state.EntryName`
  and `Entry.Route`.
- `.github/workflows/ci.yml:62` runs `go test -tags e2e -count=1 -v ./test/e2e/`.
- `docs/architecture/overview.md:62-76` holds the locked decisions; lines 64, 65, 70, 71, 74 and
  76 are worded for AmneziaWG only.
- `go.mod` already requires `google.golang.org/grpc v1.84.0`; Go is 1.27.1, whose standard
  library has package `uuid` (`uuid.NewV4`, `uuid.Parse`, verified with `go doc uuid`).
- awg-grpc generates code with buf from a separate tools module: `awg-grpc/buf.gen.yaml:1-8`,
  `awg-grpc/tools/go.mod:5-9`; its CI caches `tools/go.sum` (`awg-grpc/.github/workflows/ci.yml:33-35`);
  its `scripts/format.sh:26` runs `buf lint`.

Xray-core v26.9.9, read during planning:

- `app/proxyman/command/command.proto:91,93,95`: `AlterInbound`, `ListInbounds`,
  `GetInboundUsers`; `command.proto:13-19`: `AddUserOperation{User}`,
  `RemoveUserOperation{email}`; `command.proto:41`: `ListInboundsRequest.isOnlyTags`.
  `ListInbounds` with `isOnlyTags=false` returns the stored receiver and proxy configs
  (`app/proxyman/command/command.go:113-118`, `app/proxyman/inbound/always.go:212-221`).
- `core/config.proto:35-42`: `InboundHandlerConfig{tag, receiver_settings, proxy_settings}`, both
  settings as `TypedMessage` (`common/serial/typed_message.proto:10-14`: `type`, `value`).
  `TypedMessage.type` is the message's full proto name without a URL prefix
  (`common/serial/typed_message.go:22-24`).
- `app/proxyman/config.proto:37-42`: `ReceiverConfig{port_list, listen, stream_settings}`;
  `common/net/port.proto:10-19`: `PortList` of `PortRange{From, To}`.
- `transport/internet/config.proto:44-57`: `StreamConfig{protocol_name, security_type,
  security_settings}`. The config builder normalizes both `raw` and `tcp` to `tcp`
  (`infra/conf/transport_internet.go:18-19`). With `security: none` the `security_type` is empty.
- Raw TCP has three header forms: no transport settings at all, `tcp.Config` without
  `header_settings` (`transport/internet/tcp/config.proto:11-15`), and a `header_settings` from
  `header: {type: "none"}`, which the builder loads as the no-op authenticator
  (`infra/conf/transport_method.go:225-228,237-249`; package `xray.transport.internet.headers.noop`).
- `transport/internet/reality/config.proto:14-21`: `server_names`, `private_key`, `short_ids`,
  `mldsa65_seed`. The builder rejects an empty `serverNames` and an empty `shortIds`, decodes the
  private key with `base64.RawURLEncoding`, and stores every short ID hex-decoded into 8 bytes, so
  `""` becomes 8 zero bytes and `"ab"` becomes `ab00000000000000`
  (`infra/conf/transport_security.go:95-100,135-145`). `xray x25519` prints keys in the same
  encoding by default (`main/commands/all/x25519.go:8,15`).
- `transport/internet/reality/reality.go:84-101`: the client authenticates the server by a
  signature made with the Reality key, not by the target's certificate, and runs the ML-DSA-65
  check only when its `mldsa65Verify` is set. A link without `pqv` works against a server that has
  a `mldsa65_seed`, and a self-signed target certificate works.
- `proxy/vless/inbound/config.proto:20-24`: `users`, `decryption`; the builder stores the literal
  `none` (`infra/conf/vless.go:107,150`). The runtime config has no inbound-level `flow`:
  `infra/conf/vless.go` writes the inbound `flow` into each client while it builds the config,
  and only there.
- `proxy/vless/account.proto:16-22`: `Account{id, flow, encryption}`;
  `common/protocol/user.proto:12-18`: `User{level, email, account}`.
- `proxy/vless/inbound/inbound.go:245-247` and `proxy/vless/validator.go:47`: `RemoveUser` removes
  the user's reverse proxy and deletes it from the validator; live connections stay open. The
  validator's errors quote the email, not the id (`validator.go:39,54`).
- `proxy/vless/inbound/inbound.go:589`: a user whose account `flow` differs from the client's
  requested flow is rejected with "account ... is not able to use the flow".
- `proxy/vless/validator.go:21-25`: `ProcessUUID` zeroes bytes 6-7 of the UUID, so only a random
  (v4) UUID keeps enough entropy.
- `proxy/proxy.go:306-307`: an empty `testseed` falls back to `{900, 500, 900, 256}`.
- `app/commander/commander.go:78-90`: `api.listen` accepts a unix path;
  `transport/internet/system_listener.go:128-161`: a `path,0660` suffix sets the socket mode but
  not its owner, so the socket gets the Xray process's uid and gid.
- `GetInboundUsers` first appears in v24.11.5 (absent in v24.10.31, present in v24.11.5, checked
  per tag).
- The proto import closure of the messages above is 12 files, about 690 lines;
  `transport/internet/tcp/config.proto` adds the raw-TCP header settings as a 13th. xray-core is
  MPL-2.0.
- Xray's share-link format is a public proposal, XTLS/Xray-core discussion #716.

## Development Approach

- **testing approach**: Regular (code first, then tests)
- complete each task fully before moving to the next
- make small, focused changes
- **CRITICAL: every task MUST include new/updated tests** for code changes in that task
  - tests are not optional - they are a required part of the checklist
  - write unit tests for new functions/methods
  - write unit tests for modified functions/methods
  - add new test cases for new code paths
  - update existing test cases if behavior changes
  - tests cover both success and error scenarios
- **CRITICAL: all tests must pass before starting next task** - no exceptions
- **CRITICAL: update this plan file when scope changes during implementation**
- run tests after each change
- backward compatibility: kept for `usher.yml` and for reading `users.json` version 1; broken for
  the environment and for rolling back to the previous image (see Overview)

## Testing Strategy

- **unit tests**: table-driven, beside the source; the gRPC APIs are faked by structs with func
  fields, as `awgv1.ManagementServiceClient` already is (`AGENTS.md`, Test).
- **e2e tests**: tag `e2e`. The awg suite in `test/e2e` keeps running unchanged through every
  task. The Xray suite is its own package, `test/e2e/xray`, with its own `TestMain`, compose
  project, network and scratch root, and needs Docker only, no kernel module.
- **leak tests**: usher's own logs and errors never contain a Reality private key, a UUID or a
  preshared key. Backend error text is logged as the backend returns it; Xray's API errors quote
  emails, not ids (`validator.go:39,54`).

## Acceptance Evidence

There is no Xray support today: `usher.yml` naming an Xray inbound tag logs "interface is not
reported by awg-grpc" (`pass.go:211`) and nothing reaches Xray.

1. Unit tests: `go test -race -count=1 ./...` passes.
2. Format chain, including the generated-code check: `scripts/format.sh` exits 0.
3. Xray e2e: `go test -tags e2e -count=1 -v ./test/e2e/xray/` passes with three assertions on a
   config of two users:
   - an HTTP request through a client built from a rendered link returns 200;
   - after one user is removed from `usher.yml`, a new connection through that user's link fails
     while the other user's link still works;
   - after `docker compose restart xray`, a new connection succeeds again within 60 s (refill).
4. awg e2e: `go test -tags e2e -count=1 -v ./test/e2e/` still passes on a host with the
   `amneziawg` module.
5. Manual smoke, once: import one rendered link into a phone client (v2rayNG or Streisand) and open
   a page through it.

## Progress Tracking

- mark completed items with `[x]` immediately when done
- add newly discovered tasks with ➕ prefix
- document issues/blockers with ⚠️ prefix
- update plan if implementation deviates from original scope
- keep plan in sync with actual work done

## Solution Overview

- **Backend seam.** The pass gets a list of backends. Each pass opens one session per backend;
  opening connects and lists the backend's interfaces. A session prepares one interface (enroll
  entries, render client files) and applies one interface (send the desired users). The pass
  keeps everything else: config load, state save before any apply, failure isolation per
  interface, the `clients/` mirror. The awg driver forwards the desired list to `ApplyPeers` and
  fails an interface that is not present in the kernel in its own prepare step; the Xray driver
  computes the difference itself. `internal/xray` is the second implementation of the session
  interface; between Task 2 and Task 6 the interface has one implementation, which the second one
  completes in the same change set.
- **A backend that is down.** When opening a session fails, the pass logs it, applies the other
  backends, keeps every client file with that backend's suffix, and skips the unknown-name log
  for the pass, because the down backend's names are unknown.
- **Ownership.** usher owns every interface a backend reports, as today. For Xray that is every
  VLESS inbound; other inbounds, such as the `api` tunnel, are not reported. An unreferenced VLESS
  inbound gets an empty user list.
- **Name collisions.** A name reported by two backends fails in both, with an error that names
  both backends.
- **Link parameters come from the running Xray.** `ListInbounds` returns the inbound's receiver
  and Reality settings, including the private key. The driver derives the public key, uses it for
  the link and keeps nothing.
- **Flow is usher's choice, not a read value.** The protocol entry decides it from the transport
  and security keys and writes the same value into the account and into the link:
  `xtls-rprx-vision` for VLESS with Reality over raw TCP. usher sends no `testseed`; Xray then uses
  its default (`proxy/proxy.go:306-307`).
- **Switching off.** Removing a name removes the Xray user; new connections fail, live
  connections stay until they close or reach the Xray `connIdle` timeout. The README states this
  difference from AmneziaWG.
- **Room for more inbound shapes.** The Xray driver reads an inbound in three independent parts:
  the protocol (proxy settings), the transport (stream `protocol_name` and its settings) and the
  security (security settings). Each part is a table of decoder functions keyed by the proto type
  name or the transport name, with one entry today: VLESS, raw TCP without a header, Reality. A
  key without an entry fails the inbound with an error that names it. The protocol entry owns
  everything protocol-specific: the flow, the account it builds from an entry, and the whole link,
  so a protocol whose link is not scheme plus query (VMess) still fits. Reconcile compares
  accounts as opaque messages and knows no protocol fields. Adding TLS or another transport adds
  one table entry, its vendored proto and its tests. Adding a protocol adds the same, plus a state
  field when its credential is not a UUID.
- **Vendored protos.** The 13 Xray proto files are copied at v26.9.9 and generated with buf into
  `gen/xray`, so usher imports no xray-core Go code.

## Technical Details

- **Seam sketch** (`internal/pass`):
  ```go
  type Session interface {
      Interfaces() []string
      Prepare(st *state.State, name string, users []string) (files map[string][]byte, changed bool, err error)
      Apply(ctx context.Context, st *state.State, name string, users []string) error
      Close()
  }
  type Backend struct {
      Name   string // "awg-grpc", "xray"; used in logs
      Suffix string // client file suffix: ".conf", ".txt"
      Open   func(ctx context.Context) (Session, error)
  }
  ```
  `cmd/usher` builds each backend's `Open` as a closure over its socket path and the server facts
  it needs (`USHER_HOST` for both, `USHER_DNS` for awg). `pass.Settings` keeps the paths only.
- **Environment.** `USHER_AWG_SOCKET` and `USHER_XRAY_SOCKET` are absolute unix socket paths. At
  least one must be set. `USHER_HOST` stays required; `USHER_DNS` is required only with
  `USHER_AWG_SOCKET`.
- **State, version 2.**
  ```json
  {
    "version": 2,
    "awg":  { "awg0/alice": { "address": "...", "private_key": "...", "preshared_key": "..." } },
    "xray": { "vless-reality/alice": { "id": "5f0c...-..." } }
  }
  ```
  Version 1 loads with an empty `xray` group; Save always writes 2. A zero UUID is invalid. usher
  never re-keys an existing entry, so a seeded UUID survives every pass.
- **Xray user identity.** `email` is the entry name `tag/user`, `level` is 0, the account is what
  the protocol entry builds (`vless.Account{id, flow}`). User names are lowercase by
  `config.go:13`, which matches Xray's case-insensitive email handling. New ids come from
  `uuid.NewV4()`.
- **Decoder tables** (`internal/xray`), plain function types, no interfaces:
  ```go
  type protocol struct {
      account func(e state.XrayEntry) *serial.TypedMessage
      link    func(e state.XrayEntry, user string, host netip.Addr, port uint16, params url.Values) string
  }

  var protocols  = map[string]func(proxy []byte, transport, security string) (protocol, error) // by proxy_settings.type
  var transports = map[string]func(stream *internet.StreamConfig) (url.Values, error)            // by protocol_name
  var securities = map[string]func(settings []byte) (url.Values, error)                         // by security_type
  ```
  An empty `security_type` is named `none` in errors.
- **Inbound reading.** `ListInbounds{isOnlyTags: false}`, then per inbound:
  - `proxy_settings.type` must have an entry in `protocols`; an inbound without one is not an
    usher interface and is skipped silently. The VLESS entry requires `decryption` `none` and sets
    the flow.
  - The receiver port list must hold one range with `From == To`.
  - The `tcp` transport entry accepts all three header forms of raw TCP and fails any other
    header. ASSUMPTION: the no-op header arrives as a message of package
    `xray.transport.internet.headers.noop`; the entry accepts any message of that package.
  - The Reality security entry gives `pbk`, the X25519 public key of `private_key`
    (`crypto/ecdh`), unpadded URL-safe base64; `sid`, the lowercase hex of the first short ID with
    trailing zero bytes trimmed, so an all-zero ID gives an empty `sid`; `sni`, the first non-empty
    server name, failing the inbound when there is none. `mldsa65_seed` is ignored and the link
    carries no `pqv`.
- **Reconcile.** `GetInboundUsers{tag}` gives the current users. A current email that is not
  desired, or whose account differs from the desired one (same `TypedMessage.type` and
  `proto.Equal` of the decoded messages), is removed; a desired email that is missing, or was
  removed for a difference, is added. Each operation is one `AlterInbound`. An error fails the
  inbound; the next pass converges a partial change. The log line is `users changed` with
  `added`, `removed`, `updated` user names, like `peers changed` at `pass.go:179`.
- **Link file.** `clients/<user>/<tag>.txt`, mode 0600, one line built by the protocol entry:
  `vless://<id>@<USHER_HOST>:<port>?<params>#<user>`, where `params` merges the protocol's
  `encryption=none` and `flow`, the transport's `type=tcp` and the security's `security=reality`,
  `sni`, `fp=chrome`, `pbk`, `sid`, encoded by `url.Values.Encode`.
  ASSUMPTION: these parameter names and values are the share-link format phone clients import;
  Task 7 pins them against the public proposal.
- **Client file suffix per backend.** `clients.Path` and `clients.Existing` take the backend's
  suffix. The pass calls `Existing` once per backend and merges the results: for a failed
  interface it keeps that interface's files, for a backend that is down it keeps all of the
  backend's files.
- **Delivery contract for the Xray container** (README): Xray v24.11.5 or later; `api.listen` on a
  unix path with `,0660`; the Xray process gid equal to usher's group (`user: "0:1000"` in
  compose, the counterpart of `AWG_GRPC_SOCKET_GID`); `api.services` limited to
  `HandlerService`; VLESS inbounds start with `clients: []`; inbound tags match the usher
  interface name pattern; the operator removes a stale socket file after a hard kill, because the
  official image has no shell.

## What Goes Where

- **Implementation Steps** (`[ ]` checkboxes): code, tests, generated code, deploy examples and
  documentation in this repository.
- **Post-Completion** (no checkboxes): the manual link check, the release.

## Implementation Steps

### Task 1: Read the awg socket path from the environment

**Files:**
- Modify: `cmd/usher/env.go`
- Modify: `cmd/usher/main.go`
- Modify: `deploy/compose.example.yml`
- Modify: `deploy/compose.e2e.yml`
- Modify: `cmd/usher/env_test.go`
- Modify: `cmd/usher/main_test.go`

- [x] read `USHER_AWG_SOCKET` in `cmd/usher`, required and absolute
- [x] dial that path instead of `socketTarget` (`main.go:22,57-63`)
- [x] add `USHER_AWG_SOCKET: /run/awg-grpc/awg.sock` to `compose.example.yml:39-41` and
      `compose.e2e.yml:35-37`
- [x] write tests: variable set, unset (error), relative path (error)
- [x] run `go test -race ./...` and the awg e2e - must pass before Task 2

### Task 2: Introduce the backend seam with the awg driver

**Files:**
- Create: `internal/awg/` (driver session, enroll, render, apply), moved from `internal/pass` and
  `internal/clients/render.go`
- Move: `internal/clients/testdata/phone.conf` with `render_test.go`
- Modify: `internal/pass/pass.go`, `internal/pass/settings.go`
- Modify: `internal/clients/mirror.go` (takes the package doc comment from `render.go:1-2`)
- Modify: `cmd/usher/main.go`, `cmd/usher/main_test.go` (`TestServe`, `main_test.go:59-75`)
- Modify: tests of the moved code (`internal/pass/enroll_test.go`, `internal/pass/fake_test.go`,
  `internal/clients/render_test.go`, `internal/clients/mirror_test.go`)
- Modify: `internal/pass/pass_test.go` (cases for the seam)

- [x] add `Session` and `Backend` as in the seam sketch; the awg driver implements `Session` with
      today's behavior from `pass.go:52-101`, `pass.go:136-138`, `enroll.go:23` and `render.go:18`,
      and gets `USHER_HOST` and `USHER_DNS` from its constructor instead of `pass.Settings`
- [x] make the pass open one session per backend, fail a name reported by two backends in both,
      and handle a backend that is down as in Solution Overview
- [x] give `clients.Path` and `clients.Existing` a suffix argument (`mirror.go:22,48`) and a way to
      keep all files of one suffix
- [x] reword `pass.go:211` to "interface is not reported by any backend"
- [x] move the existing awg tests with the code; they pass unchanged in substance
- [x] write tests for the seam with two fake backends: both apply; a name collision; a failed
      interface of one backend leaving the other applied and keeping its files; one backend down
      while the other applies and the first one's files stay; no unknown-name log while a backend
      is down
- [x] run `go build ./...`, `go vet ./...`, `go test -race ./...` and the awg e2e - must pass
      before Task 3

### Task 3: Add the xray group to users.json

**Files:**
- Modify: `internal/state/state.go`
- Modify: `internal/state/state_test.go`

- [x] add `Xray map[string]XrayEntry` with `json:"xray"`; `XrayEntry{ID uuid.UUID}`
- [x] bump `version` to 2; `Load` accepts 1 and 2 (`state.go:107`), `Save` writes 2
- [x] validate xray entry names as `tag/user` and reject a zero UUID; errors never quote the UUID
- [x] reword the doc comment at `state.go:71`; keep `Entry`, `EntryName` and `Entry.Route`, which
      `test/e2e/e2e_test.go:24,406` uses
- [x] write tests: load version 1 (empty `xray`), load version 2, reject version 3, reject a zero
      UUID, save and load round trip, no UUID in any error text
- [x] run `go test -race ./...` - must pass before Task 4

### Task 4: Vendor the Xray protos and generate code

**Files:**
- Create: `proto/xray/` (13 files from Xray-core v26.9.9, original relative paths kept)
- Create: `proto/xray/README.md` (source tag, MPL-2.0 notice, refresh steps)
- Create: `buf.yaml`, `buf.gen.yaml`, `tools/go.mod`, `tools/go.sum`
- Create: `gen/xray/...` (generated, committed)
- Modify: `go.mod`, `go.sum` (`google.golang.org/protobuf` becomes a direct requirement)
- Modify: `Dockerfile` (`COPY gen ./gen` next to `Dockerfile:8-9`)
- Modify: `scripts/format.sh`
- Modify: `.github/workflows/ci.yml` (`tools/go.sum` in `cache-dependency-path`)

- [x] copy the closure of `command.proto`, `app/proxyman/config.proto`,
      `proxy/vless/inbound/config.proto`, `proxy/vless/account.proto`,
      `transport/internet/reality/config.proto`, `transport/internet/config.proto` and
      `transport/internet/tcp/config.proto`
- [x] `buf.yaml` with the module at `proto/xray`; `buf.gen.yaml` with `out: gen/xray`,
      `paths=source_relative` and managed `go_package_prefix: github.com/mrcsin/usher/gen/xray`,
      so every proto package keeps its own Go package
- [x] add the tools module pinning buf, `protoc-gen-go`, `protoc-gen-go-grpc`, as in
      `awg-grpc/tools/go.mod:5-9`
- [x] extend `scripts/format.sh` to check that `gen/` equals `buf generate`; it runs no `buf lint`,
      unlike `awg-grpc/scripts/format.sh:26`, because the vendored files are not usher's and fail
      the default rules
- [x] no unit test in this task: it adds generated code only; `scripts/format.sh`, `go build ./...`
      and `docker build .` verify it, and Tasks 5 and 6 exercise the types
- [x] run `scripts/format.sh`, `go test -race ./...` and `docker build .` - must pass before Task 5

### Task 5: Read inbounds through the decoder tables

**Files:**
- Create: `internal/xray/inbound.go`
- Create: `internal/xray/protocol.go`, `internal/xray/transport.go`, `internal/xray/security.go`
- Create: `internal/xray/inbound_test.go`, `internal/xray/protocol_test.go`,
  `internal/xray/transport_test.go`, `internal/xray/security_test.go`

- [ ] add the three decoder tables of Technical Details with one entry each: VLESS, `tcp`,
      Reality
- [ ] list inbounds, keep those whose proxy settings type has a protocol entry, decode the single
      port, and run the transport and security entries; a missing key or an unsupported value
      fails that inbound with an error naming it
- [ ] write tests for the VLESS entry: decryption `none` accepted, other decryption failing, flow
      `xtls-rprx-vision` for `tcp` and Reality
- [ ] write tests for the `tcp` entry: no transport settings, `tcp.Config` without a header and a
      no-op header accepted; an HTTP header failing
- [ ] write tests for the Reality entry: `pbk` from a fixed private key; `sid` from `ab00000000000000`
      as `ab` and from 8 zero bytes as empty; the first server name empty and the second used;
      all server names empty failing
- [ ] write tests for inbound reading: a valid inbound; a non-VLESS inbound skipped; a port range,
      TLS security, empty security (named `none`) and a non-tcp transport each failing
- [ ] write a leak test: no error or log line contains the private key bytes in any encoding
- [ ] run `go test -race ./...` - must pass before Task 6

### Task 6: Reconcile Xray users

**Files:**
- Create: `internal/xray/session.go`
- Create: `internal/xray/enroll.go`
- Create: `internal/xray/session_test.go`
- Create: `internal/xray/enroll_test.go`

- [ ] implement `Session` for Xray: open lists the inbounds (Task 5)
- [ ] enroll: create an entry with `uuid.NewV4()` for every referenced user without one; never
      change an existing entry
- [ ] apply: read `GetInboundUsers`, compare accounts as in Technical Details, remove extra and
      changed users, add missing and changed users with `level` 0, `email` `tag/user` and the
      account the protocol entry builds; log `users changed`
- [ ] treat an `AlterInbound` error as a failure of that inbound
- [ ] write tests with a fake `HandlerServiceClient`: add, remove, flow change, id change,
      unreferenced inbound emptied, a seeded UUID kept, partial failure converging on the next
      pass
- [ ] write a leak test: no usher log line or error contains a UUID
- [ ] run `go test -race ./...` - must pass before Task 7

### Task 7: Render VLESS links

**Files:**
- Create: `internal/xray/link.go`
- Create: `internal/xray/link_test.go`
- Create: `internal/xray/testdata/` (golden link)

- [ ] implement the VLESS entry's `link` as in Technical Details and write it into
      `clients/<user>/<tag>.txt` in the session's prepare step
- [ ] read the share-link proposal (XTLS/Xray-core discussion #716) and pin parameter names,
      values and encodings in a golden test; ASSUMPTION: the proposal covers the Reality
      parameters `pbk`, `sid` and `spx`
- [ ] write tests: golden link; a link with an empty `sid`; the user name escaped in the fragment
- [ ] run `go test -race ./...` - must pass before Task 8

### Task 8: Wire the Xray backend and the deploy example

**Files:**
- Modify: `cmd/usher/env.go`
- Modify: `cmd/usher/main.go`
- Modify: `deploy/compose.example.yml`
- Create: `deploy/xray/config.json`
- Modify: `cmd/usher/env_test.go`
- Modify: `cmd/usher/main_test.go`

- [ ] read `USHER_XRAY_SOCKET`, absolute when set; make `USHER_AWG_SOCKET` optional, require at
      least one of the two, and require `USHER_DNS` only with `USHER_AWG_SOCKET`
- [ ] build the Xray backend when `USHER_XRAY_SOCKET` is set, with the 10 s call deadline of
      `pass.go:21`
- [ ] add an `xray` service to `compose.example.yml`: the official image, `user: "0:1000"`, the
      example `config.json` (api on `/run/xray/api.sock,0660`, `HandlerService` only, one VLESS
      Reality inbound with `clients: []`) and the shared socket volume
- [ ] write tests: only Xray, only awg, both, neither (usage error), `USHER_DNS` missing with and
      without awg
- [ ] run `go test -race ./...` - must pass before Task 9

### Task 9: Xray end-to-end suite

**Files:**
- Create: `deploy/compose.e2e-xray.yml` (own compose project `name:` and network
  `172.30.98.0/24`, apart from the awg suite's `172.30.99.0/24`)
- Create: `deploy/e2e/xray/` (server config, nginx target config, self-signed certificate)
- Create: `test/e2e/xray/xray_test.go` (own package and `TestMain`, scratch root `.e2e-xray`)
- Modify: `.gitignore` (`.e2e-xray`)
- Modify: `.github/workflows/ci.yml`

- [ ] compose project with Xray server (official image, pinned tag, `user: "0:${E2E_GID:-0}"`),
      nginx with a self-signed certificate and `ssl_protocols TLSv1.3` as the Reality target, usher
      built from the tree, and an Xray client profile
- [ ] the test config holds two users; the test builds each client JSON by copying `pbk`, `sid`
      and `sni` from the rendered link unchanged into `publicKey`, `shortId` and `serverName`
- [ ] assert the three outcomes of Acceptance Evidence item 3
- [ ] run the suite in the CI `e2e` job next to `ci.yml:62`
- [ ] run `go test -tags e2e -count=1 -v ./test/e2e/xray/` - must pass before Task 10

### Task 10: Verify acceptance criteria

- [ ] verify all requirements from Overview are implemented
- [ ] verify edge cases are handled
- [ ] run full test suite: `go test -race -count=1 ./...`
- [ ] run `scripts/format.sh`, `shellcheck scripts/*.sh scripts/ci/*.sh scripts/git-hooks/pre-commit`
      and `go run github.com/rhysd/actionlint/cmd/actionlint@latest`
- [ ] run e2e tests: `go test -tags e2e -count=1 -v ./test/e2e/...` on a host with the module
- [ ] verify test coverage meets project standard

### Task 11: [Final] Update documentation

- [ ] README: the two socket variables, the Xray delivery contract, link files, switching off in
      Xray versus AmneziaWG
- [ ] `docs/architecture/overview.md`: the backend seam, a backend that is down, the decoder tables
      and how a new shape is added, Xray keys and links, state version 2; generalize the locked
      decisions at lines 64, 65, 70, 71, 74, 76
- [ ] `AGENTS.md`: layout (`internal/awg`, `internal/xray`, `proto/xray`, `gen/`, `tools/`,
      `test/e2e/xray`), generation command, Xray e2e, the UUID and the Reality private key in the
      never-logged list; reword "usher saves it before `ApplyPeers`" for both backends
- [ ] move this plan to `docs/plans/completed/`

## Post-Completion

*Items requiring manual intervention or external systems - no checkboxes, informational only*

**Manual verification**
- import one rendered link into a phone client and open a page through it; the link carries a
  trimmed `sid`, which phone clients have not been checked against

**Release**
- tag the release with a `!` header; the notes state that `USHER_AWG_SOCKET` is now required and
  that the previous image cannot read `users.json` once this version has saved it

**Follow-up**
- per-user metrics (issue #1) for both backends; `StatsService` joins the delivery contract then
