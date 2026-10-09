# usher overview

## Purpose

usher keeps AmneziaWG peers, Xray users and client files in step with `config/usher.yml`, a map of
user name to interface names. The operator types names only. Addresses, keys, UUIDs and the subnet
are internal. usher creates no interface and starts no process; delivery prepares each VPN
container and usher only moves users into it.

## Stack

Go, one binary: `usher run`. It talks gRPC over unix sockets to two backends, each enabled by its
own variable: `awg-grpc` (`github.com/mrcsin/awg-grpc` v0.1.0) at `USHER_AWG_SOCKET`, and the
`HandlerService` API of a running Xray at `USHER_XRAY_SOCKET`. At least one must be set. It runs
in a container with no capabilities, a read-only root file system and uid 1000. Config parsing
uses `gopkg.in/yaml.v3`. usher imports no xray-core Go code: the Xray protos are vendored in
`proto/xray` and generated into `gen/xray` with buf.

## Components

| Package | Responsibility |
|---|---|
| `cmd/usher` | Reads the environment, builds one `pass.Backend` per set socket, wires signals, the watcher and the pass. |
| `internal/config` | Loads and validates `usher.yml`. |
| `internal/state` | Loads and saves `state/users.json`. |
| `internal/atomicfile` | Replaces a file atomically for `state` and the `clients/` mirror. |
| `internal/pass` | Settings, the `Session` and `Backend` seam, and the reconcile pass. |
| `internal/awg` | The AmneziaWG backend: enrollment, address allocation, config rendering, `ApplyPeers`. |
| `internal/xray` | The Xray backend: inbound decoder tables, enrollment, user reconcile, `vless://` links. |
| `internal/clients` | Mirrors the `clients/` directory. |
| `proto/xray`, `gen/xray` | Vendored Xray protos and the generated Go code. |
| `tools` | Separate Go module that pins buf and the protobuf plugins. |
| `internal/watch` | Polls the file, runs the refill ticker and serializes passes. |

## The backend seam

`pass.Backend` has a name for logs, the suffix of its client files (`.conf`, `.txt`) and an `Open`
function. `Open` connects and lists the backend's interfaces, and returns a `pass.Session`, which
prepares one interface (enrolls entries, renders client files, reports whether it changed the
state) and applies one interface (sends the desired users). The pass owns the rest: config load,
the state save before any apply, failure isolation per interface and the `clients/` mirror. Each
backend sees only its own interfaces. Environment variables select backends; `cmd/usher` closes
over each socket path and the server facts the driver needs (`USHER_HOST` for both, `USHER_DNS`
for awg).

## The pass

1. Load `usher.yml`. When it is invalid, use the last valid file of this process. With none,
   log and stop. Load `users.json`. On error, log and stop.
2. Open a session per backend. A backend that fails to open is down: log `backend is down` and
   go on with the others.
3. For each interface a session reports: fail it when two backends report the same name, with an
   error that names both. Otherwise call `Prepare`: enroll every user that references it and
   render their files. An enrollment or render error fails the interface. The awg driver also
   fails an interface with `present=false`.
4. Save `users.json` when step 3 created or changed an entry.
5. For each interface that did not fail: call `Apply` with the users that reference it, which may
   be none. An error fails the interface.
6. Log every user and interface pair whose interface no backend reports. Skip this step while a
   backend is down, because its names are unknown.
7. Make `clients/` equal to the rendered files of applied interfaces plus the current files of
   failed ones, and of every interface of a down backend. A backend owns the files that end in
   its suffix.

The awg session calls `GetStatus` when it opens. Its `Apply` calls `ApplyPeers` with one peer per
referencing user (`address/32`), with `allow_empty` when no user references the interface. The
Xray session is described under "The Xray backend".

Two triggers start a pass: a change of `usher.yml` and a 30 s refill ticker. One goroutine runs
the watcher loop and the passes, with the poll ticker, the settle timer and the refill ticker as
cases of one `select`. A tick that fires during a pass waits in its ticker, which buffers one tick,
so each source yields at most one following pass. A refill tick and a file change during one pass
give two following passes. File polling follows `reproxy` (`app/discovery/provider/file.go`): `os.Stat` every 3 s, and a new
modification time counts once it has stayed unchanged for 500 ms. The connection is opened per
pass and every call has a 10 s deadline. A down backend fails to open on every pass, so the
refill ticker retries it.

## Keys and addresses

- `state/users.json` is the only source of secrets. It has `"version": 2` and two groups: `awg`
  and `xray`, both keyed `interface/user` (`tag/user` for Xray). The kernel, Xray and `clients/`
  derive from it. usher saves it (temp file, fsync, rename, directory fsync, mode 0600) only when
  an entry changed, and always before the first `Apply`. Version 1 loads with an empty `xray`
  group; `Save` always writes 2, so the previous image cannot read the file afterwards.
- A new awg entry gets an X25519 key pair (`crypto/ecdh`), 32 random PSK bytes and an address.
- The address is the lowest host address of the interface's first IPv4 prefix that is not the
  network or broadcast address, not an interface address and not held by another entry of that
  interface. A stored address that breaks one of these rules, for example after the operator
  changes the interface address, is reassigned and logged, and the config is rewritten.
- A new Xray entry gets a random UUID (`uuid.NewV4`). usher never changes an existing entry, so a
  UUID put into `users.json` by hand survives every pass. A zero UUID fails the load.
- Switching a user off removes the peer or the Xray user and the client file, and keeps the entry,
  so the same name gets the same credentials back. usher never deletes entries.

## The Xray backend

usher reads an Xray inbound in three independent parts, and each part is a table of decoder
functions in `internal/xray`:

