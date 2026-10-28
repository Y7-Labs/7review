package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/Y4NN777/7review/agent/profile"
	"github.com/Y4NN777/7review/agent/review"
)

// Loader loads repository-specific review skills from disk.
type Loader struct {
	SkillsDir string
	Skills    []Skill
	Profile   profile.SkillProfile
}

// Skill is metadata and body loaded from a repository-local SKILL.md file.
type Skill struct {
	Name           string
	Description    string
	License        string
	Compatibility  string
	AllowedTools   string
	RequiredChecks string
	Metadata       map[string]string
	Path           string
	Frontmatter    string
	Body           string
}

// Load initializes available skills.
func (s *Loader) Load() error {
	if s.SkillsDir == "" {
		return nil
	}
	entries, err := os.ReadDir(s.SkillsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("skills: read %s: %w", s.SkillsDir, err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(s.SkillsDir, entry.Name(), "SKILL.md")
		skill, err := loadSkill(path)
		if err != nil {
			return err
		}
		s.Skills = append(s.Skills, skill)
	}
	return nil
}

// Select returns the review guidance skills relevant to the normalized request.
func (s *Loader) Select(req review.Request) []review.Section {
	activations := s.SelectActivations(req)
	selected := make([]review.Section, 0, len(activations))
	for _, activation := range activations {
		skill, ok := s.skillByName(activation.Name)
		if !ok {
			continue
		}
		selected = append(selected, review.Section{
			Path:    skill.Path,
			Title:   skill.Name,
			Content: skill.ActivationContent(),
			Kind:    review.KindRules,
		})
	}
	return selected
}

// SelectActivations returns structured skill activation metadata for a request.
func (s *Loader) SelectActivations(req review.Request) []review.SkillActivation {
	if s == nil {
		return nil
	}
	var selected []review.SkillActivation
	for _, skill := range s.Skills {
		reason, ok := skill.activationReasonWithProfile(req, s.Profile)
		if !ok {
			continue
		}
		selected = append(selected, skill.activation(reason))
	}
	return selected
}

func (s *Loader) skillByName(name string) (Skill, bool) {
	for _, skill := range s.Skills {
		if skill.Name == name {
			return skill, true
		}
	}
	return Skill{}, false
}

func loadSkill(path string) (Skill, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Skill{}, fmt.Errorf("skills: read %s: %w", path, err)
	}
	return parseSkill(path, data)
}

func parseSkill(path string, data []byte) (Skill, error) {
	text := string(data)
	parts := strings.SplitN(text, "---", 3)
	if len(parts) != 3 || strings.TrimSpace(parts[0]) != "" {
		return Skill{}, fmt.Errorf("skills: %s missing YAML frontmatter", path)
	}

	frontmatter := strings.TrimSpace(parts[1])
	fields := parseFrontmatter(frontmatter)
	skill := Skill{
		Name:           fields["name"],
		Description:    fields["description"],
		License:        fields["license"],
		Compatibility:  fields["compatibility"],
		AllowedTools:   fields["allowed-tools"],
		RequiredChecks: fields["required-checks"],
		Metadata:       parseMetadata(frontmatter),
		Path:           path,
		Frontmatter:    frontmatter,
		Body:           strings.TrimSpace(parts[2]),
	}
	if skill.Name == "" || skill.Description == "" {
		return Skill{}, fmt.Errorf("skills: %s must define name and description", path)
	}
	if err := validateSkill(path, skill); err != nil {
		return Skill{}, err
	}
	return skill, nil
}

// Activation exposes the same metadata contract used by the local loader.
func (s Skill) Activation(reason string) review.SkillActivation {
	return s.activation(reason)
}

func (s Skill) ActivationContent() string {
	if strings.TrimSpace(s.Frontmatter) == "" {
		return s.Body
	}
	return "---\n" + strings.TrimSpace(s.Frontmatter) + "\n---\n\n" + s.Body
}

func parseFrontmatter(frontmatter string) map[string]string {
	fields := make(map[string]string)
	lines := strings.Split(frontmatter, "\n")
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if isIndented(line) {
			continue
		}
		key, value, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if value == "|" || value == "|-" || value == "|+" || value == ">" || value == ">-" || value == ">+" {
			block, next := collectBlockScalar(lines, i+1, strings.HasPrefix(value, ">"))
			fields[key] = block
			i = next - 1
			continue
		}
		fields[key] = unquoteYAML(value)
	}
	return fields
}

