package policy

import (
	"strings"
	"testing"
	"time"
)

func TestOperatorCeilingsRejectIncomparableOrInvalidLimits(t *testing.T) {
	for _, tc := range []struct {
		name, reason string
		mutate       func(*LimitsV2)
	}{
		{"currency", "currency", func(c *LimitsV2) { c.Currency = "EUR" }},
		{"shorter requested renewal", "period", func(c *LimitsV2) { p := *c.Change.Period; p.DurationMS *= 2; c.Change.Period = &p }},
		{"shifted project renewal", "period", func(c *LimitsV2) { p := *c.Project.Period; p.Anchor = p.Anchor.Add(time.Hour); c.Project.Period = &p }},
		{"negative limit is not unlimited", "nonnegative", func(c *LimitsV2) { c.Attempt.ModelCalls = -1 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := validConfig()
			ceilings := c.Limits
			tc.mutate(&ceilings)
			_, err := Compile(c, CompileContext{RuntimeCeilings: &ceilings})
			if err == nil || !strings.Contains(err.Error(), tc.reason) {
				t.Fatalf("invalid operator authority accepted: err=%v", err)
			}
		})
	}
}

func TestOperatorCeilingsContributeToEffectiveIdentity(t *testing.T) {
	c := validConfig()
	first := c.Limits
	second := c.Limits
	second.Attempt.ModelCalls++
	a, err := Compile(c, CompileContext{RuntimeCeilings: &first})
	if err != nil {
		t.Fatal(err)
	}
	b, err := Compile(c, CompileContext{RuntimeCeilings: &second})
	if err != nil {
		t.Fatal(err)
	}
	if a.Digest == b.Digest {
		t.Fatal("operator authority changed without changing effective identity")
	}
}
