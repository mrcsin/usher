// Package config loads and validates usher.yml, the map of user name to interface names.
package config

import (
	"fmt"
	"os"
	"regexp"

	"gopkg.in/yaml.v3"
)

var (
	userNamePattern      = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)
	interfaceNamePattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_=+.-]{0,14}$`)
)

// Load reads path and returns the interface names of every user. A user with an empty list is
// valid, but a file without any user/interface pair is not. Every error names the file, and the
// line when the parser or a rule violation has one.
func Load(path string) (map[string][]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if len(doc.Content) == 0 {
		return nil, fmt.Errorf("%s: no user/interface pair", path)
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%s: line %d: want a mapping of user name to interface list", path, root.Line)
	}

	users := make(map[string][]string, len(root.Content)/2)
	pairs := 0
	for i := 0; i < len(root.Content); i += 2 {
		key, value := root.Content[i], root.Content[i+1]
		if key.Kind != yaml.ScalarNode || !userNamePattern.MatchString(key.Value) {
			return nil, fmt.Errorf("%s: line %d: bad user name %q", path, key.Line, key.Value)
		}
		user := key.Value
		if _, dup := users[user]; dup {
			return nil, fmt.Errorf("%s: line %d: user %q is defined twice", path, key.Line, user)
		}
		interfaces, err := interfaceList(user, value)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		users[user] = interfaces
		pairs += len(interfaces)
	}
	if pairs == 0 {
		return nil, fmt.Errorf("%s: no user/interface pair", path)
	}
	return users, nil
}

func interfaceList(user string, node *yaml.Node) ([]string, error) {
	if node.Kind != yaml.SequenceNode {
		return nil, fmt.Errorf("line %d: user %q: want a list of interface names", node.Line, user)
	}
	interfaces := make([]string, 0, len(node.Content))
	seen := make(map[string]bool, len(node.Content))
	for _, item := range node.Content {
		if item.Kind != yaml.ScalarNode || !interfaceNamePattern.MatchString(item.Value) {
			return nil, fmt.Errorf("line %d: user %q: bad interface name %q", item.Line, user, item.Value)
		}
		if seen[item.Value] {
			return nil, fmt.Errorf("line %d: user %q: interface %q is listed twice", item.Line, user, item.Value)
		}
		seen[item.Value] = true
		interfaces = append(interfaces, item.Value)
	}
	return interfaces, nil
}
