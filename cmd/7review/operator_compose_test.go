package main

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestCanonicalOperatorComposeForwardsExplicitMappingAndDedicatedCredential(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "docker-compose.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Services map[string]struct {
			Environment map[string]string `yaml:"environment"`
		} `yaml:"services"`
	}
	if err = yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	env := doc.Services["7review"].Environment
	for _, name := range []string{"REVIEW_OPERATOR_GRANTS_JSON", "REVIEW_OPERATOR_TOKEN"} {
		if env[name] != "${"+name+":-}" {
			t.Fatalf("canonical container configuration is not forwarded independently: %s", name)
		}
	}
}
