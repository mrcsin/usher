# usher_interface_up 0 for unreported interfaces

## Overview

- An interface that `usher.yml` references and no open backend reports gets
  `usher_interface_up{interface} 0` in `usher.prom`, with no user series. This covers a backend
  that is down and an interface that a running backend stops reporting, such as a removed Xray
  inbound.
- Alert rules then need no interface names: `usher_interface_up == 0` catches every referenced
  interface that the last pass did not apply.
- Interfaces that an open backend reports keep their current status lines.
- Implements usher issue #12.

## Context (from discovery)

- `internal/pass/pass.go:131` `writeStatus` loops over `open` and `b.session.Interfaces()` only.
  It already takes `byInterface`, the users of each interface from `usher.yml`, built by
  `usersByInterface` (`internal/pass/pass.go:238`). `written` dedupes a name that two backends
  report and holds every reported name, colliding ones included.
- `Run` calls `writeStatus` only when `StatusPath` is set and the pass is not cancelled
  (`internal/pass/pass.go:74`). `loadConfig` falls back to the last valid config, so
  `byInterface` comes from that config when the current file is invalid; the same map drives
  `Prepare` and `Apply`.
- `logUnknown` logs "interface is not reported by any backend" only when no backend is down
  (`internal/pass/pass.go:79`).
- `internal/status/status.go:47` `Render` sorts interfaces by name and writes `Up` as 0 or 1; a
  nil `Users` map writes no user series. The doc comment of `status.Interface`
  (`internal/status/status.go:21`) says "Users is nil when Prepare or Stats failed".
- `internal/awg/session.go:39` `Open` dials the awg-grpc socket and calls `GetStatus`. A backend
  that fails `Open` is unreachable to usher; the kernel interface can still forward for enrolled
  peers.
- Tests: `TestRunStatusBackendDown` (`internal/pass/pass_test.go:693`) expects only `vless` when
  `awg0` is referenced and its backend is down. Helpers `newPassEnv`, `enableStatus` (`:176`),
  `statusFile` (`:182`); `fakeSession.names` sets the reported interfaces. Every status test
  compares the exact file content against `status.Render`.
- e2e: `test/e2e/e2e_test.go:232` and `test/e2e/xray/xray_test.go:308` read `usher.prom` through
  `readStatus` and assert specific samples present; none asserts an absent series, and no
  scenario references an interface that no backend reports.
- Docs:
  - `README.md:159`: the `usher_interface_up` value cell reads "1 when the last pass applied the
    interface, 0 when it failed it".
  - `README.md:184`: "The interfaces of a backend that is down are absent."
  - `docs/architecture/overview.md:57-58`: step 6 gives `usher_interface_up` to "each interface of
    each open backend".
  - `docs/operations.md:43-70`: `### Alerts` opens with "usher reports most failures by leaving
    series out", lists two bullets (the first ends "Each interface needs an `absent` rule of its
    own"), and a rules block with `UsherInterfaceDown`, `UsherInterfaceAbsent` (one `absent()`
    term per interface name), `UsherStatusEmpty`, `UsherStatusStale`.

## Development Approach

- **testing approach**: Regular (code first, then tests)
- complete each task fully before moving to the next
- **CRITICAL: every task MUST include new/updated tests** for code changes in that task
- **CRITICAL: all tests must pass before starting next task**
- **CRITICAL: update this plan file when scope changes during implementation**

## Testing Strategy

- unit tests in `internal/pass/pass_test.go` through `Run`, the only production entry point
- the e2e suites need no change: they assert present samples only, and their scenarios have no
  referenced interface that no backend reports

## Acceptance Evidence

- Today: `go test -race -run TestRunStatusBackendDown ./internal/pass/` passes with a file that
  holds only `usher_interface_up{interface="vless"} 1`; `awg0` is absent.
- After: these commands pass, and the tests assert the exact file content:

  ```sh
  go test -race -run 'TestRunStatusBackendDown|TestRunStatusUnreportedInterface|TestRunStatusAllBackendsDown' ./internal/pass/
  go test -race ./...
  scripts/format.sh
  ```

  `TestRunStatusBackendDown` expects `usher_interface_up{interface="awg0"} 0` and
  `usher_interface_up{interface="vless"} 1`, with no user series for `awg0`.

## Solution Overview

- After the existing loop, `writeStatus` appends `status.Interface{Name: name}` (`Up` false, nil
  `Users`) for each name in `byInterface` that no open backend reports. `byInterface` is already
  the set of referenced interfaces. `Run` passes `owners`, the result of `interfaceOwners`, so
  `writeStatus` and `logUnknown` test "reported by an open backend" with the same map.
  `written` only dedupes a name that two backends report.
- `Stats` is not called for an unreported interface: no backend owns it.
- `usher_interface_up 0` means the last pass did not apply the interface: it failed, its backend
  is down, or no backend reports it. It does not mean the users cannot connect: an AmneziaWG
  interface keeps forwarding while awg-grpc is unreachable.
- The log tells the causes apart only in part. "interface failed" and "backend is down" are always
  logged; "interface is not reported by any backend" only when no backend is down. With one
  backend down, a removed inbound of the other backend reads 0 with no log line of its own.
- A name in `usher.yml` that no backend has ever had, such as a typo, gives a permanent 0, so
  `UsherInterfaceDown` fires.
