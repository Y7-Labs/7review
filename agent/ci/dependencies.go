package ci

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// ValidateDependencyGraph rejects self/downstream cycles before the runner
// performs any provider request. Every reachable prerequisite must have an
// explicit graph entry; an empty entry means independently established.
func ValidateDependencyGraph(currentJob string, graph map[string][]string) error {
	currentJob = strings.TrimSpace(currentJob)
	if currentJob == "" {
		return errors.New("CI dependency graph requires the current job identity")
	}
	if _, exists := graph[currentJob]; !exists {
		return fmt.Errorf("CI dependency metadata for current job %q is unavailable", currentJob)
	}
	state := map[string]uint8{}
	var visit func(string, []string) error
	visit = func(job string, stack []string) error {
		if state[job] == 1 {
			return fmt.Errorf("CI dependency cycle: %s", strings.Join(append(stack, job), " -> "))
		}
		if state[job] == 2 {
			return nil
		}
		dependencies, exists := graph[job]
		if !exists {
			return fmt.Errorf("CI dependency metadata for required job %q is unavailable", job)
		}
		state[job] = 1
		dependencies = append([]string(nil), dependencies...)
		sort.Strings(dependencies)
		for _, dependency := range dependencies {
			dependency = strings.TrimSpace(dependency)
			if dependency == "" {
				return fmt.Errorf("CI dependency for job %q is empty", job)
			}
			if err := visit(dependency, append(stack, job)); err != nil {
				return err
			}
		}
		state[job] = 2
		return nil
	}
	return visit(currentJob, nil)
}

func ParseDependencyGraph(values []string) (map[string][]string, error) {
	if len(values) == 0 {
		return nil, nil
	}
	graph := make(map[string][]string, len(values))
	for _, value := range values {
		job, list, ok := strings.Cut(value, "=")
		job = strings.TrimSpace(job)
		if !ok || job == "" {
			return nil, errors.New("dependency must use job=dependency1,dependency2")
		}
		if _, exists := graph[job]; exists {
			return nil, fmt.Errorf("duplicate CI dependency entry for %q", job)
		}
		if strings.TrimSpace(list) == "" {
			graph[job] = []string{}
			continue
		}
		for _, dependency := range strings.Split(list, ",") {
			graph[job] = append(graph[job], strings.TrimSpace(dependency))
		}
	}
	return graph, nil
}