func parseMetadata(frontmatter string) map[string]string {
	metadata := make(map[string]string)
	lines := strings.Split(frontmatter, "\n")
	inMetadata := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if !isIndented(line) {
			key, value, ok := strings.Cut(trimmed, ":")
			inMetadata = ok && strings.TrimSpace(key) == "metadata" && strings.TrimSpace(value) == ""
			continue
		}
		if !inMetadata {
			continue
		}
		key, value, ok := strings.Cut(trimmed, ":")
		if !ok {
			continue
		}
		metadata[strings.TrimSpace(key)] = unquoteYAML(strings.TrimSpace(value))
	}
	return metadata
}

func collectBlockScalar(lines []string, start int, folded bool) (string, int) {
	var block []string
	i := start
	for ; i < len(lines); i++ {
		line := lines[i]
		if strings.TrimSpace(line) == "" {
			block = append(block, "")
			continue
		}
		if !isIndented(line) {
			break
		}
		block = append(block, strings.TrimSpace(line))
	}
	if folded {
		return strings.TrimSpace(strings.Join(block, " ")), i
	}
	return strings.TrimSpace(strings.Join(block, "\n")), i
}

func isIndented(line string) bool {
	return strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t")
}

func unquoteYAML(value string) string {
	value = strings.TrimSpace(value)
	value = strings.Trim(value, `"`)
	return strings.Trim(value, `'`)
}

var skillNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}[a-z0-9]$|^[a-z0-9]$`)

func validateSkill(path string, skill Skill) error {
	if !skillNamePattern.MatchString(skill.Name) {
		return fmt.Errorf("skills: %s invalid name %q", path, skill.Name)
	}
	if len(skill.Description) > 1024 {
		return fmt.Errorf("skills: %s description exceeds 1024 characters", path)
	}
	dirName := filepath.Base(filepath.Dir(path))
	if dirName != skill.Name {
		return fmt.Errorf("skills: %s name %q must match directory %q", path, skill.Name, dirName)
	}
	if strings.TrimSpace(skill.Body) == "" {
		return fmt.Errorf("skills: %s must include markdown instructions", path)
	}
	return nil
}

func (s Skill) appliesTo(req review.Request) bool {
	_, ok := s.activationReason(req)
	return ok
}

func (s Skill) activationReason(req review.Request) (string, bool) {
	return s.activationReasonWithProfile(req, profile.SkillProfile{})
}

func (s Skill) activationReasonWithProfile(req review.Request, cfg profile.SkillProfile) (string, bool) {
	name := strings.ToLower(s.Name)

	providerSkills := cfg.ProviderSkills
	if len(providerSkills) == 0 {
		providerSkills = map[string]string{
			"github": "github-merge-api",
			"gitlab": "gitlab-merge-api",
		}
	}
	if expected := providerSkills[strings.ToLower(req.Provider)]; strings.EqualFold(name, expected) {
		return "provider " + strings.ToLower(req.Provider) + " requires provider API operating rules", true
	}
	for provider, expected := range providerSkills {
		if strings.EqualFold(name, expected) && !strings.EqualFold(req.Provider, provider) {
			return "", false
		}
	}

	if alwaysOnSkill(name, cfg) {
		return "core baseline review skill", true
	}
	score := s.score(req)
	minScore := cfg.TopicalActivationMinScore
	if minScore == 0 {
		minScore = 2
	}
	if score >= minScore {
		return fmt.Sprintf("matched request signals score=%d", score), true
	}
	return "", false
}

func alwaysOnSkill(name string, cfg profile.SkillProfile) bool {
	alwaysOn := cfg.AlwaysOn
	if len(alwaysOn) == 0 {
		alwaysOn = []string{
			"methodology-review",
			"project-knowledge",
			"framework-rules-review",
			"traceability-review",
			"review-publisher",
			"laws-guards-review",
			"security-review",
			"hil-gate-review",
		}
	}
	for _, candidate := range alwaysOn {
		if strings.EqualFold(name, candidate) {
			return true
		}
	}
	return false
}

func (s Skill) activation(reason string) review.SkillActivation {
	name := strings.ToLower(s.Name)
	category := skillCategory(name)
	return review.SkillActivation{
		Name:           s.Name,
		Path:           s.Path,
		Category:       category,
		RiskTier:       s.Metadata["risk-tier"],
		ReviewDomain:   s.Metadata["review-domain"],
		AllowedTools:   splitList(s.AllowedTools),
		RequiredChecks: skillRequiredChecks(s),
		Required:       category == "core" || category == "provider-api",
		Reason:         reason,
	}
}

func skillCategory(name string) string {
	switch name {
	case "github-merge-api", "gitlab-merge-api":
		return "provider-api"
	case "methodology-review",
		"project-knowledge",
		"framework-rules-review",
		"traceability-review",
		"review-publisher",
		"laws-guards-review",
		"security-review",
		"hil-gate-review":
		return "core"
	case "project-wiki", "process-knowledge-review":
		return "knowledge"
	default:
		return "triggered"
	}
}

func skillRequiredChecks(skill Skill) []string {
	checks := splitList(skill.RequiredChecks)
	if len(checks) > 0 {
		return checks
	}
	checks = splitList(skill.Metadata["required-checks"])
	if len(checks) > 0 {
		return checks
	}
	switch strings.ToLower(skill.Name) {
	case "github-merge-api", "gitlab-merge-api":
		return []string{"webhook-normalization", "scm-enrichment", "diff-normalization", "publish-idempotency"}
	case "methodology-review":
		return []string{"lifecycle", "deterministic-boundaries"}
	case "project-knowledge":
		return []string{"selected-context", "citations"}
	case "framework-rules-review":
		return []string{"local-rules", "changed-path-rules"}
	case "traceability-review":
		return []string{"changed-file-evidence", "source-identifier"}
	case "review-publisher":
		return []string{"draft-final-boundary", "idempotency"}
	case "laws-guards-review":
		return []string{"protected-actions", "validation-boundary"}
	case "security-review":
		return []string{"trust-boundary", "secret-redaction"}
	case "hil-gate-review":
		return []string{"approval-boundary", "durable-state"}
	default:
		return nil
	}
}

func splitList(value string) []string {
	parts := strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n'
	})
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func (s Skill) score(req review.Request) int {
	requestTerms := requestTerms(req)
	if len(requestTerms) == 0 {
		if s.Name == "test-quality-review" && hasReviewableCode(req) {
			return 2
		}
		return 0
	}
	index := skillIndexText(s)
	score := 0
	for _, term := range requestTerms {
		if strings.Contains(index, term) {
			score++
		}
	}
	if s.Name == "test-quality-review" && hasReviewableCode(req) {
		score++
	}
	return score
}

func selectionText(req review.Request) string {
	parts := []string{
		req.Provider,
		req.Title,
		req.Description,
		req.Repository,
		req.SourceBranch,
		req.TargetBranch,
	}
	parts = append(parts, req.Labels...)
	parts = append(parts, req.ChangedPaths...)
	return strings.ToLower(strings.Join(parts, " "))
}

func requestTerms(req review.Request) []string {
	text := selectionText(req)
	candidates := splitTerms(text)
	seen := make(map[string]bool)
	var terms []string
	for _, term := range candidates {
		if ignoreTerm(term) || seen[term] {
			continue
		}
		seen[term] = true
		terms = append(terms, term)
	}
	sort.Strings(terms)
	return terms
}

func skillIndexText(skill Skill) string {
	parts := []string{skill.Name, skill.Description}
	for _, line := range strings.Split(skill.Body, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "-") {
			parts = append(parts, trimmed)
		}
	}
	return strings.ToLower(strings.Join(parts, " "))
}

var termSplitPattern = regexp.MustCompile(`[^a-z0-9._/-]+`)

func splitTerms(text string) []string {
	raw := termSplitPattern.Split(strings.ToLower(text), -1)
	var out []string
	for _, item := range raw {
		item = strings.Trim(item, "._/-")
		if item == "" {
			continue
		}
		out = append(out, item)
		out = append(out, splitPathTerms(item)...)
	}
	return out
}

func splitPathTerms(item string) []string {
	var out []string
	for _, sep := range []string{"/", "-", "_", "."} {
		if strings.Contains(item, sep) {
			for _, part := range strings.Split(item, sep) {
				part = strings.TrimSpace(part)
				if part != "" {
					out = append(out, part)
				}
			}
		}
	}
	return out
}

func ignoreTerm(term string) bool {
	if len(term) < 3 {
		return true
	}
	switch term {
	case "the", "and", "for", "with", "from", "this", "that", "change", "update", "review", "agent", "docs", "file", "files":
		return true
	default:
		return false
	}
}

func hasReviewableCode(req review.Request) bool {
	for _, path := range req.ChangedPaths {
		lower := strings.ToLower(filepath.ToSlash(path))
		if strings.HasSuffix(lower, ".md") || strings.HasSuffix(lower, ".txt") {
			continue
		}
		if strings.Contains(lower, "testdata/") {
			continue
		}
		return true
	}
	return false
}
