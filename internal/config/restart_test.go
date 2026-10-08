package config

import (
	"testing"
)

func TestRestartPolicies(t *testing.T) {
	for _, value := range []string{"", "no", "always", "unless-stopped", "on-failure", "on-failure:1", "on-failure:1000"} {
		if _, _, err := ParseRestartPolicy(value); err != nil {
			t.Fatalf("%q: %v", value, err)
		}
	}
	for _, value := range []string{"never", "on-failure:0", "on-failure:-1", "on-failure:1001", "on-failure:", "always:2", " on-failure"} {
		if _, _, err := ParseRestartPolicy(value); err == nil {
			t.Fatalf("accepted %q", value)
		}
	}
}
