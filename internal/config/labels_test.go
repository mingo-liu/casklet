package config

import (
	"fmt"
	"strings"
	"testing"
)

func TestLabelsPreserveLiteralValuesAndBoundMetadata(t *testing.T) {
	for _, value := range []string{"", "demo", "a=b=c", "中文 $HOME # literal"} {
		key, got, err := ParseLabel("org.example/project=" + value)
		if err != nil || key != "org.example/project" || got != value {
			t.Fatalf("literal %q: %q %q %v", value, key, got, err)
		}
	}
	for _, assignment := range []string{"project", "=demo", "-key=x", "bad key=x", strings.Repeat("k", 129) + "=x", "key=\x00", "key=\n", "key=\xff", "key=" + strings.Repeat("a", 4097)} {
		if _, _, err := ParseLabel(assignment); err == nil {
			t.Fatalf("accepted %q", assignment)
		}
	}
	labels := map[string]string{}
	for i := 0; i < 65; i++ {
		labels[fmt.Sprintf("k%d", i)] = ""
	}
	if err := ValidateLabels(labels); err == nil {
		t.Fatal("accepted too many labels")
	}
	labels = map[string]string{}
	for i := 0; i < 5; i++ {
		labels[fmt.Sprintf("k%d", i)] = strings.Repeat("x", 4096)
	}
	if err := ValidateLabels(labels); err == nil {
		t.Fatal("accepted oversized metadata")
	}
	c := Config{Command: []string{"true"}, Labels: map[string]string{"project": "demo"}}
	if err := c.ValidateExecution(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(c.CommandEnvironment(), "\n"), "project") {
		t.Fatal("label leaked into workload environment")
	}
	c.Labels["bad key"] = "x"
	if err := c.ValidateExecution(); err == nil {
		t.Fatal("execution accepted invalid labels")
	}
}
