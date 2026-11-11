package network

import (
	"reflect"
	"strings"
	"testing"
)

func TestResolverSelection(t *testing.T) {
	input := `# stub
nameserver 127.0.0.53
nameserver ::1
nameserver bad
nameserver 8.8.8.8
nameserver 8.8.8.8
nameserver 1.1.1.1 # upstream
nameserver 9.9.9.9
nameserver 4.4.4.4
`
	if got := ParseResolvers(strings.NewReader(input)); !reflect.DeepEqual(got, []string{"8.8.8.8", "1.1.1.1", "9.9.9.9"}) {
		t.Fatal(got)
	}
}
