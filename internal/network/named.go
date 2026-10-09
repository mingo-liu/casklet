package network

import (
	"context"
	"errors"
	"github.com/mingo-liu/casklet/internal/config"
	"os"
	"time"
)

const NamedRoot = "/var/lib/casklet/networks"

var ErrInUse = errors.New("network is in use or referenced by a retained container")

type Record struct {
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
	Bridge    string    `json:"bridge"`
	Subnet    string    `json:"subnet"`
	Gateway   string    `json:"gateway"`
}
type ReferenceCheck func(context.Context, string) (bool, error)

func AcquireNetwork(ctx context.Context, mode string) (*os.File, error) {
	if !config.IsNamedNetwork(mode) {
		return nil, nil
	}
	store, err := OpenStore()
	if err != nil {
		return nil, err
	}
	return store.Acquire(ctx, mode)
}
