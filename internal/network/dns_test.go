package network

import (
	"reflect"
	"strings"
	"testing"
)

func TestResolverSelection(t *testing.T) {
	input := "# stub\nnameserver 127.0.0.53\nnameserver ::1\nnameserver bad\nnameserver 8.8.8.8\nnameserver 8.8.8.8\nnameserver 1.1.1.1 # upstream\nnameserver 9.9.9.9\nnameserver 4.4.4.4\n"
	if got := ParseResolvers(strings.NewReader(input)); !reflect.DeepEqual(got, []string{"8.8.8.8", "1.1.1.1", "9.9.9.9"}) {
		t.Fatal(got)
	}
}
