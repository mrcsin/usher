# Per-user stats in a Prometheus textfile

## Overview

- usher writes the traffic and online state of every applied user to a Prometheus text exposition
  file. node_exporter's textfile collector serves it. usher keeps no network listener.
- The file is backend-neutral: AmneziaWG and Xray users get the same metrics with the same labels.
  A reader tells the backends apart only by the `interface` label value.
- The file is a snapshot. Each pass replaces it whole; history lives in Prometheus. usher keeps no
  totals and no state between passes.
- Implements usher issue #1 with a design that also covers Xray. The PR description states where
  it differs from the issue.

## Context (from discovery)

- `internal/pass/backend.go:10` defines `Session` (`Interfaces`, `Prepare`, `Apply`, `Close`).
  `internal/pass/pass.go:40` `Run` calls `applyAll` at `:69` and `mirrorClients` at `:76`.
  `cmd/usher/main.go:88` `asSession[S pass.Session]` is instantiated with `*awg.Session` (`:71`)
  and `*xray.Session` (`:82`), so a method added to `Session` must exist on both types first.
- `internal/pass/settings.go:5` `Settings` holds `ConfigPath`, `ClientsDir`, `StatePath`;
  `cmd/usher/env.go:62` fills it. Optional backends use the "empty means off" pattern for
  `USHER_AWG_SOCKET` and `USHER_XRAY_SOCKET` (`cmd/usher/env.go:37`, `:47`).
- AWG: `internal/awg/session.go:25` `Dial` returns `awgv1.ManagementServiceClient`;
  `publicKeyNames` at `:125` maps public keys to users. The pinned `awg-grpc v0.1.0` client has
  `ListPeers` (`gen/awg/v1/awg_grpc.pb.go:52`) and `PeerStatus` getters `GetLastHandshake`,
  `GetRxBytes`, `GetTxBytes` (`gen/awg/v1/awg.pb.go:377`, `:384`, `:391`).
- AWG client configs carry `PersistentKeepalive = 25` (`internal/awg/render.go:45`), so a client
  with the tunnel up handshakes about every 2 minutes even when idle.
- Xray: `internal/xray/session.go:22` `Dial` returns only `command.HandlerServiceClient`;
  `cmd/usher/main.go:77` builds it through the generic `dialUnix` at `:95`. Six closures in
  `internal/xray/session_test.go` have that `Dial` type. `readConfig` at
  `internal/xray/config.go:88` returns `([]inbound, error)` and decodes only `inbounds` (`:97`).
- Xray `Apply` returns a usable drift joined with the reconcile result
  (`internal/xray/session.go:130`), so the pass fails an inbound whose users Xray still serves.
- Xray stat names, from the pinned `xray-core`: `user>>>EMAIL>>>traffic>>>uplink` and
  `...>>>downlink` (`app/dispatcher/default.go:164`, `:173`), `user>>>EMAIL>>>online`
  (`:225`). The email is `tag/user` (`internal/state/state.go:102` `EntryName`).
  `QueryStats` matches by `strings.Contains` (`app/stats/command/command.go:168`).
  `GetAllOnlineUsers` returns the full online map names with a count above 0
  (`app/stats/stats.go:225`).
- Xray counter lifetime: a traffic counter is registered at a user's first connection, so an
  enrolled user can have none. No code outside `app/stats` unregisters counters or online maps, so
  a removed user's counters stay until Xray restarts, and a re-added user continues from the old
  value.
- The online map is reference-counted per open connection and ignores `127.0.0.1` and `[::1]`
  (`app/stats/online_map.go`).
- Xray enables these only with `stats: {}`, `policy.levels.0.statsUserUplink`,
  `statsUserDownlink`, `statsUserOnline`, and `StatsService` in `api.services`. A level policy is
  merged onto the defaults (`app/policy/config.go` `overrideWith`), so these keys leave timeouts
  unchanged. usher creates users at level 0.
