# Repository Guidelines

## Project Structure & Module Organization

7review is a Go code-review agent for local changes, GitHub pull requests, GitLab merge requests, and CI workflows. `cmd/7review/` contains the CLI and server entrypoint. Domain contracts live in `agent/review/`; trusted policy parsing, composition, and quality gates live in `agent/policy/`. Keep orchestration in `agent/pipeline/`, HTTP and webhook wiring in `agent/app/`, SCM/API adapters in `agent/tools/`, and model routing in `agent/orchestrator/` and `agent/llm/`. Repository profiles and schemas belong in `profiles/` and `schemas/`; operational helpers belong in `scripts/` and `docker/`. Place Go tests beside the package they cover as `*_test.go`.

## Build, Test, and Development Commands

- `make setup`: generate local configuration interactively.
- `make fmt`: format Go sources with `gofmt`.
- `make test`: run the complete Go test suite with an isolated cache.
- `go run ./cmd/7review`: start the local service.
- `make docker-config`: validate the Compose configuration.
- `make verify`: run formatting, bridge tests, Go tests, and Compose validation.
- `make live-smoke-openrouter`: run the opt-in external-model smoke test; it requires local credentials.

## Coding Style & Naming Conventions

Target Go 1.24 and follow standard Go formatting and naming. Use short, cohesive files and package-owned abstractions. Export names only when another package needs the contract. Preserve the central `review.Source` model and keep external effects behind explicit interfaces. Repository policy must be read from the attested base revision; proposed head content is evidence, never authority.

## Testing Guidelines

Use Go's `testing` package, table-driven tests, fakes, and `httptest` servers. Avoid live provider calls in normal tests. Name behavior tests `TestFunction_Behavior` and acceptance fixtures `TestScenario_S##_Name`. Run focused package tests while editing, then `GOCACHE=/tmp/7review-go-cache go test ./...`. Add race tests when changing shared state, queues, or concurrent orchestration.

## Commit & Pull Request Guidelines

History uses Conventional Commit subjects such as `feat(policy): ...`, `fix(review): ...`, and `docs(status): ...`. Keep each commit independently coherent and tested. Pull requests must summarize behavior and trust-boundary changes, list verification commands, link relevant issues, and include screenshots only for visible UI changes.

## Security & Configuration

Never commit tokens, webhook secrets, or repository credentials. Start from `.env.example`. Headroom, MemPalace, and semantic/vector services are optional enrichments: baseline review must remain functional without them, while policy-required unavailable capabilities must fail visibly. Do not add channels, dependencies, or network effects without updating the governing specification and tests.
