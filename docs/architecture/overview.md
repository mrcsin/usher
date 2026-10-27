# usher overview

## Purpose

usher keeps AmneziaWG peers, Xray users and client files in step with `config/usher.yml`, a map of
user name to interface names. An interface is an AmneziaWG interface or a tagged inbound of the
operator's Xray config. The operator types names only. Addresses and keys are internal.

## Stack

Go, one binary: `usher run`. It talks gRPC to awg-grpc over the socket in `USHER_AWG_SOCKET`, and
to Xray's `HandlerService` and `StatsService` over the socket in `USHER_XRAY_SOCKET`. At least one
is set. It runs in a container with no capabilities, a read-only root file system and uid 1000.
The Xray backend builds inbounds with `xray-core` itself and renders share links with `libxray`, so
the Xray image must match the `xray-core` version in `go.mod`.

## Components

| Package | Responsibility |
|---|---|
| `cmd/usher` | Reads the `USHER_*` environment, builds the backends, wires signals, the watcher and the pass. |
| `internal/config` | Loads and validates `usher.yml`. |
| `internal/state` | Loads and saves `state/users.json`. |
| `internal/atomicfile` | Replaces a file atomically for `state`, the `clients/` mirror and the status file. |
| `internal/status` | Renders the per-user stats of a pass as the Prometheus text file. |
| `internal/pass` | Settings, the `Backend` and `Session` seam and the reconcile pass. |
| `internal/awg` | The awg backend: enrollment, address allocation, `.conf` rendering. |
| `internal/xray` | The Xray backend: config reader, drift guard, users, mirror, `.json` and `.txt` rendering, stale socket recovery. |
| `internal/clients` | Paths of client files and the mirror of the `clients/` directory. |
| `internal/watch` | Polls the file, runs the refill ticker and serializes passes. |

## The pass

A backend is a `Backend{Name, Suffixes, Open}`. `Open` returns a `Session` with `Interfaces`,
`Prepare` (enroll users, render files), `Apply`, `Stats` (traffic and online state per user) and
`Close`. `Suffixes` lists the file suffixes the backend owns. Each backend maps its own API data to
one `status.UserStats`, so the pass and the renderer never branch on the backend.

| Step        | awg                                              | Xray                                                        |
| ----------- | ------------------------------------------------ | ----------------------------------------------------------- |
| `Open`      | `GetStatus`                                      | read the config, remove a stale socket, `ListInbounds`, drift check |
| `Interfaces`| every interface awg-grpc reports                 | every managed inbound tag of the config                     |
| `Prepare`   | fails an interface that is not present           | returns the structural error of an inbound                  |
| `Apply`     | `ApplyPeers`, `allow_empty` with no users        | `GetInboundUsers`, then `AlterInbound` per changed user     |
| `Stats`     | `ListPeers`; a user without a peer is left out   | `QueryStats` per inbound and one `GetAllOnlineUsers` per session, looked up by exact `user>>>tag/user>>>` names; an error without an API call when the config lacks a stats switch |
| Suffixes    | `.conf`                                          | `.json`, `.txt`                                             |

1. Load `usher.yml`. When it is invalid, use the last valid file of this process. With none,
   log and stop. Load `users.json`. On error, log and stop.
2. `Open` every backend. A backend that fails to open is down for this pass: usher keeps every
   file with one of its suffixes, and the other backends go on.
3. For each interface of each open backend: fail it when two backends report its name. Otherwise
   `Prepare` it: enroll every user that references it and render their files. An error fails the
   interface.
4. Save `users.json` when step 3 created or changed an entry. On error, log and stop.
5. `Apply` each interface that did not fail. An error fails the interface.
6. When `USHER_STATUS_DIR` is set, write the status file. Each interface of each open backend gets
   `usher_interface_up` 1 when it was applied and 0 when it failed. `Stats` runs for every
   interface that step 3 did not fail, applied or not, because an inbound with a usable drift
   still serves its users. A `Stats` error is logged as `reading stats` and leaves the users
   of that interface out; it never fails the interface and never changes `usher_interface_up`. A
   name that two backends report is written once. `atomicfile.Write` replaces the file with mode
   0644; a write error is logged and the pass goes on.