- `internal/atomicfile/atomicfile.go:12` `Write` replaces a file with temp file, fsync, rename and
  directory fsync. The temp name is `.atomic-*.tmp`, which the textfile collector ignores because
  it reads only `*.prom`.
- E2E: the AWG suite steps are at `test/e2e/e2e_test.go:144`, its scratch directories at `:47`;
  the Xray suite steps are at `test/e2e/xray/xray_test.go:254`, its directories at `:114`. Both
  suites set `changeTimeout = 10 * time.Second`, shorter than the 30 s refill tick
  (`cmd/usher/main.go:59`). The Xray e2e server config at `test/e2e/xray/server_test.go:84` lists
  only `HandlerService`, as does `deploy/xray/config.json:7`.
- `Dockerfile:16` creates `/srv/usher/{config,clients,state}`. `docs/xray.md` holds the Xray
  config contract; `docs/operations.md` holds operator procedures.

## Development Approach

- **testing approach**: Regular (code first, then tests in the same task)
- complete each task fully before moving to the next
- make small, focused changes
- **CRITICAL: every task MUST include new/updated tests** for code changes in that task
  - tests cover both success and error scenarios
  - table-driven with `t.Run`, beside the source; the awg-grpc and Xray APIs are faked by structs
    with func fields, as the existing tests do
- **CRITICAL: every task ends with `go test -race ./...` passing**; the build never breaks between
  tasks
- **CRITICAL: update this plan file when scope changes during implementation**
- run `scripts/format.sh` before presenting changes
- backward compatibility: without `USHER_STATUS_DIR` usher behaves exactly as today

## Testing Strategy

- **unit tests**: renderer output, AWG peer mapping, Xray stat mapping, Xray config switches, pass
  wiring, env parsing
- **e2e tests**: the AWG suite (Linux host) and the Xray suite (`go test -tags e2e ./test/e2e/xray/`)
  each gain a step that reads the status file

## Acceptance Evidence

There is no defect to reproduce; the feature is measured by its output.

1. `go test -race ./...` passes, including `internal/status` golden-output tests and
   `internal/pass` tests that assert the file content for an applied interface, an interface failed
   in `Prepare`, an interface failed in `Apply`, and a stats-failed interface.
2. `go test -tags e2e -count=1 -v ./test/e2e/xray/ -run 'TestXray'` passes its `stats` step:
   `.e2e-xray/status/usher.prom` holds `usher_user_uplink_bytes_total` and
   `usher_user_downlink_bytes_total` above 0 for the user that fetched through each inbound, and
   `usher_interface_up 1` for every inbound. While alice holds a tunnel open through the Reality
   inbound, `usher_user_online{interface="vless-reality",user="alice"} 1`.
3. On the Linux e2e host, `./e2e.test -test.count=1 -test.v` passes its `stats` step:
   `usher_user_online{user="phone",...} 1` and both byte counters above 0 after the handshake step.
4. Without `USHER_STATUS_DIR`, the existing unit suites pass unchanged, and `TestRunStatusOff` in
   `internal/pass` shows that usher calls no `Stats` and writes no file. Both e2e compose files set
   `USHER_STATUS_DIR`, so no e2e run covers the unset path.

## Progress Tracking

- mark completed items with `[x]` immediately when done
- add newly discovered tasks with ➕ prefix
- document issues/blockers with ⚠️ prefix
- keep plan in sync with actual work done

## Solution Overview

- **Seam.** `Session` gains `Stats`. Each backend maps its own API data to one neutral
  `status.UserStats`; the pass and the renderer never branch on the backend.
- **Metrics are a side channel.** `Stats` runs for every interface that `Prepare` accepted, whether
  or not `Apply` succeeded: a drifted Xray inbound still serves its users, and its series stay. A
  `Stats` error is logged and drops that interface's users from the file; it never fails the
  interface, never changes `usher_interface_up`, never blocks the pass. A missing series means
  "unknown"; usher never writes 0 for a value it could not read.
