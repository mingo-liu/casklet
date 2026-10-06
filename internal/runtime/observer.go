package runtime

// Event describes a startup milestone after its resources have been recorded.
// Observers run synchronously; an error prevents further command execution.
type Event struct {
	Phase   string
	RunPath string
	Cgroup  string
}

type Observer func(Event) error
