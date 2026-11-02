package review

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
)

const operatorFixture = `{"schema_version":1,"principals":[{"id":"alice","token_env":"ALICE_TOKEN","grants":[{"boundary_id":"service","project_id":"team","provider":"github","host":"github.com","repository_id":"org/repo","roles":["operator"]}]}]}`

func TestOperatorDirectoryConcurrentAuthenticationDoesNotWidenGrants(t *testing.T) {
	d, err := DecodeOperatorDirectory([]byte(operatorFixture), func(string) string { return "alice-secret" })
	if err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	for n := 0; n < 16; n++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for n := 0; n < 100; n++ {
				actor, err := d.Authenticate("alice-secret")
				if err != nil || d.Authorize(actor, "service", "team", ChangeKey{Provider: "github", Host: "github.com", RepositoryID: "org/repo"}, OperatorRoleOperator) != nil {
					t.Error("concurrent authentication lost authority")
					return
				}
				if d.Authorize(actor, "service", "team", ChangeKey{Provider: "github", Host: "github.com", RepositoryID: "org/other"}, OperatorRoleOperator) == nil {
					t.Error("concurrent authority widened")
					return
				}
			}
		}()
	}
	workers.Wait()
}

func TestOperatorDirectoryAuthenticatesExactScopedAuthority(t *testing.T) {
	d, err := DecodeOperatorDirectory([]byte(operatorFixture), func(string) string { return "alice-secret" })
	if err != nil {
		t.Fatal(err)
	}
	actor, err := d.Authenticate("alice-secret")
	if err != nil || actor.PrincipalID() != "alice" {
		t.Fatalf("actor: %v %v", actor, err)
	}
	change := ChangeKey{Provider: "github", Host: "github.com", RepositoryID: "org/repo"}
	if err := d.Authorize(actor, "service", "team", change, OperatorRoleOperator); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*ChangeKey){func(c *ChangeKey) { c.Provider = "gitlab" }, func(c *ChangeKey) { c.Host = "evil.example" }, func(c *ChangeKey) { c.RepositoryID = "org/other" }} {
		other := change
		mutate(&other)
		if d.Authorize(actor, "service", "team", other, OperatorRoleOperator) == nil {
			t.Fatal("cross-repository authority accepted")
		}
	}
	if d.Authorize(actor, "other", "team", change, OperatorRoleOperator) == nil || d.Authorize(actor, "service", "other", change, OperatorRoleOperator) == nil || d.Authorize(actor, "service", "team", change, OperatorRoleReviewer) == nil {
		t.Fatal("namespace/role authority widened")
	}
	other, _ := DecodeOperatorDirectory([]byte(operatorFixture), func(string) string { return "alice-secret" })
	if other.Authorize(actor, "service", "team", change, OperatorRoleOperator) == nil {
		t.Fatal("actor from another authority accepted")
	}
	if d.Authorize(OperatorActor{}, "service", "team", change, OperatorRoleOperator) == nil {
		t.Fatal("zero actor accepted")
	}
	for _, token := range []string{"", "wrong", " alice-secret", "alice-secret "} {
		if _, err := d.Authenticate(token); err == nil {
			t.Fatal("invalid credential accepted")
		}
	}
	var absent *OperatorDirectory
	if _, err := absent.Authenticate("alice-secret"); err == nil {
		t.Fatal("absent directory authenticated")
	}
	raw, _ := json.Marshal(d)
	if strings.Contains(string(raw), "secret") || strings.Contains(string(raw), "alice") {
		t.Fatal("directory serialization leaks credentials/principals")
	}
}

func TestOperatorDirectoryRejectsAmbiguousOrUnsafeConfiguration(t *testing.T) {
	cases := []string{"", "null", "{}", strings.Repeat(" ", 65537), operatorFixture + "{}",
		strings.Replace(operatorFixture, `"schema_version":1`, `"schema_version":1,"schema_version":1`, 1),
		strings.Replace(operatorFixture, `"id":"alice"`, `"id":"alice","ID":"mallory"`, 1),
		strings.Replace(operatorFixture, `"token_env":"ALICE_TOKEN"`, `"token":"raw-secret"`, 1),
		strings.Replace(operatorFixture, `"repository_id":"org/repo"`, `"repository_id":"*"`, 1),
		strings.Replace(operatorFixture, `"roles":["operator"]`, `"roles":["operator","operator"]`, 1),
		strings.Replace(operatorFixture, `"roles":["operator"]`, `"roles":["admin"]`, 1),
		strings.Replace(operatorFixture, `"host":"github.com"`, `"host":"https://github.com"`, 1),
		strings.Replace(operatorFixture, `"id":"alice"`, `"id":" alice"`, 1),
		strings.Replace(operatorFixture, `"schema_version":1`, `"schema_version":2`, 1),
		strings.Replace(operatorFixture, `"roles":["operator"]`, `"roles":null`, 1),
	}
	for n, raw := range cases {
		if _, err := DecodeOperatorDirectory([]byte(raw), func(string) string { return "secret" }); err == nil {
			t.Errorf("case %d accepted", n)
		}
	}
	for _, token := range []string{"", " secret", "secret\n"} {
		if _, err := DecodeOperatorDirectory([]byte(operatorFixture), func(string) string { return token }); err == nil {
			t.Fatal("invalid resolved secret accepted")
		}
	}
	var doc map[string]any
	_ = json.Unmarshal([]byte(operatorFixture), &doc)
	principals := doc["principals"].([]any)
	copy := map[string]any{"id": "bob", "token_env": "BOB_TOKEN", "grants": principals[0].(map[string]any)["grants"]}
	doc["principals"] = append(principals, copy)
	raw, _ := json.Marshal(doc)
	if _, err := DecodeOperatorDirectory(raw, func(string) string { return "same-secret" }); err == nil {
		t.Fatal("ambiguous shared credential accepted")
	}
}
