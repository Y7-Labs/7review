package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDockerCompose_WiresRequiredSidecarsOnOneNetwork(t *testing.T) {
	data := readRepoFile(t, "docker-compose.yml")

	required := []string{
		"7review:",
		"headroom:",
		"mempalace:",
		"HEADROOM_URL: http://headroom:8787",
		"MEMPALACE_URL: http://mempalace:8788",
		"CORPUS_ROOT: /workspace",
		"INPUT_PROFILE: /app/profiles/default.input-profile.json",
		"SKILLS_DIR: /app/agent/skills",
		"INSTRUCTIONS_PATH: /app/agent/instructions.md",
		"HTTP_READ_HEADER_TIMEOUT_MS:",
		"HTTP_WRITE_TIMEOUT_MS:",
		"WEBHOOK_JOB_TIMEOUT_MS:",
		"ACCOUNTING_ROUTE_BOUNDS_JSON:",
		`test: ["CMD", "/app/7review", "healthcheck"]`,
		"${CORPUS_ROOT:-.}:/workspace:ro",
		"GITLAB_URL:",
		"review-agent:",
		"mempalace-data:",
		"headroom-cache:",
		"OPENROUTER_API_KEY:",
		"OPENROUTER_BASE_URL:",
		"DEEPSEEK_API_KEY:",
		"DEEPSEEK_BASE_URL:",
		`PROVIDER: "${PROVIDER:-}"`,
		"PROVIDER_API_KEY:",
		"PROVIDER_BASE_URL:",
		"REVIEW_MODEL:",
		"SMALL_MODEL:",
		"host.docker.internal:host-gateway",
		"read_only: true",
		"no-new-privileges:true",
		"restart: unless-stopped",
	}
	for _, item := range required {
		if !strings.Contains(data, item) {
			t.Fatalf("docker-compose.yml missing %q", item)
		}
	}
	if strings.Count(data, "driver: bridge") != 1 {
		t.Fatalf("expected one bridge network, got compose:\n%s", data)
	}
}

func TestDockerfiles_BuildAgentAndSidecarImages(t *testing.T) {
	files := map[string][]string{
		"Dockerfile": {
			"go build",
			"/app/7review",
			"mkdir -p /out/data/7review",
			"/out/agent",
			"/out/profiles",
			"COPY --from=build --chown=nonroot:nonroot /out/data /data",
			"COPY --from=build --chown=nonroot:nonroot /out/agent /app/agent",
			"COPY --from=build --chown=nonroot:nonroot /out/profiles /app/profiles",
			"agent/instructions.md",
			"agent/skills",
			"profiles/default.input-profile.json",
			`HEALTHCHECK`,
			`"/app/7review", "healthcheck"`,
		},
		filepath.Join("docker", "headroom-bridge", "Dockerfile"): {
			"headroom-ai==${HEADROOM_VERSION}",
			"ARG HEADROOM_VERSION=0.36.5",
			"USER app:app",
			"app.py",
		},
		filepath.Join("docker", "mempalace-bridge", "Dockerfile"): {
			"mempalace==${MEMPALACE_VERSION}",
			"ARG MEMPALACE_VERSION=3.8.0",
			"USER app:app",
			"/data",
			"app.py",
		},
	}

	for name, required := range files {
		data := readRepoFile(t, name)
		for _, item := range required {
			if !strings.Contains(data, item) {
				t.Fatalf("%s missing %q", name, item)
			}
		}
	}
}

func TestDockerIgnoreExcludesFrontendBuildContext(t *testing.T) {
	data := readRepoFile(t, ".dockerignore")
	if !strings.Contains(data, "site") {
		t.Fatal(".dockerignore must exclude the frontend tree from the agent image context")
	}
}

func TestComposeSmokeScriptExercisesFullStackReadiness(t *testing.T) {
	data := readRepoFile(t, filepath.Join("scripts", "compose_smoke.sh"))
	for _, item := range []string{
		"COMPOSE_PROJECT_NAME",
		"7review_smoke",
		"docker compose up --wait",
		"docker compose exec -T 7review /app/7review status --plain --server http://127.0.0.1:8080",
		"docker compose exec -T headroom python - < scripts/compose_contract_smoke.py",
		"docker compose down -v --remove-orphans",
		"trap cleanup EXIT",
	} {
		if !strings.Contains(data, item) {
			t.Fatalf("compose smoke script missing %q", item)
		}
	}

	contract := readRepoFile(t, filepath.Join("scripts", "compose_contract_smoke.py"))
	for _, item := range []string{"/reduce", "/write", "/recall", "query_embedding", "sidecar contracts: ok"} {
		if !strings.Contains(contract, item) {
			t.Fatalf("compose contract smoke missing %q", item)
		}
	}
}

func TestRuntimeWorkflowRunsSourceAndComposeVerification(t *testing.T) {
	data := readRepoFile(t, filepath.Join(".github", "workflows", "runtime.yml"))
	for _, item := range []string{"make verify", "make compose-smoke", "needs: verify", "permissions:"} {
		if !strings.Contains(data, item) {
			t.Fatalf("runtime workflow missing %q", item)
		}
	}
}

func readRepoFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", path))
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}
