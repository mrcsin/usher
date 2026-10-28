# Operations

This page covers what usher leaves to the services, how to issue new keys to a user and how
node_exporter serves the stats.

## Isolation and DNS

usher does not configure the services. For AmneziaWG, the interface config does it: a `FORWARD`
drop between clients in `PostUp`, and a resolver on the tunnel address (for example cloudflared
with a DoH upstream in the awg-grpc network namespace) with `USHER_DNS` set to that address.

## New keys for a user

`state/users.json` has two groups: `awg` with entries `<interface>/<user>` and `xray` with entries
`<tag>/<user>`. usher never deletes an entry. To issue new keys:

1. `docker compose stop usher`
2. `docker run --rm -it -u 1000:1000 -v <project>_usher-state:/state alpine vi /state/users.json`
3. Delete the entries of the user, then `docker compose start usher`

A user still in `usher.yml` gets new keys, and every client file of that user changes.

## Stats with node_exporter

usher writes `usher.prom` into `USHER_STATUS_DIR`; the [README](../README.md#stats) describes its
metrics. The example compose file mounts `./usher/status` there. To serve the file:

1. Create `./usher/status` owned by uid 1000, with mode `0755`.
2. Give node_exporter that directory: on the host as it is, or through a bind mount when
   node_exporter runs in a container.
3. Start node_exporter with `--collector.textfile.directory=<that directory>`.

usher writes the file with mode `0644`. A node_exporter that runs as another user, such as
`nobody`, reads it only when it can traverse every directory of the path. The textfile collector
reads only `*.prom` files, so it skips the temporary file of a write in progress.

A pass that stops early, for example on a `users.json` error, or that a stop signal interrupts,
writes no file, and the file keeps the previous content. A stopped usher leaves it the same way.
node_exporter keeps serving that content, so every interface keeps its last value. usher writes no
timestamp of its own; node_exporter's `node_textfile_mtime_seconds` for the file stops moving. A
write error is logged as `writing status file`.

### Alerts

`usher_interface_up` reads 0 for each referenced interface the last pass did not apply, and for
each interface of an open backend that failed. An unreferenced interface of a backend that is down
is absent from the file. The cause of a 0 can be any of these: the interface failed, its backend
is down, or no backend reports it, as after the removal of an Xray inbound. One rule on
`usher_interface_up == 0` covers them all and needs no interface names. A name in `usher.yml` that
no backend has, such as a typo, reads 0 until you fix it.

The log names the cause. `interface failed` and `interface is not reported by any backend` name
the interface. `backend is down` names only the backend, not its interfaces. usher logs `interface
is not reported by any backend` only when no backend is down, so an interface that one backend
stops reporting while the other backend is down reads 0 with no log line of its own.

A value of 0 does not mean that the users cannot connect. An AmneziaWG interface keeps forwarding
for its enrolled peers while awg-grpc is unreachable.

Two failures do not show as 0:

- `absent(usher_interface_up)` fires when node_exporter serves no `usher_interface_up` series: the
  file is missing or unreadable.
- A stale file keeps its last values. Only its age shows the failure.

```yaml
groups:
  - name: usher
    rules:
      - alert: UsherInterfaceDown
        expr: usher_interface_up == 0
        for: 2m
      - alert: UsherStatusEmpty
        expr: absent(usher_interface_up)
        for: 2m
      - alert: UsherStatusStale
        expr: time() - node_textfile_mtime_seconds{file=~".*usher.prom"} > 180
        for: 2m
```

usher runs a pass at least every 30 s, so 180 s of file age is six missed passes.
