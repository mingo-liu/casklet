// Package storage measures guest disk allocation without following workload paths.
package storage

type Filesystem struct {
	TotalBytes     uint64 `json:"total_bytes"`
	FreeBytes      uint64 `json:"free_bytes"`
	AvailableBytes uint64 `json:"available_bytes"`
	LowSpace       bool   `json:"low_space"`
}
type Category struct {
	Kind           string `json:"kind"`
	AllocatedBytes uint64 `json:"allocated_bytes"`
}
type Report struct {
	Filesystem Filesystem `json:"filesystem"`
	Categories []Category `json:"categories"`
}
