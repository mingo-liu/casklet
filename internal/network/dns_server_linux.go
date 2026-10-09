//go:build linux

package network

import (
	"errors"
	"golang.org/x/sys/unix"
	"net"
	"os"
	"path/filepath"
	"strings"
)

func startDNS(gateway, address, network string, upstream []string) (*dnsServer, error) {
	return newDNS(gateway, address, upstream, func(name string) (net.IP, bool, error) {
		name = strings.TrimSuffix(name, ".casklet")
		records, err := allocations()
		if err != nil {
			return nil, false, err
		}
		boot, err := currentBoot()
		if err != nil {
			return nil, false, err
		}
		for path, r := range records {
			if r.Network != network || r.BootID != boot {
				continue
			}
			live, err := activeRun(path)
			if err != nil {
				return nil, false, err
			}
			if !live {
				continue
			}
			for _, alias := range r.Aliases {
				if name == alias {
					return net.ParseIP(r.Address), true, nil
				}
			}
		}
		return nil, false, nil
	})
}

// The runtime holds this existing lock until cleanup finishes. Unlocked
// abandoned journals remain allocation reservations but are no longer DNS data.
func activeRun(path string) (bool, error) {
	fd, err := unix.Open(filepath.Join(path, "lock"), unix.O_RDWR|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer unix.Close(fd)
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return false, err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG || st.Uid != 0 || st.Mode&0022 != 0 || st.Nlink != 1 {
		return false, errors.New("unsafe DNS execution lock")
	}
	err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
	if errors.Is(err, unix.EWOULDBLOCK) {
		return true, nil
	}
	return false, err
}
