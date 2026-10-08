// Package volume owns guest-local named data directories and their deletion leases.
package volume

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/mingo-liu/casklet/internal/config"
)

var ErrInUse = errors.New("volume is in use or referenced by a retained container")

type Record struct {
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}
type ReferenceCheck func(context.Context, string) (bool, error)
type Leases struct{ Files []*os.File }

func (l *Leases) Close() error {
	var err error
	for _, f := range l.Files {
		err = errors.Join(err, f.Close())
	}
	l.Files = nil
	return err
}
func AcquireMounts(ctx context.Context, mounts []config.BindMount) (*Leases, error) {
	leases := &Leases{}
	names := config.VolumeNames(mounts)
	if len(names) == 0 {
		return leases, nil
	}
	store, err := OpenStore()
	if err != nil {
		return nil, err
	}
	for _, name := range names {
		file, err := store.Acquire(ctx, name)
		if err != nil {
			leases.Close()
			return nil, err
		}
		leases.Files = append(leases.Files, file)
	}
	return leases, nil
}