- **`usher_interface_up`** is 1 when the pass applied the interface, 0 when `Prepare` or `Apply`
  failed it.
- **Online is computed by usher**, because the two backends have no shared raw value. AWG: a
  handshake less than 180 s ago. Xray: the email is in `GetAllOnlineUsers`.
- **No "last seen" metric.** Xray drops `lastSeen` when a user's last connection closes, so usher
  cannot give it for both backends without keeping state. Prometheus derives it from the history of
  `usher_user_online`.
- **No pass timestamp metric.** node_exporter exports `node_textfile_mtime_seconds` for the file;
  a pass that stops early writes nothing, so that metric goes stale.
- **Opt-in by environment.** `USHER_STATUS_DIR` unset means off. Set means usher writes
  `<dir>/usher.prom` every pass and logs every write error.
- **Xray preconditions are read from its config**, so a missing switch gives an error that names
  the keys rather than silent zeros. The error repeats every pass for every inbound, like usher's
  other per-pass errors, until the operator fixes the config.

## Technical Details

### Neutral data and the seam

```go
// internal/status
type UserStats struct {
	Uplink   uint64 // bytes from the client
	Downlink uint64 // bytes to the client
	Online   bool
}

type Interface struct {
	Name  string
	Up    bool
	Users map[string]UserStats // nil when Prepare failed or Stats failed
}

func Render(interfaces []Interface) []byte

// internal/pass, added to Session
Stats(ctx context.Context, st *state.State, name string, users []string) (map[string]status.UserStats, error)
```

`Stats` returns an entry for every user in `users` that the backend has data for, and none for
other users.

### File format

`<USHER_STATUS_DIR>/usher.prom`, mode 0644, replaced through `atomicfile.Write`. Interfaces and
users are sorted; label values are escaped per the text exposition format.

```
# HELP usher_interface_up 1 when the last pass applied the interface.
# TYPE usher_interface_up gauge
usher_interface_up{interface="awg0"} 1
# HELP usher_user_online 1 when the user is connected now.
# TYPE usher_user_online gauge
usher_user_online{interface="awg0",user="alice"} 1
# HELP usher_user_uplink_bytes_total Bytes received from the user.
# TYPE usher_user_uplink_bytes_total counter
usher_user_uplink_bytes_total{interface="awg0",user="alice"} 1234
# HELP usher_user_downlink_bytes_total Bytes sent to the user.
# TYPE usher_user_downlink_bytes_total counter
usher_user_downlink_bytes_total{interface="awg0",user="alice"} 5678
```

### Backend mapping

| Value | AWG (`ListPeers`) | Xray (`StatsService`) |
|---|---|---|
| `Uplink` | `rx_bytes` | `QueryStats` `user>>>tag/user>>>traffic>>>uplink`, 0 when absent |
| `Downlink` | `tx_bytes` | `...>>>downlink`, 0 when absent |
| `Online` | `last_handshake` set and `now - last_handshake < 180s` | `user>>>tag/user>>>online` in `GetAllOnlineUsers` |
| requested user missing | peer not listed: user omitted | never: every requested user gets an entry |

- AWG: one `ListPeers` per interface. Keys map through `publicKeyNames`. The mapping is a pure
  function `peerStats(peers, names, users, now)`; `Stats` passes `time.Now()`.
- Xray: `QueryStats` with pattern `user>>>tag/` per interface and one `GetAllOnlineUsers` per
  session, since its list covers every inbound. Values
  are looked up by exact names built from `EntryName` for the requested users only, so a counter of
  another tag with a shared prefix, or of a removed user that Xray still holds, never appears.
- Xray: `Dial` returns both clients on one connection:
  `type Clients struct { Handler command.HandlerServiceClient; Stats statscmd.StatsServiceClient }`.
