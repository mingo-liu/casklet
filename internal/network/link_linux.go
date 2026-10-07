//go:build linux

package network

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

func attribute(kind uint16, value []byte) []byte {
	length := 4 + len(value)
	result := make([]byte, (length+3)&^3)
	binary.NativeEndian.PutUint16(result[:2], uint16(length))
	binary.NativeEndian.PutUint16(result[2:4], kind)
	copy(result[4:], value)
	return result
}

func ownershipGroup(owner string) uint32 {
	sum := sha256.Sum256([]byte(owner))
	return 0x4d000000 | (binary.BigEndian.Uint32(sum[:4]) & 0x00ffffff)
}

// Linux 6.8 applies IFLA_GROUP during creation, but only applies IFLA_IFALIAS
// on an existing link. The immutable-at-startup group identifies ownership even
// if the supervisor dies between link creation and its descriptive alias.
func addLink(name, alias, kind, peer string) error {
	const nested = 1 << 15
	attrs := attribute(unix.IFLA_IFNAME, append([]byte(name), 0))
	group := make([]byte, 4)
	binary.NativeEndian.PutUint32(group, ownershipGroup(alias))
	attrs = append(attrs, attribute(unix.IFLA_GROUP, group)...)
	info := attribute(unix.IFLA_INFO_KIND, append([]byte(kind), 0))
	if peer != "" {
		peerInfo := make([]byte, unix.SizeofIfInfomsg)
		peerInfo = append(peerInfo, attribute(unix.IFLA_IFNAME, append([]byte(peer), 0))...)
		// VETH_INFO_PEER = 1, whose payload starts with an ifinfomsg.
		data := attribute(1|nested, peerInfo)
		info = append(info, attribute(unix.IFLA_INFO_DATA|nested, data)...)
	}
	attrs = append(attrs, attribute(unix.IFLA_LINKINFO|nested, info)...)
	message := make([]byte, unix.NLMSG_HDRLEN+unix.SizeofIfInfomsg)
	message = append(message, attrs...)
	binary.NativeEndian.PutUint32(message[:4], uint32(len(message)))
	binary.NativeEndian.PutUint16(message[4:6], unix.RTM_NEWLINK)
	binary.NativeEndian.PutUint16(message[6:8], unix.NLM_F_REQUEST|unix.NLM_F_ACK|unix.NLM_F_CREATE|unix.NLM_F_EXCL)
	binary.NativeEndian.PutUint32(message[8:12], 1)
	fd, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_RAW|unix.SOCK_CLOEXEC, unix.NETLINK_ROUTE)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	if err := unix.SetsockoptTimeval(fd, unix.SOL_SOCKET, unix.SO_RCVTIMEO, &unix.Timeval{Sec: 5}); err != nil {
		return err
	}
	if err := unix.Bind(fd, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}); err != nil {
		return err
	}
	if err := unix.Sendto(fd, message, 0, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}); err != nil {
		return err
	}
	reply := make([]byte, 8192)
	n, _, err := unix.Recvfrom(fd, reply, 0)
	if err != nil {
		return err
	}
	if n < unix.NLMSG_HDRLEN+4 || binary.NativeEndian.Uint16(reply[4:6]) != unix.NLMSG_ERROR || binary.NativeEndian.Uint32(reply[8:12]) != 1 {
		return errors.New("invalid link creation acknowledgement")
	}
	errno := int32(binary.NativeEndian.Uint32(reply[unix.NLMSG_HDRLEN:]))
	if errno != 0 {
		return fmt.Errorf("create %s link %s: %w", kind, name, unix.Errno(-errno))
	}
	return nil
}