| Table | File | Key | Entry today |
|---|---|---|---|
| `protocols` | `protocol.go` | type name of the inbound's proxy settings | VLESS |
| `transports` | `transport.go` | stream `protocol_name` | `tcp` |
| `securities` | `security.go` | stream `security_type` (the settings message type) | Reality |

`listInbounds` calls `ListInbounds`, drops every inbound whose proxy settings type has no
`protocols` entry (such as the `api` tunnel) and every inbound without a tag, and decodes the
rest. An inbound that fails to
decode becomes a failed interface with the error; the others are unaffected. A transport or
security without a table entry fails the inbound with an error that names it, and an empty
security type is named `none` in the error.

- The receiver port list must hold one range with `From == To`.
- The VLESS entry requires `decryption` `none` and builds the account `{id, flow}` with the flow
  `xtls-rprx-vision`, which it also puts into the link. It owns the whole link, so a protocol
  whose link is not scheme plus query still fits.
- The `tcp` entry accepts raw TCP with no transport settings, with settings that have no header,
  and with the no-op header (any message in the proto package
  `xray.transport.internet.headers.noop`). Any other header fails the inbound.
- The Reality entry reads the server's settings and keeps the private key inside the function. It
  returns `pbk`, the X25519 public key in unpadded URL-safe base64; `sid`, the lowercase hex of the
  first short ID with trailing zero bytes removed, so an all-zero ID gives an empty `sid`; `sni`,
  the first non-empty server name, failing the inbound when there is none; and `fp=chrome`.
  `mldsa65_seed` is ignored and the link has no `pqv`.
- The supported shape is therefore VLESS + raw TCP without header + Reality + `decryption: none`.

To add a shape, add one table entry, vendor the proto that describes its settings into
`proto/xray` and regenerate `gen/xray`, and write the entry's tests. A new protocol also needs a
field in `state` when its credential is not a UUID, plus an `enroll` for it.

`Apply` reconciles one inbound. It calls `GetInboundUsers`, and compares the current users with the
desired ones by email, which is the entry name `tag/user`. A current email that is not desired is
removed. One whose account differs (same type name and `proto.Equal` of the decoded messages;
the code treats an unknown account type as different) is removed and added again. A desired email
that is missing is added. Each change is one `AlterInbound` with level 0. An error fails the
inbound, and the next pass converges a partial change. usher logs `users changed` with the
`added`, `removed` and `updated` user names. Reconcile knows no protocol fields: the protocol entry
builds the desired account.

The link file is `clients/<user>/<tag>.txt`, one line, mode 0600:
`vless://<id>@<USHER_HOST>:<port>?<params>#<user>`. The params are `encryption=none`,
`flow=xtls-rprx-vision`, `type=tcp`, `security=reality`, `sni`, `fp`, `pbk` and `sid`, encoded with
`url.Values.Encode` and `%20` for spaces. The format follows XTLS/Xray-core discussion #716.

## Locked decisions

- The scope is AmneziaWG over IPv4 and Xray VLESS with Reality over raw TCP. The decoder tables
  take more Xray shapes.
- usher owns every interface a backend reports, and an unreferenced interface gets an empty list.
  For Xray that is every VLESS inbound.
- A backend that is down never blocks the others, and its client files stay.
- A name reported by two backends fails in both.
- A broken `usher.yml` never changes state. The last valid file is kept in memory only.
- A file without any user and interface pair is invalid, so a truncated file cannot wipe a node.
- A failure of one interface never stops the daemon and never blocks the others.
- An interface without an IPv4 prefix fails only when a user references it.
- `clients/` files of an interface that a live backend no longer reports are removed.
- `USHER_HOST` is an IPv4 address, not a name: a client whose DNS points into the tunnel cannot
  resolve the name again to reconnect.
- No private key, PSK, `client_params` value, Xray UUID or Reality private key is logged. A public
  key without an entry is logged as base64.
- No subcommands besides `usher run`, no central management, no migration of existing peers.

## Operations

- `clients/` belongs to usher: anything not rendered from the applied state is removed. Files are
  mode `0600`, uid 1000.
- Xray delivery: Xray v24.11.5 or later; `api.listen` on a unix path with `,0660`; the Xray
  process gid equal to usher's group (`user: "0:1000"`); `api.services` limited to
  `HandlerService`; VLESS inbounds start with `clients: []`; inbound tags fit the interface name
  pattern; after a hard kill the operator removes the stale socket file, because the official
  image has no shell.
- Switching a user off in Xray stops new connections only. Live connections last until they close
  or hit Xray's `connIdle`.
- Xray 26.x blocks private destinations for VLESS by default. The e2e server config allows its
  target in the freedom outbound `finalRules`.
- Changing an interface's address, key, port or obfuscation values rewrites every client config of
  that interface; those configs are handed out again.
- Addresses are never freed: switched-off users keep theirs. A full subnet fails the whole
  interface, refill included. awg-grpc accepts at most 1000 peers per interface.
- `users.json` sits in the `usher-state` volume (mode `0600`, uid 1000). To re-key or delete a user:
  `docker compose stop usher`, then
  `docker run --rm -it -u 1000:1000 -v <project>_usher-state:/state alpine vi /state/users.json`,
  delete the `<interface>/<user>` entry from the `awg` or `xray` group,
  `docker compose start usher`. A user still in `usher.yml` gets new keys.
- `USHER_HOST` is an address, not a name: a client that re-resolves a name while its DNS points
  into the tunnel cannot reconnect.
- Client isolation and DNS redirection belong to the service config, not to usher. For AmneziaWG:
  `FORWARD` drops between clients in `PostUp`; a resolver on the tunnel address (for example
  cloudflared with a DoH upstream in the `awg` network namespace) with `USHER_DNS` set to that
  address, and optionally a `DNAT` of port 53 in `PostUp`.
