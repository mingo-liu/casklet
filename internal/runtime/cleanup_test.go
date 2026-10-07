package runtime

import (
	"errors"
	"fmt"
	"reflect"
	"testing"
)

func TestCleanupStagesPreservesJoinedErrorsWithoutPrivateDetails(t *testing.T) {
	cause := errors.New("private resource /run/secret")
	err := errors.Join(errors.New("command failed"), cleanupFailure("cgroup.kill", cause),
		fmt.Errorf("wrapped: %w", errors.Join(cleanupFailure("cgroup.remove", cause), cleanupFailure("cgroup.kill", cause))))
	if got := CleanupStages(err); !reflect.DeepEqual(got, []string{"cgroup.kill", "cgroup.remove"}) {
		t.Fatalf("cleanup stages=%v", got)
	}
	if !errors.Is(err, cause) || len(CleanupStages(nil)) != 0 || cleanupFailure("run.remove", nil) != nil {
		t.Fatal("cleanup classification lost the cause or reported success as failure")
	}
}
