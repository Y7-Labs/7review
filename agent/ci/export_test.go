package ci

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestExportWritesCanonicalArtifactsAndVerifiedManifest(t *testing.T) {
	repo, base, head := ciGitFixture(t)
	result := runPartialFixture(t, repo, base, head)
	output := filepath.Join(t.TempDir(), "artifacts")
	if err := Export(result, output); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"assessment.json", "coverage.json", "gate.json", "report.md", "run-manifest.json"} {
		info, err := os.Stat(filepath.Join(output, name))
		if err != nil {
			t.Fatalf("missing %s: %v", name, err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("artifact %s has mode %o", name, info.Mode().Perm())
		}
	}
	manifestData, err := os.ReadFile(filepath.Join(output, "run-manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest runManifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.SchemaVersion != 1 || manifest.ExitCode != 2 || len(manifest.Artifacts) != 4 || len(manifest.Exclusions) != 3 {
		t.Fatalf("unexpected manifest: %#v", manifest)
	}
	for _, artifact := range manifest.Artifacts {
		data, err := os.ReadFile(filepath.Join(output, artifact.Name))
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		if artifact.Digest != "sha256:"+hex.EncodeToString(sum[:]) || artifact.Bytes != len(data) {
			t.Fatalf("manifest descriptor does not bind %s: %#v", artifact.Name, artifact)
		}
	}
	matches, err := filepath.Glob(filepath.Join(output, ".*-*"))
	if err != nil || len(matches) != 0 {
		t.Fatalf("temporary output files leaked: %v %v", matches, err)
	}
}
