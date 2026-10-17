package config

import (
	"testing"

	"gopkg.in/yaml.v3"
)

// yamlNode decodes a scalar YAML document into a node for UnmarshalYAML tests.
func yamlNode(t *testing.T, doc string) *yaml.Node {
	t.Helper()
	var n yaml.Node
	if err := yaml.Unmarshal([]byte(doc), &n); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}
	if len(n.Content) == 0 {
		t.Fatalf("empty yaml document")
	}
	return n.Content[0]
}
