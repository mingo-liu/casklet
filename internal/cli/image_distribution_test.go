package cli

import "testing"

func TestImageDistributionArguments(t *testing.T) {
	for _, action := range []string{"tag", "push"} {
		r, err := Parse([]string{"image", action, "abc123", "example.com/team/app:v1"})
		if err != nil || r.Reference != "abc123" || r.Target != "example.com/team/app:v1" {
			t.Fatalf("%s: %+v %v", action, r, err)
		}
		for _, operands := range [][]string{nil, {"one"}, {"one", "two", "three"}, {"one", "app@sha256:bad"}, {"https://bad", "app"}} {
			if _, err := Parse(append([]string{"image", action}, operands...)); err == nil {
				t.Fatalf("accepted %v", operands)
			}
		}
	}
}
