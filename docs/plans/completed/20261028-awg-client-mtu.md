# MTU line in AmneziaWG client configs

## Overview

- Every rendered AmneziaWG client config carries `MTU = <n>` in `[Interface]`, where `<n>` is the
  `MTU` line of the server interface config, reported by awg-grpc in `InterfaceStatus.mtu`.
  Without that line awg-grpc reports 0 and the client config stays as today.
- One value on both ends of the tunnel, set once by the operator in the awg-grpc
  `<interface>.conf`. usher has no MTU setting and no default of its own.
- Today client configs carry no `MTU`, so each client picks its own: the mobile apps 1280,
  `awg-quick` on a desktop 1420. 1420 plus the AmneziaWG overhead overshoots paths narrower than
  1500.
- Implements usher issue #6. Depends on awg-grpc `v0.2.0` (awg-grpc plan
  `docs/plans/20261028-interface-mtu.md`).

## Context (from discovery)

- `internal/awg/render.go:16` `render(entry, iface, host, dns)` writes `[Interface]`
  (`PrivateKey`, `Address`, `DNS`, then `client_params`) and `[Peer]`. Its one caller is
  `internal/awg/session.go:83`.
- `internal/awg/render_test.go` checks rendered lines against `client_params` fixtures through
  `mutate` cases; `TestRenderGolden` (`:38`) compares `testInterface()` with
  `internal/awg/testdata/phone.conf`.
- `test/e2e/e2e_test.go:272` `clientConfigCopy` drops only `DNS = ` lines, so an `MTU` line
  reaches `awgc0.conf`; the `compose` helper runs commands in the `client` container.
- Issue #6 proposes a usher-side default of 1280. usher keeps none: the value lives in the
  server interface config, and without it clients keep their own defaults (1280 in the mobile
  apps).
- `go.mod:8` pins `github.com/mrcsin/awg-grpc v0.1.0`; `deploy/compose.example.yml:3` and
  `deploy/compose.e2e.yml:4,44` pin `ghcr.io/mrcsin/awg-grpc:v0.1.0`.
- `internal/pass/pass.go:191` opens every backend and so calls `GetStatus` on every pass: a new
  value reaches the client files on the next pass.
- e2e: `deploy/e2e/awg/awge2e0.conf` (no `MTU`, `S4 = 23`); `test/e2e/e2e_test.go:198-203` copies
  the rendered `phone/<iface>.conf` to `awgc0.conf`; the `client` service runs
  `awg-quick up awgc0` (`deploy/compose.e2e.yml:53`).
- Xray needs nothing: VLESS rides a TCP connection of the host stack, and the share link has no
  MTU parameter.

## Development Approach

- **testing approach**: Regular (code first, then tests)
- complete each task fully before moving to the next
- **CRITICAL: every task MUST include new/updated tests** for code changes in that task
- **CRITICAL: all tests must pass before starting next task**
- **CRITICAL: update this plan file when scope changes during implementation**

## Testing Strategy

- unit tests in `internal/awg/render_test.go`, table-driven, as today
- e2e (`test/e2e`, tag `e2e`) on a Linux host with the module: the server interface config sets
  `MTU = 1280`; the test asserts the rendered line and the MTU of the client interface

## Acceptance Evidence

- Today: `go test ./internal/awg/` passes, and no rendered config contains `MTU`.
- After:
  - `go test -race ./...` passes with render cases for `mtu` 1280 (line present right after
    `DNS`) and `mtu` 0 (no line).
  - `scripts/format.sh` passes.
  - The e2e suite passes with `MTU = 1280` in `awge2e0.conf`; it asserts `MTU = 1280` in the
    rendered `phone/awge2e0.conf` and that `cat /sys/class/net/awgc0/mtu` in the client
    container prints 1280.

## Solution Overview

- `render` writes `MTU = <iface.GetMtu()>` after `DNS` when the value is above 0. A 0 means the
  server config has no `MTU` line, or awg-grpc is older than `v0.2.0`; either way the file stays
  as today.
- usher copies the value and does not judge it; the operator owns it. The awg-grpc README
  explains the choice of 1280.
