package skills

import (
	"fmt"
	"io"
	"strings"

	"gopkg.in/yaml.v3"
)

// Parse validates a policy-selected method from immutable bytes. The legacy
// heuristic loader retains its historical parser while policy methods fail
// closed on malformed, duplicate or unknown frontmatter fields.
func Parse(path string, data []byte) (Skill, error) {
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	if len(lines) < 3 || lines[0] != "---" {
		return Skill{}, fmt.Errorf("skills: %s missing YAML frontmatter", path)
	}
	end := 1
	for end < len(lines) && lines[end] != "---" {
		end++
	}
	if end == len(lines) {
		return Skill{}, fmt.Errorf("skills: %s missing closing frontmatter delimiter", path)
	}
	frontmatter := strings.Join(lines[1:end], "\n")
	var fields struct {
		Name           string            `yaml:"name"`
		Description    string            `yaml:"description"`
		License        string            `yaml:"license"`
		Compatibility  string            `yaml:"compatibility"`
		AllowedTools   string            `yaml:"allowed-tools"`
		RequiredChecks string            `yaml:"required-checks"`
		Metadata       map[string]string `yaml:"metadata"`
	}
	decoder := yaml.NewDecoder(strings.NewReader(frontmatter))
	decoder.KnownFields(true)
	if err := decoder.Decode(&fields); err != nil {
		return Skill{}, fmt.Errorf("skills: %s invalid frontmatter: %w", path, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return Skill{}, fmt.Errorf("skills: %s must contain exactly one frontmatter document", path)
	}
	skill := Skill{
		Name: fields.Name, Description: fields.Description, License: fields.License,
		Compatibility: fields.Compatibility, AllowedTools: fields.AllowedTools,
		RequiredChecks: fields.RequiredChecks, Metadata: fields.Metadata,
		Path: path, Frontmatter: frontmatter, Body: strings.TrimSpace(strings.Join(lines[end+1:], "\n")),
	}
	if strings.TrimSpace(skill.Description) == "" {
		return Skill{}, fmt.Errorf("skills: %s must define description", path)
	}
	if err := validateSkill(path, skill); err != nil {
		return Skill{}, err
	}
	return skill, nil
}
