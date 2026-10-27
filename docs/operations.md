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
writes no file, and the file keeps the previous content. node_exporter's
`node_textfile_mtime_seconds` for the file then stops moving; usher writes no timestamp of its own.
A write error is logged as `writing status file`.

When every backend is down, usher still writes the file, but it holds no series. Alert on
`absent(usher_interface_up)` as well.
