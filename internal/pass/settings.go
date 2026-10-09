// Package pass runs one reconcile pass over the backends: enrollment, the state file, the
// requests to each backend and the clients directory.
package pass

// Settings holds the paths one pass works with.
type Settings struct {
	ConfigPath string
	ClientsDir string
	StatePath  string
}
