package runtime

// ExitStatus describes observed termination separately from the command exit code.
// An empty reason means that no result was recorded by this runtime version.
type ExitStatus struct {
	ExitReason string
	OOMKilled  bool
}

func (status *ExitStatus) finish(started bool) {
	if status.OOMKilled {
		status.ExitReason = "oom_killed"
	} else if status.ExitReason == "" {
		status.ExitReason = "startup_failed"
		if started {
			status.ExitReason = "runtime_error"
		}
	}
}
