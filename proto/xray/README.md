# Vendored Xray protos

These files are copied unchanged from [XTLS/Xray-core](https://github.com/XTLS/Xray-core) at tag
`v26.9.9`, with their original relative paths. They are licensed under MPL-2.0; the license text is
at <https://github.com/XTLS/Xray-core/blob/v26.9.9/LICENSE>.

usher imports no xray-core Go code. `buf generate` turns these files into `gen/xray`, with the Go
import prefix `github.com/mrcsin/usher/gen/xray`.

## Refresh

1. Pick the new tag and set it in this file.
2. Fetch each file listed by `find proto/xray -name '*.proto'` from
   `https://raw.githubusercontent.com/XTLS/Xray-core/<tag>/<path>`. Add any new `import` target
   outside `google/`; drop files that no import reaches.
3. Run `go tool -modfile=tools/go.mod buf generate` and commit `gen/`.