7. When no backend is down, log every user and interface pair that no backend reports ("interface
   is not reported by any backend").
8. Make `clients/` equal to the rendered files of applied interfaces plus the current files of
   failed interfaces and down backends.

A pass runs at start, after a change of `usher.yml` and at every 30 s refill tick. One goroutine
runs the watcher loop and the passes, with the poll ticker, the settle timer and the refill ticker
as cases of one `select`. A tick that fires during a pass waits in its ticker, which buffers one
tick, so each source yields at most one following pass. The watcher calls `os.Stat` every 3 s, and
a new modification time counts once it has stayed unchanged for 500 ms. Each backend opens its
connection per pass, and every call has a 10 s deadline.

## AmneziaWG keys and addresses

- Entries are keyed `interface/user` under the `awg` group of `users.json`.
- A new entry gets an X25519 key pair (`crypto/ecdh`), 32 random PSK bytes and an address.
- The address is the lowest host address of the interface's first IPv4 prefix that is not the
  network or broadcast address, not an interface address and not held by another entry of that
  interface. A stored address that breaks one of these rules, for example after the operator
  changes the interface address, is reassigned and logged, and the config is rewritten.
- Switching a user off removes the peer and the client file and keeps the entry, so the same
  name gets the same key and address back. usher never deletes entries.
- Addresses are never freed: switched-off users keep theirs. A full subnet fails the whole
  interface, refill included. awg-grpc accepts at most 1000 peers per interface.
- A change of the interface key, port or `client_params` rewrites every client config of that
  interface; those configs are handed out again.

## Xray backend

### Ownership

- The operator owns one Xray server config: inbounds, outbounds, routing, `api`, certificates,
  Reality keys, and a per-inbound `x-usher` block with client hints. Xray reads it at start. usher
  reads the same file read-only at every pass.
- usher owns the users inside the managed inbounds, through the API, and everything under
  `clients/`. Xray keeps users in memory, so the refill tick restores them after a restart.
- A managed inbound has a tag and a protocol of vless, hysteria, vmess or trojan. Other inbounds
  are skipped.

### Errors of an inbound

`validate` puts every problem of an inbound into one of two classes:

- A structural error (port, `listen`, `clients`, decryption, transport, security, Hysteria
  fields, masks, xhttp, Reality keys, a decode failure, an invalid or duplicate tag). `Prepare`
  returns it, so the pass skips `Apply` and touches no user of the tag.
- A hint error (`x-usher`), an enroll error or a render error. `Prepare` keeps it and returns
  none, so the pass calls `Apply`, which removes the users who left and then returns the error.
  `validate` runs the hint check last, so `checkDrift` still compares an inbound with a hint
  error.

A tag defined twice is reported once, and its entry carries the error.

### Reading the config

`readConfig` reads the file with Xray's comment-aware reader into `infra/conf` types plus raw maps.
It normalizes the aliases (`method`, `splithttp`, `websocket`, `tcp`, `splithttpSettings`,
`tcpSettings`, Hysteria `users`) before validation and mirroring. Every error names the field and
never a value. An inbound without `streamSettings` stays without them in `Raw`, so it builds as
Xray builds the file.

### Users

- The email is `tag/user`, level 0. State keeps `id` (UUID v4) for VLESS and VMess, `password`
  (base64url of 32 random bytes) for Trojan and Hysteria2, under the `xray` group. enroll never
  changes an existing entry.
- `Apply` lists all users of the inbound with `GetInboundUsers` and compares the credential only:
  VLESS `id` and `flow`, VMess `id`, Trojan `password`, Hysteria `auth`. It removes foreign and
  changed emails and adds missing and changed ones. It tries every operation, joins the errors and
  logs `users changed`. The next pass converges from the listed state.
- When the running inbound is missing, runs another protocol or could not be compared, `Apply`
  changes nothing: an account of the wrong type makes Xray's validator panic and kills the process.

### Drift guard

`Open` calls `ListInbounds` once and compares each valid inbound with `Build()` of its file entry:
proxy type, proxy settings, port, stream settings and receiver settings. Certificate entries that
name a file are dropped on both sides, because Xray reloads those files itself. A tag missing at
runtime counts as drift. The proxy type of the running handler is compared with the file's protocol
before any `Build()`; a mismatch, or a file entry that does not decode or build, marks the drift
unusable. The reason names the differing parts; an error of `Build()` becomes a fixed text, because
Xray quotes the keys and short ids it rejects. On a usable drift `Apply` reconciles users and then
returns the drift error, so the old client files stay. On an unusable drift it changes nothing. A
running managed inbound that the file does not define is logged each pass.

### Stale socket

A killed Xray leaves its API socket, and a new start fails on it. Xray holds `flock(LOCK_EX)` on
`<socket>.lock` while it listens. Before dialing, usher opens the lock read-only and tries
`LOCK_EX|LOCK_NB`. When it gets the lock, no Xray listens: usher unlinks the socket and reports Xray
down for this pass. The restart policy starts Xray at its next attempt. A failed Xray start removes
the lock file, so for a socket without one usher probes the socket instead: only a refused
connection counts as stale, and a live Xray, or a socket that a new Xray replaced meanwhile, stays.
A path that is not a socket is an error.

### Client mirror

- Taken from the server: `network`, `security`, path and host of ws and httpupgrade,
  `serviceName` and `authority` of grpc. Everything else of the stream settings is dropped.
- xhttp: path, host and mode stay at the top. A server `extra` is copied verbatim; otherwise every
  other field moves into `extra`. `.json` and `.txt` carry the same form. `downloadSettings` fails
  the inbound.
- VLESS `flow` is `xtls-rprx-vision` when the network is raw and the security is tls or reality.
- `tlsSettings` is rebuilt: server name, ALPN, fingerprint and, with `trust: pin`,
  `pinnedPeerCertSha256`.
- `realitySettings` is rebuilt: public key from `privateKey` (`crypto/ecdh`), `shortIds[0]`,
  `spiderX` `/`, `mldsa65Verify` from `mldsa65Seed`.
- Hysteria2: the client `auth` is the user's password. From salamander only `password` is copied.
  `hop_ports` adds a client `udphop` mask with mode `intervalLocal,intervalRemote` and interval 30.

### Outputs

- `clients/<user>/<tag>.json` is a complete client config: the outbound plus a socks inbound on
  127.0.0.1:10808 and an http inbound on 127.0.0.1:10809. The outbound must pass
  `conf.OutboundDetourConfig.Build()` before any file is written, or the inbound fails.
- `clients/<user>/<tag>.txt` comes from libxray `ConvertXrayJsonToShareLinks`. usher builds only
  shapes libxray knows, so a refusal is a usher defect: it is logged with the tag and no `.txt` is
  written. A Hysteria2 inbound with `trust: pin` gets no `.txt`.

## State

`state/users.json` is the only source of secrets; the kernel, Xray and `clients/` derive from it.
It is version 2: the `awg` group keyed `interface/user` and the `xray` group keyed `tag/user`. An
`xray` entry holds exactly one of `id` and `password`. usher saves it (temp file, fsync, rename,
directory fsync, mode 0600) only when an entry changed, and always before any backend's `Apply`.
`Load` accepts versions 1 and 2. Before the first save of a version 1 file usher writes
`users.json.v1` beside it, so a rollback is a file rename.

`clients/` belongs to usher: anything not rendered from the applied state is removed. Files are
mode `0600`, uid 1000.
