package cli

import (
	"flag"
	"testing"

	"github.com/mingo-liu/casklet/internal/runargs"
)

// Check actual declarations so a newly added boolean cannot silently consume
// the following resource option in a host-side scanner.
func TestRunArgumentGrammarMatchesRegisteredFlags(t *testing.T) {
	for _, action := range []string{"run", "doctor"} {
		r := Request{Action: action}
		var memory, cpus, user string
		fs := runFlagSet(&r, &memory, &cpus, &user)
		fs.VisitAll(func(f *flag.Flag) {
			boolean, ok := f.Value.(interface{ IsBoolFlag() bool })
			want := ok && boolean.IsBoolFlag()
			if got := runargs.IsBoolean(f.Name); got != want {
				t.Errorf("%s --%s: boolean=%v want %v", action, f.Name, got, want)
			}
		})
	}
}