- awg-grpc reads the `MTU` line once at start, so a changed value reaches usher after an
  awg-grpc restart. usher then rewrites every client file of the interface. Clients keep working
  with the old file; the new value applies once a user imports the new one. Keys and addresses do
  not change.
- `MTU` is a standard `[Interface]` key of WireGuard configs; the parsers read key-value pairs in
  any order. The e2e proves `awg-quick` accepts it; acceptance by the mobile and desktop apps is
  not tested here.

## Implementation Steps

### Task 1: Bump awg-grpc to v0.2.0

**Files:**
- Modify: `go.mod`, `go.sum`
- Modify: `deploy/compose.example.yml`, `deploy/compose.e2e.yml`

- [x] `go get github.com/mrcsin/awg-grpc@v0.2.0`, `go mod tidy`
- [x] pin `ghcr.io/mrcsin/awg-grpc:v0.2.0` in all three image lines
- [x] run `go test -race ./...` - must pass before task 2

### Task 2: Render the MTU line

**Files:**
- Modify: `internal/awg/render.go`
- Modify: `internal/awg/render_test.go`

- [x] write `MTU = <n>` after `DNS` when `iface.GetMtu() > 0`
- [x] `testInterface()` stays at `mtu` 0, so `TestRenderGolden` and `testdata/phone.conf` stay
      unchanged
- [x] render cases through `mutate`: `mtu` 1280 gives `DNS = 9.9.9.9\nMTU = 1280\nJc = 4\n`
      (pins the position); `mtu` 0 gives no `MTU` line
- [x] run `go test -race ./...` and `scripts/format.sh` - must pass before task 3

### Task 3: Cover the MTU in the e2e suite

**Files:**
- Modify: `deploy/e2e/awg/awge2e0.conf`
- Modify: `test/e2e/e2e_test.go`

- [x] add `MTU = 1280` to `awge2e0.conf`
- [x] assert the rendered `phone/awge2e0.conf` contains `MTU = 1280`
- [x] after the handshake, assert `cat /sys/class/net/awgc0/mtu` in the client container prints
      1280
- [x] build and run the e2e suite on a Linux host (AGENTS.md "Test") (compiles; run deferred to
      the CI e2e job on the PR)

### Task 4: Verify acceptance criteria

- [x] run the commands of Acceptance Evidence (`go test -race ./...` and `scripts/format.sh`
      pass; the render cases `mtu follows dns` and `mtu 0 writes no mtu line` and the e2e MTU
      assertions exist; e2e compiles; run deferred to the CI e2e job on the PR)

### Task 5: [Final] Update documentation

**Files:**
- Modify: `README.md`
- Modify: `docs/architecture/overview.md`

- [x] README, AmneziaWG client config paragraph (`README.md:136`): the config carries the `MTU`
      of the server interface config when it has one; link to the awg-grpc README MTU section
- [x] README: a changed MTU rewrites the client files; users import the new file to apply it
- [x] README: why usher copies and keeps no default of its own (one value on both ends; the
      PR body says the same, since issue #6 proposed a usher default)
- [x] overview: the AWG backend reads `mtu` from `GetStatus` for the client files
- [ ] move this plan to `docs/plans/completed/` (at delivery)

## Post-Completion

**External system updates:**
- awg-grpc `v0.2.0` must be tagged and its image published before Task 1
- an operator sets `MTU = 1280` in each awg-grpc `<interface>.conf`, upgrades both containers,
  and sends users their new client files

**Executed by exec:**
- branch: awg-client-mtu

## Verify it yourself

1. `go test -race ./internal/awg/`: the render cases `mtu follows dns` (`DNS = 9.9.9.9\nMTU = 1280\nJc = 4\n`),
   `mtu without client params` and `mtu 0 writes no mtu line` pass; `TestRenderGolden` keeps
   `testdata/phone.conf` without `MTU`. At `2d0c367` no render case knows `mtu`.
2. The CI `e2e` job on the PR: the rendered `phone/awge2e0.conf` contains `MTU = 1280`, and
   `cat /sys/class/net/awgc0/mtu` in the client container prints 1280.
3. On a deployment with awg-grpc v0.2.0 and `MTU = 1280` in the interface config: after the awg
   container restart and one usher pass, `grep MTU <clients>/<user>/<iface>.conf` prints
   `MTU = 1280`.