- Xray: `readConfig(path) (fileConfig, error)` with
  `fileConfig{Inbounds []inbound; StatsMissing []string}`. It also decodes `stats`,
  `policy.levels."0"` and `api.services`; `StatsMissing` names each absent key. `Open` keeps the
  list on the session; `Stats` returns `stats are off in the xray config: <keys>` when it is
  non-empty, without calling the API.

### Pass flow

`applyAll` records the interfaces it failed apart from those `Prepare` failed. After `applyAll`
and before `mirrorClients`, when `Settings.StatusPath` is set:

1. For every interface of every open backend: an interface failed in `Prepare` becomes
   `{Up: false}` and `Stats` is not called. Otherwise call `Stats` with `byInterface[name]`;
   `Up` is false when `Apply` failed it. On a `Stats` error log `reading stats` with
   backend and interface, and leave `Users` nil.
2. Interfaces of down backends are unknown by name and are left out.
3. `atomicfile.Write(StatusPath, status.Render(...), 0o644)`; on error log `writing status file`.

## What Goes Where

- **Implementation Steps**: code, tests, docs in this repository
- **Post-Completion**: node_exporter flag and bind mount on the server, Prometheus scrape

## Implementation Steps

### Task 1: Renderer for the exposition file

**Files:**
- Create: `internal/status/status.go`
- Create: `internal/status/status_test.go`

- [x] add package `status` with `UserStats`, `Interface` and `Render`, sorted output, HELP and
      TYPE lines, label escaping of `\`, `"` and newline
- [x] write golden-output tests: one up interface with two users, a down interface, an up
      interface with nil `Users`, no interfaces
