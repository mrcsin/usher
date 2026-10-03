# usher overview

## Purpose

usher keeps AmneziaWG peers and client configs in step with `config/usher.yml`, a map of user name
to interface names. The operator types names only. Addresses, keys and the subnet are internal.

## Stack

Go, one binary: `usher run`. It talks gRPC to `awg-grpc` (`github.com/mrcsin/awg-grpc` v0.1.0)
over the unix socket `/run/awg-grpc/awg.sock`. It runs in a container with no capabilities, a
read-only root file system and uid 1000. Config parsing uses `gopkg.in/yaml.v3`.

## Components

| Package | Responsibility |
|---|---|
| `cmd/usher` | Reads `USHER_HOST` and `USHER_DNS`, wires signals, the watcher and the pass. |
| `internal/config` | Loads and validates `usher.yml`. |
| `internal/state` | Loads and saves `state/users.json`. |
| `internal/atomicfile` | Replaces a file atomically for `state` and the `clients/` mirror. |
| `internal/pass` | Settings, enrollment, address allocation and the reconcile pass. |
| `internal/clients` | Renders client configs and mirrors the `clients/` directory. |
| `internal/watch` | Polls the file, runs the refill ticker and serializes passes. |

## The pass

1. Load `usher.yml`. When it is invalid, use the last valid file of this process. With none,
   log and stop. Load `users.json`. On error, log and stop.
2. Call `GetStatus`. On error, log and stop.
3. For each reported interface: fail it when `present=false`. Otherwise enroll every user that
   references it and render their configs. An enrollment or render error fails the interface.
4. Save `users.json` when step 3 created or changed an entry.
5. For each interface that did not fail: call `ApplyPeers` with one peer per referencing user
   (`address/32`), with `allow_empty` when no user references the interface. An error fails the
   interface.
6. Log every user and interface pair whose interface `awg-grpc` does not report.
7. Make `clients/` equal to the rendered files of applied interfaces plus the current files of
   failed ones.

Two triggers start a pass: a change of `usher.yml` and a 30 s refill ticker. One goroutine runs
the watcher loop and the passes, with the poll ticker, the settle timer and the refill ticker as
cases of one `select`. A tick that fires during a pass waits in its ticker, which buffers one tick,
so each source yields at most one following pass. A refill tick and a file change during one pass
give two following passes. File polling follows `reproxy` (`app/discovery/provider/file.go`): `os.Stat` every 3 s, and a new
modification time counts once it has stayed unchanged for 500 ms. The connection is opened per
pass and every call has a 10 s deadline.

## Keys and addresses

- `state/users.json` is the only source of secrets. Entries are keyed `interface/user` under the
  `awg` group. The kernel and `clients/` derive from it. usher saves it (temp file, fsync, rename,
  directory fsync, mode 0600) only when an entry changed, and always before `ApplyPeers`.
- A new entry gets an X25519 key pair (`crypto/ecdh`), 32 random PSK bytes and an address.
- The address is the lowest host address of the interface's first IPv4 prefix that is not the
  network or broadcast address, not an interface address and not held by another entry of that
  interface. A stored address that breaks one of these rules, for example after the operator
  changes the interface address, is reassigned and logged, and the config is rewritten.
- Switching a user off removes the peer and the client file and keeps the entry, so the same
  name gets the same key and address back. usher never deletes entries.

## Locked decisions

- The scope is AmneziaWG over IPv4. The `users.json` grouping leaves room for Xray.
- usher owns every interface the wrapper reports, and an unreferenced interface gets an empty
  list.
- A broken `usher.yml` never changes state. The last valid file is kept in memory only.
- A file without any user and interface pair is invalid, so a truncated file cannot wipe a node.
- A failure of one interface never stops the daemon and never blocks the others.
- An interface without an IPv4 prefix fails only when a user references it.
- `clients/` files of an interface that `awg-grpc` no longer reports are removed.
- `USHER_HOST` is an IPv4 address, not a name: a client whose DNS points into the tunnel cannot
  resolve the name again to reconnect.
- No private key, PSK or `client_params` value is logged. A public key without an entry is logged
  as base64.
- No subcommands besides `usher run`, no central management, no migration of existing peers.

## Operations

- `clients/` belongs to usher: anything not rendered from the applied state is removed. Files are
  mode `0600`, uid 1000.
- Changing an interface's address, key, port or obfuscation values rewrites every client config of
  that interface; those configs are handed out again.
- Addresses are never freed: switched-off users keep theirs. A full subnet fails the whole
  interface, refill included. awg-grpc accepts at most 1000 peers per interface.
- `users.json` sits in the `usher-state` volume (mode `0600`, uid 1000). To re-key or delete a user:
  `docker compose stop usher`, then
  `docker run --rm -it -u 1000:1000 -v <project>_usher-state:/state alpine vi /state/users.json`,
  delete the `<interface>/<user>` entry, `docker compose start usher`. A user still in `usher.yml`
  gets new keys.
- `USHER_HOST` is an address, not a name: a client that re-resolves a name while its DNS points
  into the tunnel cannot reconnect.
- Client isolation and DNS redirection belong to the service config, not to usher. For AmneziaWG:
  `FORWARD` drops between clients in `PostUp`; a resolver on the tunnel address (for example
  cloudflared with a DoH upstream in the `awg` network namespace) with `USHER_DNS` set to that
  address, and optionally a `DNAT` of port 53 in `PostUp`.
