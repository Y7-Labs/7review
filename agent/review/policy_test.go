package review

import (
	"encoding/json"
	"testing"
)

func TestPolicyCapabilitiesSurvivePersistenceWithoutSharingAuthority(t *testing.T) {
	source := Source{Policy: PolicyProjection{Mode: "enforce", Enforced: true, AllowedCapabilities: []string{"repo.read"}, ResolvedMethods: []PolicyMethod{{ID: "repo/backend", Revision: "base"}}}}
	clone := source.Clone()
	clone.Policy.AllowedCapabilities[0] = "model.review"
	clone.Policy.ResolvedMethods[0].Revision = "head"
	if source.Policy.ResolvedMethods[0].Revision != "base" {
		t.Fatal("cloned method provenance mutated original authority")
	}
	if source.Policy.AllowsCapability("model.review") || !source.Policy.AllowsCapability("repo.read") {
		t.Fatal("cloned projection mutated original permissions")
	}
	data, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	var restored Source
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatal(err)
	}
	if restored.Policy.AllowsCapability("model.review") || !restored.Policy.AllowsCapability("repo.read") {
		t.Fatal("persisted capability intersection was lost")
	}
}

func TestOldEnforcedPolicyWithoutCapabilitiesDoesNotWidenPermissions(t *testing.T) {
	var projection PolicyProjection
	if err := json.Unmarshal([]byte(`{"mode":"enforce","digest":"old-policy"}`), &projection); err != nil {
		t.Fatal(err)
	}
	if projection.AllowsCapability("model.review") {
		t.Fatal("old enforced snapshot without permissions silently became unrestricted")
	}
}
