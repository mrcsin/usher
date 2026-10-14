# Xray backend from the operator's config

## Overview

- usher gains an Xray backend whose inbounds are whatever the operator defines in Xray's own
  server config. usher reads that config, pushes users into the inbounds through Xray's
  `HandlerService`, and writes per user and inbound a full client config (`.json`) and a share
  link (`.txt`).
- Supported protocols: VLESS and Hysteria2; VMess and Trojan as table entries covered by unit and
  golden tests only. Shadowsocks and WireGuard are out of scope.
- Supported shapes: transports raw/tcp (header none), ws, grpc, httpupgrade, xhttp, hysteria;
  security none, tls, reality; finalmask salamander (Hysteria2) and a synthesized udphop for port
  hopping. Anything else fails that inbound with an error that names the field.
- TLS cover is Reality (no own certificate) or a certificate a public CA issued for the operator's
  domain. A pinned self-signed certificate is an explicit opt-in.
- Xray-core generates no client configs or share links (verified 2026-10-14 against v26.9.9 and
  the 26.10.10 commit: no `vless://`, `hysteria2://` or similar in the source; the CLI commands in
  `main/commands/all/` generate keys and talk to the API). The client side is usher's job, as it
  is a panel's job in 3x-ui and Marzban.
- Breaking changes: `USHER_AWG_SOCKET` becomes required for the awg backend, `users.json` moves to
  version 2. usher is below 1.0, so they ship in a minor release with no breaking marker in commit
  headers.

## Context (from discovery)

usher on `master` (`876b208`):
- `cmd/usher/main.go:22` hardcodes the awg socket.
- `internal/config/config.go:13-14`: user names `{0,31}`, interface names
  `^[A-Za-z0-9_][A-Za-z0-9_=+.-]{0,14}$`.
- `internal/state/state.go:107` rejects every version other than 1.
- `internal/clients/mirror.go:22-24` hardcodes `.conf` in `Path`.
- `internal/watch` polls `usher.yml` only; any other change is seen at the refill tick.