- An interface that no user references still leaves the file when its backend is down. It has no
  users, so the file loses nothing.
- A referenced interface keeps one series that moves between 1 and 0; it leaves the file only when
  its last user leaves `usher.yml`.
- `absent(usher_interface_up)` fires when node_exporter serves no `usher_interface_up` at all: the
  file is missing or unreadable. A pass that writes the file always has a valid `usher.yml` with
  at least one user/interface pair, so the file always holds a series. The rule stays.

## Technical Details

- `Render` sorts by name, so the append order and the map iteration order do not matter.
- The cancelled-pass guard at `internal/pass/pass.go:74` is unchanged, so a cancelled pass still
  writes no file.

## What Goes Where

- Implementation Steps: `internal/pass`, `internal/status`, `README.md`,
  `docs/architecture/overview.md`, `docs/operations.md`.
- Post-Completion: operator alert rules, outside this repository.

## Implementation Steps

### Task 1: Write unreported interfaces in writeStatus

**Files:**
- Modify: `internal/pass/pass.go`
- Modify: `internal/status/status.go`
- Modify: `internal/pass/pass_test.go`

- [x] in `writeStatus` (`internal/pass/pass.go:132`), after the loop, append
      `status.Interface{Name: name}` for each name of `byInterface` that `owners` does not hold
- [x] update the doc comment of `writeStatus` to name the referenced interfaces
- [x] update the doc comment of `status.Interface` (`internal/status/status.go:21`): `Users` is
      also nil for an interface that no backend reports
- [x] update `TestRunStatusBackendDown` to expect `awg0` with `Up: false` next to `vless`
- [x] add `TestRunStatusUnreportedInterface`: one open backend reports `awg0`, `usher.yml`
      references `awg0` and `vless`; the file holds `awg0` 1 with users and `vless` 0 without
      users, and `Stats` is not called for `vless`
- [x] add `TestRunStatusAllBackendsDown`: both backends fail to open; the file holds every
      referenced interface with 0 and no user series
- [x] add `TestRunStatusInterfaceLeaves`: a reported `vless` reads 1, reads 0 once the backend
      stops reporting it, and leaves the file once its last user leaves `usher.yml`
- [x] add `TestRunStatusBackendDownHidesUnreported`: with one backend down, a referenced name the
      open backend does not report reads 0 and gets no "interface is not reported" log line
- [x] add `TestRunStatusInvalidConfig`: an invalid `usher.yml` keeps the referenced names of the
      last valid one in the file
- [x] run `go test -race ./internal/pass/ ./internal/status/` - must pass before task 2

### Task 2: Verify acceptance criteria

- [x] run the commands of Acceptance Evidence
- [x] confirm `TestRunStatusCancelled` and `TestRunStatusNameCollision` pass unchanged

### Task 3: [Final] Update documentation

**Files:**
- Modify: `README.md`
- Modify: `docs/architecture/overview.md`
- Modify: `docs/operations.md`

- [x] `README.md:159`: the value cell also names 0 for a referenced interface that no open
      backend reports
- [x] `README.md:184`: replace "The interfaces of a backend that is down are absent" with: an
      interface that `usher.yml` references and no open backend reports reads 0 with no user
      series; an unreferenced interface of a backend that is down is absent
- [x] `docs/architecture/overview.md:57-58`: step 6 also writes 0 for each referenced interface
      that no open backend reports, and calls no `Stats` for it
- [x] `docs/operations.md:43-51`: rewrite the `### Alerts` intro and bullets. A referenced
      interface that the pass did not apply reads 0, whatever the cause, and the log names the
      cause except for an unreported interface while another backend is down;
      `absent(usher_interface_up)` fires when node_exporter serves no usher series; a stale file
      keeps its last values and only its age shows the failure
- [x] `docs/operations.md:60-64`: delete the `UsherInterfaceAbsent` rule; keep
      `UsherInterfaceDown`, `UsherStatusEmpty`, `UsherStatusStale`
- [ ] move this plan to `docs/plans/completed/` (at delivery)

## Post-Completion

**External system updates:**
- an operator who alerts with per-interface `absent()` terms can replace them with the three rules
  from `docs/operations.md`

**Executed by exec:**
- branch: feat/status-unreported-interfaces

## Verify it yourself

1. Run the status tests on the branch: `go test -race -count=1 -run TestRunStatus ./internal/pass/`.
   All pass at `a32973e`.
2. Run the same test file against the code before the change. At `979b987` with the branch's
   `internal/pass/pass_test.go`, six tests fail: `TestRunStatusBackendDown`,
   `TestRunStatusUnreportedInterface`, `TestRunStatusInterfaceLeaves`,
   `TestRunStatusBackendDownHidesUnreported`, `TestRunStatusInvalidConfig`,
   `TestRunStatusAllBackendsDown`. The old `writeStatus` leaves `awg0` out of the file when its
   backend is down; the new one writes `usher_interface_up{interface="awg0"} 0`.
3. On a running deployment, stop the awg-grpc container and wait one pass. `usher.prom` keeps
   `usher_interface_up{interface="<awg interface>"} 0` instead of dropping the line, and
   `UsherInterfaceDown` fires after 2 minutes.
4. Remove an Xray inbound that `usher.yml` still references and wait one pass. Its
   `usher_interface_up` line reads 0, and the log has `interface is not reported by any backend`.