- [x] write tests for label escaping of a user name with `"`, `\` and a newline
- [x] run `go test -race ./...` - must pass before next task

### Task 2: AWG stats from ListPeers

**Files:**
- Modify: `internal/awg/session.go`
- Modify: `internal/awg/fake_test.go`
- Modify: `internal/awg/session_test.go`

- [x] add method `Session.Stats`: `ListPeers` with the call timeout, then `peerStats`
- [x] add `peerStats(peers, names, users, now)` with the 180 s online window as a named constant
- [x] add a `listPeers` func field to the fake client
- [x] write table tests for `peerStats`: never handshaked, handshake 179 s and 181 s ago, a peer of
      a user not requested, a foreign key, a requested user without a peer
- [x] write tests for `Stats`: success through the fake; a `ListPeers` error is returned wrapped
- [x] run `go test -race ./...` - must pass before next task

### Task 3: Xray Dial returns both clients

**Files:**
- Modify: `internal/xray/session.go`
- Modify: `internal/xray/session_test.go`
- Modify: `cmd/usher/main.go`

- [x] add `xray.Clients{Handler, Stats}` and change `xray.Dial` to return it; `Session` keeps both
- [x] build both clients from one connection in `xrayBackend`
- [x] update the six `Dial` closures in `internal/xray/session_test.go`
- [x] confirm `TestBackends` in `cmd/usher` still covers `xrayBackend` (it builds the backend
      and calls `Open`; the missing config fails before `dial`, so the dial closure is not run)
- [x] run `go test -race ./...` - must pass before next task

### Task 4: readConfig reports the stats switches

**Files:**
- Modify: `internal/xray/config.go`
- Modify: `internal/xray/config_test.go`
- Modify: `internal/xray/session.go`

- [x] change `readConfig` to return `fileConfig{Inbounds, StatsMissing}`; decode `stats`,
      `policy.levels."0"` and `api.services`
- [x] keep `StatsMissing` on the session in `Open`
- [x] write config tests: all switches present gives an empty list; each missing key is named;
      a config with comments still parses; existing inbound tests keep passing
- [x] run `go test -race ./...` - must pass before next task

### Task 5: Xray Session.Stats

**Files:**
- Create: `internal/xray/stats.go`
- Create: `internal/xray/stats_test.go`

- [x] add method `Session.Stats`: return the missing-switches error, else `QueryStats` and
      `GetAllOnlineUsers`, then `userStats(users, tag, counters, online)`
- [x] add a fake `StatsServiceClient` with func fields
- [x] write table tests for `userStats`: counters present, a user without counters gets zeros, an
      online user, a counter of another tag with a shared prefix is ignored, a counter of a
      removed user of the same tag is ignored
- [x] write tests for `Stats`: switches missing returns the error without an API call; a
      `QueryStats` or `GetAllOnlineUsers` error is returned wrapped
- [x] run `go test -race ./...` - must pass before next task

### Task 6: Stats on the Session seam and the pass

**Files:**
- Modify: `internal/pass/backend.go`
- Modify: `internal/pass/settings.go`
- Modify: `internal/pass/pass.go`
- Modify: `internal/pass/pass_test.go`

- [x] add `Stats` to `Session` and `StatusPath` to `Settings`
- [x] make `applyAll` report the interfaces it failed; add `writeStatus` to `Pass`, called from
      `Run` after `applyAll`, following the pass flow above
- [x] extend `fakeSession` with `stats` and `statsErr` maps and a record of `Stats` calls
- [x] write tests: empty `StatusPath` writes nothing and calls no `Stats`; an applied interface
      writes its users; a `Prepare` failure writes `up 0` and `Stats` is not called; an `Apply`
      failure writes `up 0` with users; a stats failure writes `up 1` without users; a down
      backend's interfaces are absent; an unwritable path is logged and `mirrorClients` still runs
- [x] run `go test -race ./...` - must pass before next task

### Task 7: USHER_STATUS_DIR and the image

**Files:**
- Modify: `cmd/usher/env.go`
- Modify: `cmd/usher/env_test.go`
- Modify: `Dockerfile`
- Modify: `deploy/compose.example.yml`
- Modify: `deploy/xray/config.json`

- [x] parse optional `USHER_STATUS_DIR` with `parseAbsPath`; set `StatusPath` to
      `<dir>/usher.prom`
- [x] create `/srv/usher/status` owned by 1000 in the `Dockerfile`
- [x] add the status bind mount and `USHER_STATUS_DIR` to `deploy/compose.example.yml`; add the
      stats switches to `deploy/xray/config.json`
- [x] write env tests: `/srv/usher/status` gives `StatusPath` `/srv/usher/status/usher.prom`;
      unset gives an empty `StatusPath`; a relative path is an error
- [x] run `go test -race ./...` - must pass before next task

### Task 8: E2E stats steps

**Files:**
- Modify: `deploy/compose.e2e.yml`
- Modify: `deploy/compose.e2e-xray.yml`
- Modify: `test/e2e/e2e_test.go`
- Modify: `test/e2e/xray/server_test.go`
- Modify: `test/e2e/xray/xray_test.go`

- [x] add a `statusDir = "status"` constant and add it to `scratchDirs` (AWG) and the directory list
      in `TestMain` (Xray), so the suites create it as the test user
- [x] mount the status directory and set `USHER_STATUS_DIR` in both compose files
- [x] add the stats switches to the Xray e2e server config
- [x] add a `stats` step after `handshake` in the AWG suite: `waitFor` with a `statsTimeout` of
      45 s, longer than one refill tick, until phone is online 1 and both counters are above 0
- [x] add a `stats` step after `connect` in the Xray suite: `waitFor` with the same timeout until
      both counters are above 0 for the fetching user on each inbound and `usher_interface_up` is 1
      for each inbound. Online is not asserted: the fetch has closed its connections by then
- [x] run e2e suite (skipped - no Docker on this machine; compiled with -tags e2e instead)

### Task 9: Verify acceptance criteria

- [x] verify every item of the Overview and the Acceptance Evidence
- ➕ [x] write a name that two backends report once in the status file, so it holds no duplicate
      `usher_interface_up` series (`TestRunStatusNameCollision`)
- [x] run `go test -race ./...`
- [x] run `scripts/format.sh`
- [x] run e2e suites (skipped - no Docker on this machine; compiled with -tags e2e, see Verify it
      yourself)

### Task 10: [Final] Update documentation

- [x] `docs/xray.md`: the required stats switches, the "stats are off" error, the online
      semantics including loopback sources, the counter lifetime
- [x] `docs/operations.md`: the status directory, its bind mount, the node_exporter
      `--collector.textfile.directory` flag, permissions for a non-root node_exporter
- [x] README: the status file, the four metrics with their per-backend semantics, `USHER_STATUS_DIR`,
      the "last seen" PromQL query; links to `docs/xray.md` and `docs/operations.md`
- [x] `docs/architecture/overview.md`: `Stats` in the session table and the pass steps, the
      meaning of `usher_interface_up`, metrics never failing an interface
- [x] `AGENTS.md`: the fake `StatsServiceClient`
- [ ] move this plan to docs/plans/completed/ (left for the delivery step)

## Post-Completion

*Items requiring manual intervention or external systems - no checkboxes, informational only*

**Semantics to state in the PR and the docs**
- AWG online means the tunnel is up; it drops up to 180 s after the client disconnects.
- Xray online means at least one open proxied connection; an idle client with no connection reads
  0, and a quiet connection holds it at 1 until Xray's `connIdle`, 300 s by default. A client that
  reaches Xray from loopback, through a local proxy in front of it, never reads online.
- AWG bytes count encrypted packets including keepalives; Xray bytes count proxied payload.
- AWG counters reset when the peer is recreated or the interface restarts. Xray counters reset only
  when Xray restarts; a removed and re-added user continues from the old value. `rate()` and
  `increase()` handle resets.
- Last seen: `max_over_time((timestamp(usher_user_online == 1))[30d:1m])`.

**Deployment**
- Enable the Xray stats switches (`docs/xray.md`) and restart Xray.
- Add `--collector.textfile.directory` to node_exporter and the status bind mount to usher
  (`docs/operations.md`). A non-root node_exporter needs the directory traversable and the file
  readable by others.

**Executed by exec:**
- branch: status-metrics

## Verify it yourself

No e2e suite ran during execution: this machine has no Docker. Both suites compile for
linux/amd64 with `-tags e2e`. The checks below close Acceptance Evidence items 2 and 3.

1. Unit evidence (items 1 and 4), on any machine:
   `go test -race -count=1 ./internal/status ./internal/pass ./internal/awg ./internal/xray ./cmd/usher`
   passes, including `TestRender`, `TestRunStatus`, `TestRunStatusOff`, `TestRunStatusCancelled`,
   `TestRunStatusNameCollision`, `TestPeerStats`, `TestUserStats` and
   `TestXrayBackendDialsBothServices`. On `origin/master` these tests do not exist.
2. Xray e2e (item 2), on a host with Docker:
   `go test -tags e2e -count=1 -v ./test/e2e/xray/ -run TestXray`. The `stats` step must pass;
   after the run, `.e2e-xray/status/usher.prom` holds `usher_interface_up{interface=...} 1` for
   every inbound and byte counters above 0 for alice and bob. The online assertion for alice on
   `vless-reality` is the one check never exercised against a real Xray.
3. AWG e2e (item 3), on the Linux host with the `amneziawg` module, as `AGENTS.md` describes:
   `GOOS=linux GOARCH=amd64 go test -c -tags e2e -o e2e.test ./test/e2e`, copy, then
   `./e2e.test -test.count=1 -test.v`. The `stats` step must pass with
   `usher_user_online{interface=...,user="phone"} 1` and both counters above 0.
4. On the server, after deploying an image built from this branch and the setup in
   `docs/operations.md`, "Stats with node_exporter": node_exporter's `/metrics` holds one
   `usher_interface_up` per interface and a line per user; before the deploy it holds none.