Branch `xray-driver` (parked, draft PR #2) holds reusable commits:
- seam and awg: `bd02bac` (`USHER_AWG_SOCKET`), `00113e4` (seam; moves `internal/clients/render.go`,
  `internal/pass/enroll.go`, `fake_test.go`, `testdata/phone.conf` into `internal/awg`, edits
  `internal/pass/settings.go`), `f409cac` (shared Open adapter, AWG dialer), `7100137` (unexport
  render).
- state: `64dd6b0` (xray group, version 2), `69a3ba5` (validator renames).
- e2e: `c5a9f2c` (Xray e2e suite), `e47b4cc` (freedom outbound reaches the target), `0993e07`
  (`test/e2e/e2eutil`, also rewrites `test/e2e/e2e_test.go`).
- `internal/pass/pass.go` there marks an interface that fails in `Prepare` as failed, skips its
  `Apply` and keeps its files (lines 71-117).

Xray-core at the commit libxray requires (`v1.260327.1-0.20261010092107-701af60772cd`, release
26.10.10 per `core/core.go:20-24`), libxray `v1.261010.0` and reality
`v0.0.0-20260921001439-3c98159dee38`:
- Eight inbound protocols implement `proxy.UserManager`: vless, vmess, trojan, shadowsocks,
  shadowsocks_2022, hysteria, wireguard, masque (`proxy/masque/server.go:225`).
- Accounts: `vless.Account{id, flow, encryption}`, `vmess.Account{id}`, `trojan.Account{password}`,
  `hysteria/account.Account{auth}`. `vmess.MemoryAccount.ToProto` always adds `SecuritySettings`
  (`proxy/vmess/account.go:33-45`); the others echo what was sent.
- `app/proxyman/command/command.go:138`: `GetInboundUsers` with an email answers `[nil]` for a
  user that does not exist; without an email it lists all users.
- Hysteria server: `infra/conf/hysteria.go:39-75` (`version` must be 2; `clients`, or its alias
  `users`, of `{auth, email, level}`). Salamander is a `finalmask.udp` entry
  (`infra/conf/transport_finalmask.go:83`); with `packetSize` it builds a different framing
  (`transport_finalmask.go:733-740`). `transport/internet/finalmask/udphop/config.go:15`: udphop is
  client only; a hop range lives in host DNAT, outside Xray.
- `transport/internet/hysteria/hub.go:62-68`: the hub asks the user validator per request; with
  empty `clients` and empty server `auth` it rejects everyone.
- `infra/conf/serial/loader.go:55-57`: a plain `json.Decoder`, so the `x-usher` key is ignored;
  `infra/conf/json/reader.go` accepts `//`, `/* */` and `#` comments.
- `infra/conf/transport_internet.go:16-45,80-82`: aliases `method` for `network`, `splithttp` for
  `xhttp`, `websocket` for `ws`, `tcp` for `raw`, `splithttpSettings` and `tcpSettings` for the
  settings blocks.
- `infra/conf/transport_security.go:258-278`: `TLSCertConfig.Build()` reads `certificateFile` and
  `keyFile` from disk. `transport_security.go:361-363`: `allowInsecure` is a removed feature and
  fails config load. `infra/conf/trojan.go:73-74`: a Trojan `flow` fails the build.
- `transport/internet/tls/config.go:609-620`: the Xray client verifies against its bundled Mozilla
  roots unless `useSystemCA` is set. `tls/config.go:97-130`: certificate files are re-read every
  3600 s and swapped in when they change.
- `transport/internet/splithttp/hub.go:143-154`: the xhttp server rejects a request whose padding,
  session id or seq placement differs from its config. `infra/conf/transport_method.go:317-326`:
  `extra` replaces every xhttp field except host, path and mode.
- `transport/internet/system_listener.go:128-165`: a unix listener never unlinks an existing
  socket path and holds `flock(LOCK_EX)` on `<socket>.lock` while it listens;
  `app/commander/commander.go:91-95` fails core start on the bind error.
- reality `tls.go:212`: `serverNames` is an exact-match map. `tls.go:216-235`: a ClientHello
  without an `X25519MLKEM768` key share is not authenticated and goes to the target.
- libxray `share/generate_share.go:30-41`: a failing outbound is skipped and the only error is the
  constant `no valid outbounds`. `share/hysteria2.go:235-255`: refuses salamander `packetSize`;
  accepts udphop only as `mode: "intervalLocal,intervalRemote"`, `interval` with From equal to To
  and at least 5, no `remoteIPs`, no `sockopt`, udphop last in `udp` and salamander first.

Spike, measured 2026-10-14 on a Linux host with `ghcr.io/xtls/xray-core:26.10.10`, a throwaway
Go tool and docker-network clients (Xray 26.10.10 and sing-box 1.14.3):
- Xray starts with `x-usher` blocks, empty `clients` and a comment in the file.
- A user added through `AlterInbound` works at once on every inbound: VLESS raw+Reality+vision,
  five xhttp+TLS variants, ws+TLS, Hysteria2+TLS+salamander, VMess ws, Trojan TLS.
- After `RemoveUser`, new connections fail on every inbound. An open Hysteria2 QUIC connection
  keeps working until the client reconnects.
- The plain-copy xhttp mirror (path, host, mode) fails against a server with
  `sessionIDPlacement: header`, `xPaddingObfsMode` or `xPaddingBytes`; the full block, or the full
  block packed into `extra` in both `.json` and `.txt`, succeeds.
- With a CA the client trusts, `.json` and `.txt` work for every TLS inbound without a pin.
- The Xray client connects to Reality with fingerprints `chrome`, `firefox` and `safari`; `ios`,
  `android`, `edge`, `360`, `qq`, `random` and `randomized` fail (three runs each for the random
  ones).
- sing-box 1.14.3 works with VLESS ws, Trojan, VMess and Hysteria2+salamander. It fails Reality
  against this server on every uTLS fingerprint (the server debug output shows no auth key) and
  has no xhttp transport.
- `GetInboundUsers` echo equals the sent account for VLESS, Trojan and Hysteria; VMess returns
  `security_settings:{type:AES128_GCM}` added.
- `docker kill -s KILL` on Xray, then start: `Failed to start: listen unix /run/xray/api.sock:
  bind: address already in use`. The image has no shell. The uid that owns the socket directory
  can unlink the stale socket, after which Xray starts.
- With inline certificates, `Build()` of every inbound in the file is `proto.Equal` to
  `ListInbounds` (receiver and proxy settings; runtime users do not appear in the listing).
  Editing `shortIds` or a port in the file makes the receiver differ. File certificates were not
  measured.
- After `docker compose restart xray` with a 30 s refill tick, Reality was back after 28 s and
  Hysteria2 after 32 s.
- `go list -deps` gives 711 packages; the static stripped tool binary is 34 MB.

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
  the environment and for rolling back to an image that only knows version 1

## Testing Strategy

- **unit tests**: table-driven, beside the source; gRPC fakes as structs, as `AGENTS.md` describes.
- **golden tests**: one input inbound (server JSON plus `x-usher`) and its expected client `.json`
  and `.txt` per supported shape.
- **leak test**: no file under `clients/`, no log line and no drift reason contains `privateKey`,
  `mldsa65Seed`, `certificateFile`, `keyFile`, a `key` PEM, `sockopt` or a server `fallbacks`
  entry.
- **e2e**: tag `e2e`, package `test/e2e/xray`, Docker only. The server certificate comes from a
  test CA the test generates; the Xray client container trusts it through `SSL_CERT_FILE` plus
  `useSystemCA`, which the test adds to the client config (the Xray client ignores the system
  store by default). Each case connects with the rendered `.json` and with the `.txt` turned back
  into a config by libxray `ConvertShareLinksToXrayJson`.

## Acceptance Evidence

Today `master` has no Xray backend: naming an Xray tag in `usher.yml` logs "interface is not
reported by awg-grpc" and nothing reaches Xray.

1. `go test -race -count=1 ./...` and `scripts/format.sh` pass.
2. Golden tests: `go test ./internal/xray/ -run Golden` passes for VLESS+raw+Reality,
   VLESS+xhttp+TLS with non-default fields, VLESS+xhttp+TLS with a server `extra`, VLESS+ws+TLS,
   Hysteria2+TLS+salamander, Hysteria2 with `hop_ports`, Hysteria2 with `trust: pin`, VMess+ws+TLS
   and Trojan+raw+TLS. Every golden `.json` outbound builds with `conf.OutboundDetourConfig.Build()`
   and every golden `.txt` parses back through `ConvertShareLinksToXrayJson`.
3. Xray e2e: `go test -tags e2e -count=1 -v ./test/e2e/xray/` passes with four inbounds
   (VLESS+raw+Reality; VLESS+xhttp+TLS with `sessionIDPlacement: header` and `xPaddingBytes`;
   VLESS+ws+TLS with `certificateFile`; Hysteria2+TLS+salamander with an inline certificate) and
   two users:
   - an HTTP request through each user's `.json` and `.txt` returns 200;
   - after one user leaves `usher.yml`, new connections through that user fail and the other user
     still works;
   - after `docker compose restart xray`, connections work again within 45 s;
   - after `docker kill -s KILL` of Xray, the restart policy restarts it, it fails on the stale
     socket until usher unlinks it at its next tick, and connections work again within 90 s;
   - changing `shortIds` in the mounted config without restarting Xray fails that inbound with a
     drift reason at the next tick, and a user removed from `usher.yml` while that drift is present
     is still removed from Xray;
   - rewriting the `certificateFile` with a new leaf from the same CA causes no drift.
4. awg e2e: `go test -tags e2e -count=1 -v ./test/e2e/` still passes on a host with the module.
5. Manual smoke on a public host: import one VLESS+Reality link and one Hysteria2 link into
   v2rayNG and open a page through each.

## Progress Tracking

- mark completed items with `[x]` immediately when done
- add newly discovered tasks with ➕ prefix
- document issues/blockers with ⚠️ prefix
- update plan if implementation deviates from original scope
- keep plan in sync with actual work done

## Solution Overview

- **Ownership of Xray's JSON.** The operator (delivery) owns one server `config.json`: inbounds,
  outbounds, routing, `api`, certificates, Reality keys, plus a per-inbound `x-usher` block with
  client hints. Xray reads it at start; usher reads the same file read-only. usher owns the users
  inside the inbounds at runtime (through the API) and everything under `clients/`. Xray keeps the
  users in memory only, so usher refills them after a restart.
- **Backend seam.** The `Session`/`Backend` seam from `xray-driver` carries over, with
  `Backend.Suffixes []string`, because the Xray backend owns two file kinds. A down backend keeps
  all files with any of its suffixes. The Xray session reports every managed tag of the file, so a
  tag that fails validation or drift keeps its files instead of having them deleted.
- **Reading the config.** The file is read with Xray's comment-aware reader into `infra/conf`
  types plus raw maps. Config aliases (`method`, `splithttp`, `websocket`, `tcp`,
  `splithttpSettings`, `tcpSettings`, Hysteria `users`) are normalized before validation and
  mirroring. The backend registers a discard handler for Xray's global logger once at
  construction, because `Build()` prints deprecation warnings and usher runs no Xray instance.
- **Credentials in final form.** The `xray` group of `users.json` stores what the protocol uses:
  `id` (UUID v4, lowercase) for VLESS and VMess, `password` (base64url of 32 random bytes) for
  Trojan and Hysteria2. An operator can still seed or re-key an entry by hand.
- **Users.**
  - Email is `tag/user`, level 0. Every managed inbound starts with an empty `clients` list.
  - Reconcile lists all users of an inbound (`GetInboundUsers` without an email) and compares per
    protocol on the credential only: VLESS `id` and `flow`, VMess `id`, Trojan `password`,
    Hysteria `auth`. It removes foreign and changed emails and adds missing and changed ones.
  - Removal is effective for new connections. A Hysteria2 client with an open QUIC connection
    keeps it until it reconnects; the README states this.
- **API socket.** When the socket path exists and `flock(LOCK_EX|LOCK_NB)` on `<socket>.lock`
  succeeds, no Xray listens on it: usher unlinks the socket and reports Xray down for this pass.
  Xray's restart policy brings it up on the next attempt. The socket directory is writable by
  usher.
- **Drift guard.** `Open` calls `ListInbounds` once and compares each managed inbound with
  `Build()` of its file entry: proxy type, proxy settings and receiver settings. Certificate
  entries with a `certificateFile` are removed from the file entry before `Build()` (usher has no
  access to the key files) and entries with a `CertificatePath` from the runtime side; Xray
  reloads those files itself. A tag in the file but not at runtime also counts as drift. `Apply`
  reconciles users regardless and then returns the drift error, so revocation keeps working while
  the old client files stay. The drift reason names the differing part (port, proxy type, proxy
  settings, stream settings), never a value.
- **Client mirror.**
  - Taken from the server: `network`, `security`; from `wsSettings`, `grpcSettings` and
    `httpupgradeSettings` only path, host, serviceName and authority; `rawSettings` only with
    header type none.
  - xhttp: path, host and mode at the top. If the server has `extra`, it is mirrored verbatim and
    the other top-level fields are dropped, as the server itself ignores them; otherwise every other
    field goes into `extra`. The same form serves `.json` and `.txt`. An inbound with
    `downloadSettings` fails.
  - Rebuilt: `tlsSettings` and `realitySettings`. Dropped: everything else.
  - VLESS only: `flow` is `xtls-rprx-vision` iff network is raw and security is tls or reality.
  - The rendered outbound passes `conf.OutboundDetourConfig.Build()` before any file is written;
    an error fails the inbound.
- **TLS trust**, one rule for every protocol, set by `x-usher.trust`:
  - `ca` (default): the client gets `serverName`, `alpn` and the fingerprint, nothing else.
  - `pin`: the client also gets `pinnedPeerCertSha256`, from `x-usher.pin_sha256` or computed from
    an inline `certificates[].certificate`. A pin inbound without either fails. A Hysteria2 pin
    inbound gets no `.txt`, because v2rayNG and Hiddify do not read `pcs` for `hysteria2://`.
- **Reality.**
  - `serverName` is the first `serverNames` entry that is non-empty and has no `*`; none such fails
    the inbound.
  - `publicKey` comes from `privateKey` (`crypto/ecdh`); `shortId` is `shortIds[0]` verbatim;
    `spiderX` is `/`; `mldsa65Verify` is derived from `mldsa65Seed` when the seed is set.
  - `fingerprint` is `chrome` by default; an override is limited to `chrome`, `firefox` and
    `safari`, the ones that carry `X25519MLKEM768`.
  - Reality links work in Xray-core clients only; sing-box based clients fail the server's
    `X25519MLKEM768` requirement. The README states this.
- **Hysteria2.**
  - The server `hysteriaSettings.auth` must be empty; the client gets its `auth` from the user's
    password.
  - From salamander only `password` is mirrored; a salamander with `packetSize` fails the inbound.
    `finalmask.quicParams` is allowed and stays on the server, with `udpIdleTimeout` and
    `masquerade`.
  - `x-usher.hop_ports` synthesizes a client udphop mask in libxray's shape (Context). The hop
    range needs a host DNAT to the inbound port, which the README states as a prerequisite.
- **Outputs.**
  - `clients/<user>/<tag>.json` is a complete client config: one outbound and a local socks/http
    inbound.
  - `clients/<user>/<tag>.txt` comes from libxray, called once per outbound. usher builds every
    outbound from shapes it knows, so a libxray refusal is a usher defect: it is logged with the
    tag, and no `.txt` is written.
- **Dependency.** usher imports `xray-core` (`infra/conf`, `app/proxyman/command`) and
  `libxray/share`. The vendored protos and buf tooling of `xray-driver` are not used.

## Technical Details

- **Environment.**
  - `USHER_AWG_SOCKET` and `USHER_XRAY_SOCKET` are absolute socket paths; at least one is set.
  - `USHER_XRAY_CONFIG` is the read-only path of the Xray server config and is required with
    `USHER_XRAY_SOCKET`.
  - `USHER_HOST` stays required. `USHER_DNS` is required only with awg.
- **`x-usher` block** on an inbound (Xray ignores unknown keys):
  ```json
  "x-usher": {
    "address": "203.0.113.10",
    "port": 443,
    "sni": "example.com",
    "fingerprint": "chrome",
    "trust": "ca",
    "pin_sha256": "…",
    "hop_ports": "20000-30000"
  }
  ```
  - `address` defaults to `USHER_HOST`, `port` to the inbound port, `sni` to the server
    `tlsSettings.serverName`, `fingerprint` to `chrome`, `trust` to `ca`.
  - A TLS inbound with neither `sni` nor `tlsSettings.serverName` fails.
  - `pin_sha256` is valid only with `trust: pin`.
- **Delivery contract.**
  - One JSON file, no `-confdir`; comments allowed. Both containers mount the directory that holds
    it, so a replaced file is seen; usher sees an edit at its next refill tick.
  - `api.services: ["HandlerService"]` and `api.listen` on a unix path with `,0660` in a directory
    usher can write; the Xray process gid equals usher's group.
  - Xray runs with `restart: unless-stopped`.
  - Every managed inbound has empty `clients`, a single integer `port`, no unix `listen`, and a tag
    that matches the usher interface pattern.
  - Hysteria2 inbounds have an empty `hysteriaSettings.auth`.
  - TLS inbounds use a public-CA certificate for the operator's domain unless `trust: pin`.
  - The Xray image is `ghcr.io/xtls/xray-core:26.10.10`, the release of the `xray-core` commit in
    `go.mod`. A different image can change `Build()` output and put every inbound in drift.
- **Interface name pattern** widens from 15 to 32 characters. awg-grpc still rejects names the
  kernel cannot hold.
- **State.** Version 2 adds the `xray` group: `{"vless-reality/alice": {"id": "…"},
  "hy2/alice": {"password": "…"}}`. Before the first save of a version 1 file usher writes
  `users.json.v1` next to it, so a rollback is a file rename.
- **Rendering per inbound shape**: the golden tests in Task 8 are the authoritative table.

## What Goes Where

- **Implementation Steps** (`[ ]` checkboxes): code, tests, deploy examples and docs in this
  repository.
- **Post-Completion** (no checkboxes): the public-host smoke with phone clients, the release.

## Implementation Steps

### Task 1: Port the backend seam and the awg socket variable

**Files:**
- Modify: `cmd/usher/env.go`, `cmd/usher/main.go`, `cmd/usher/*_test.go`
- Create: `internal/pass/backend.go`; Modify: `internal/pass/pass.go`, `internal/pass/settings.go`,
  `internal/pass/*_test.go`
- Create: `internal/awg/` (moved from `internal/clients/render.go`, `internal/pass/enroll.go`,
  `fake_test.go`, `testdata/phone.conf`); Modify: `internal/clients/mirror.go`
- Modify: `deploy/compose.example.yml`, `deploy/compose.e2e.yml`

- [x] cherry-pick `bd02bac`, `00113e4`, `f409cac`, `7100137` from `xray-driver`; drop the hunks
      for `docs/plans/20261007-xray-driver.md` and for Xray files that do not exist here; reword
      the `bd02bac` header without `!`
- [x] change `Backend.Suffix` to `Suffixes []string` and make `clients.Existing` keep every suffix
      of a down backend
- [x] make `USHER_DNS` required only when `USHER_AWG_SOCKET` is set
- [x] keep the awg behaviour of `master` unchanged
- [x] write tests for the seam: two fake backends, a collision, a down backend with two suffixes,
      the `USHER_DNS` rule
- [x] run `go test -race ./...` and the awg e2e (CI) - must pass before Task 2 (awg e2e deferred
      to CI, not run locally)

### Task 2: users.json version 2 with Xray credentials

**Files:**
- Modify: `internal/state/state.go`, `internal/state/state_test.go`
- Modify: `internal/config/config.go`, `internal/config/config_test.go`

- [x] start from `64dd6b0` and `69a3ba5`: `Xray map[string]XrayEntry`, `XrayEntry{ID uuid.UUID;
      Password string}` with exactly one set; validate names `tag/user`; errors never quote a secret
- [x] version 2; `Load` accepts 1 and 2; before the first save of a version 1 file write
      `users.json.v1`
- [x] widen the interface name pattern to 32 characters
- [x] write tests: v1 load and backup, v2 round trip, both credential kinds, invalid entries, no
      secret in errors, a 32-character tag
- [x] run `go test -race ./...` - must pass before Task 3

### Task 3: Read the operator's Xray config

**Files:**
- Modify: `go.mod`, `go.sum`
- Create: `internal/xray/config.go`, `internal/xray/config_test.go`
- Create: `internal/xray/testdata/` (server configs)

- [x] require `github.com/xtls/libxray v1.261010.0` and the `xray-core` commit it requires
      (`701af60772cd`, release 26.10.10); record the stripped binary size and the cold CI
      `go build` time in the progress log
- [x] read the file with Xray's comment-aware reader, decode inbounds into `infra/conf` types plus
      raw maps, normalize the aliases; register the discard log handler
- [x] keep inbounds whose protocol has an entry (`vless`, `hysteria`, `vmess`, `trojan`); skip
      others and untagged ones
- [x] validate per inbound: single integer port, no unix listen, empty `clients`/`users`, VLESS
      `decryption` none, empty server Hysteria auth, masks only salamander without `packetSize`
      (plus `quicParams`), no xhttp `downloadSettings`, a usable Reality `serverNames` entry, an
      SNI for TLS, the `x-usher` schema (`trust` values, `pin_sha256` only with `pin`, a pin source
      for `pin`, the fingerprint allowlist on Reality); each failure names the field
- [x] write tests for every accepted shape, every failure, every alias and a file with comments
- [x] run `go test -race ./...` and `docker build .` (CI) - must pass before Task 4 (docker build run on `ger`)

### Task 4: Drift guard against the running Xray

**Files:**
- Create: `internal/xray/drift.go`, `internal/xray/drift_test.go`

- [x] compare `Build()` of each managed inbound with one `ListInbounds`: proxy type, proxy settings,
      receiver settings, with file-based certificate entries removed on both sides
- [x] a mismatch or a tag missing at runtime is a per-tag drift reason naming the differing part
      only; runtime tags absent from the file are logged each pass
- [x] write tests with a fake `HandlerServiceClient`: match, port change, `shortIds` change, a
      `privateKey` change (reason without the value), protocol change, a `certificateFile` inbound
      with changed file contents (no drift), a file tag missing at runtime, an extra runtime tag
- [x] run `go test -race ./...` - must pass before Task 5

### Task 5: Users through HandlerService

**Files:**
- Create: `internal/xray/protocol.go`, `internal/xray/session.go`, `internal/xray/enroll.go`
- Create: matching `_test.go` files

- [ ] protocol table: credential kind, account builder and credential comparison for VLESS (`id`,
      flow), Hysteria2 (`auth`), VMess (`id`), Trojan (`password`)
- [ ] enroll: create the credential for every referenced user without one; never change an
      existing entry
- [ ] session: `Interfaces` reports every managed tag; `Open` runs the drift check; `Apply` lists
      all users per inbound, compares credentials, removes foreign and changed emails, adds missing
      and changed, logs `users changed`, then returns the tag's drift error if any
- [ ] write tests with a fake service: add, remove, change, foreign email, seeded credential kept,
      VMess echo with `SecuritySettings` (no change), removal while the tag is in drift, partial
      failure converging, no secret in logs
- [ ] run `go test -race ./...` - must pass before Task 6

### Task 6: Stale API socket recovery

**Files:**
- Create: `internal/xray/socket.go`, `internal/xray/socket_test.go`

- [ ] before dialing: if the socket path exists and `flock(LOCK_EX|LOCK_NB)` on `<socket>.lock`
      succeeds, unlink the socket, release the lock and report Xray down for this pass
- [ ] write tests in `t.TempDir()`: a stale socket (listener with `SetUnlinkOnClose(false)`) is
      removed; a socket whose lock another descriptor holds is kept; a missing path is left alone
- [ ] run `go test -race ./...` - must pass before Task 7

### Task 7: Client mirror

**Files:**
- Create: `internal/xray/mirror.go`, `internal/xray/mirror_test.go`

- [ ] transport fields as in Solution Overview, xhttp `extra` packing or verbatim server `extra`,
      the VLESS flow rule
- [ ] the TLS trust rule: `ca` default, `pin` from `pin_sha256` or the inline certificate
- [ ] Reality rewrite including the `serverNames` rule and `mldsa65Verify` from `mldsa65Seed`
- [ ] Hysteria2: client auth, salamander password, the udphop mask from `hop_ports` in libxray's
      shape
- [ ] `x-usher` overrides
- [ ] write the leak test (Testing Strategy) and tests for each trust case, hop, flow, xhttp
      packing, server `extra` and override
- [ ] run `go test -race ./...` - must pass before Task 8

### Task 8: Render client files and golden tests

**Files:**
- Create: `internal/xray/render.go`, `internal/xray/render_test.go`
- Create: `internal/xray/testdata/golden/`

- [ ] `.json`: complete client config with the mirrored outbound and a local proxy inbound; the
      outbound passes `conf.OutboundDetourConfig.Build()` or the inbound fails
- [ ] `.txt`: libxray per outbound; on error no `.txt` and a logged defect; none for a Hysteria2
      pin inbound
- [ ] prepare step writes both files through the seam
- [ ] golden tests for the shapes in Acceptance Evidence item 2
- [ ] run `go test -race ./...` - must pass before Task 9

### Task 9: Wire the backend and the deploy example

**Files:**
- Modify: `cmd/usher/env.go`, `cmd/usher/main.go`, tests
- Modify: `deploy/compose.example.yml`; Create: `deploy/xray/config.json`

- [ ] read `USHER_XRAY_SOCKET` and `USHER_XRAY_CONFIG`; build the Xray backend
- [ ] example config: one VLESS+Reality and one Hysteria2 inbound with a `certificateFile` and
      `x-usher`, placeholders that Xray refuses to start with; compose mounts the config directory
      into both containers (usher read-only), a socket directory usher owns, Xray with
      `restart: unless-stopped` and image `26.10.10` with a comment that it follows `go.mod`
- [ ] write env and wiring tests
- [ ] run `go test -race ./...` - must pass before Task 10

### Task 10: Xray end-to-end suite

**Files:**
- Create: `test/e2e/xray/` and `deploy/compose.e2e-xray.yml`, `deploy/e2e/xray/` (from
  `c5a9f2c`, `e47b4cc`, `0993e07`, adapted)
- Modify: `.github/workflows/ci.yml`

- [ ] server config with the four inbounds of Acceptance Evidence item 3, a test CA and leaves
      generated by the test, `x-usher` blocks, freedom `finalRules` allowing the target; config
      directory mount; Xray with `restart: unless-stopped`; Hysteria2 case first
- [ ] Xray client with `SSL_CERT_FILE` and `useSystemCA`
- [ ] each client connects with the rendered `.json` and with the `.txt` via
      `ConvertShareLinksToXrayJson`
- [ ] assert the outcomes of Acceptance Evidence item 3
- [ ] run the suite in the CI `e2e` job - must pass before Task 11

### Task 11: Verify acceptance criteria

- [ ] verify all requirements from Overview are implemented
- [ ] verify edge cases are handled
- [ ] run full test suite: `go test -race -count=1 ./...`
- [ ] run `scripts/format.sh`, shellcheck and actionlint
- [ ] run e2e tests: `go test -tags e2e -count=1 -v ./test/e2e/...` (CI)
- [ ] verify test coverage meets project standard

### Task 12: [Final] Update documentation

- [ ] README: delivery contract (image follows `go.mod`, directory mount, restart policy, socket
      directory), `x-usher` block, TLS trust rule, outputs, supported shapes, client compatibility
      (Reality in Xray-core clients only, xhttp not in upstream sing-box), the Hysteria2 removal
      limit, the hop DNAT prerequisite, config edits seen at the refill tick
- [ ] `docs/architecture/overview.md`: who owns which JSON, the seam, the mirror, drift guard,
      state v2, the stale socket rule
- [ ] `AGENTS.md`: layout, the Xray e2e, never-logged fields
- [ ] move this plan to `docs/plans/completed/`

## Post-Completion

*Items requiring manual intervention or external systems - no checkboxes, informational only*

**Manual verification on a public host**
- run the example stack on a test VPS with a public IP and a domain with a public certificate; the
  operator opens the test ports in the host firewall (sudo)
- import one VLESS+Reality link, one Hysteria2 link and one Hysteria2 `hop_ports` link into
  v2rayNG and open a page through each; the hop link checks how v2rayNG reads a port range in the
  authority
- import the ws and Hysteria2 links into Hiddify

**Release**
- bump the image tag in `deploy/compose.example.yml`; the notes state the new required variables,
  `users.json` version 2, the `users.json.v1` backup and the Xray image 26.10.10
