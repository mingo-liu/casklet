package container

const RecordPageSize = 64

// RecordPageOptions selects immutable identities before lifecycle refresh.
// Labels are immutable; state and health must be sampled after selection.
type RecordPageOptions struct {
	After      string
	Labels     map[string]string
	ExcludeIDs []string
}

// RecordPage traverses full IDs in increasing order. A continuation is the last
// returned ID; a filtered traversal may finish with an empty final page.
type RecordPage struct {
	Records []Record `json:"records"`
	Next    string   `json:"next,omitempty"`
}
