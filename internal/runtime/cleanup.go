package runtime

import (
	"errors"
	"fmt"
)

// CleanupError separates resource cleanup failures from the command's exit code.
// Stage is safe to expose; the wrapped error can contain private runtime paths.
type CleanupError struct {
	Stage string
	Err   error
}

func (err *CleanupError) Error() string { return fmt.Sprintf("cleanup %s: %v", err.Stage, err.Err) }
func (err *CleanupError) Unwrap() error { return err.Err }

func cleanupFailure(stage string, err error) error {
	if err == nil {
		return nil
	}
	return &CleanupError{Stage: stage, Err: err}
}

// CleanupStages returns unique failure stages, including errors joined by
// deferred cleanup. Error messages and private resource paths are excluded.
func CleanupStages(err error) []string {
	var stages []string
	seen := make(map[string]bool)
	var visit func(error)
	visit = func(err error) {
		if err == nil {
			return
		}
		if failure, ok := err.(*CleanupError); ok && !seen[failure.Stage] {
			seen[failure.Stage] = true
			stages = append(stages, failure.Stage)
		}
		switch wrapped := err.(type) {
		case interface{ Unwrap() []error }:
			for _, cause := range wrapped.Unwrap() {
				visit(cause)
			}
		case interface{ Unwrap() error }:
			visit(errors.Unwrap(err))
		}
	}
	visit(err)
	return stages
}
