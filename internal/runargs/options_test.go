package runargs

import (
	"errors"
	"reflect"
	"testing"
)

func TestWalkPreservesValuesBoundariesAndOriginalSpelling(t *testing.T) {
	args := []string{"run", "-it", "--read-only=false", "--health-cmd", "--publish", "-p", "8080:80", "--image=old", "--image", "last", "--", "--image", "workload"}
	original := append([]string(nil), args...)
	names := []string{}
	err := Walk(args, func(name, value string, start, end int) error {
		names = append(names, name)
		if name == "health-cmd" && value != "--publish" {
			t.Fatal(value)
		}
		if name == "image" {
			SetValue(args, start, end, "new")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(names, []string{"it", "read-only", "health-cmd", "p", "image", "image"}) {
		t.Fatal(names)
	}
	original[7], original[9] = "--image=new", "new"
	if !reflect.DeepEqual(args, original) {
		t.Fatalf("rewritten=%q want=%q", args, original)
	}
	for _, args := range [][]string{{"run", "operand", "--image", "untouched"}, {"run", "-", "--image", "untouched"}, {"run", "---image", "untouched"}} {
		if err := Walk(args, func(string, string, int, int) error { t.Fatal("scanned an operand or malformed flag"); return nil }); err != nil {
			t.Fatal(err)
		}
	}
}

func TestWalkMissingValueAndVisitorFailure(t *testing.T) {
	if err := Walk([]string{"run", "--image"}, func(string, string, int, int) error { t.Fatal("visited missing value"); return nil }); err == nil {
		t.Fatal("accepted missing value")
	}
	sentinel := errors.New("stop")
	if err := Walk([]string{"run", "--help", "--image", "x"}, func(string, string, int, int) error { return sentinel }); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
}
