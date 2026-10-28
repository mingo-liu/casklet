package config

import "testing"

func TestSecretSourceAndTargetValidation(t *testing.T) {
	secret, err := ParseSecret("source=token,target=app-token", "/config")
	if err != nil || secret.Source != "/config/token" || secret.Target != "app-token" {
		t.Fatalf("relative: %+v %v", secret, err)
	}
	for _, value := range []string{"", "source=relative,target=token", "source=/token", "source=/token,target=../escape", "source=/token,target=.", "source=/token,target=one/two", "source=/token,target=x,readonly", "source=/token,source=/other,target=x"} {
		if _, err := ParseSecret(value, ""); err == nil {
			t.Fatalf("accepted %q", value)
		}
	}
	if err := ValidateSecrets([]Secret{secret, secret}, nil); err == nil {
		t.Fatal("duplicate target")
	}
	for _, target := range []string{"/run", "/run/secrets", "/run/secrets/child"} {
		if err := ValidateSecrets([]Secret{secret}, []BindMount{{Target: target}}); err == nil {
			t.Fatalf("overlap %s", target)
		}
	}
}
