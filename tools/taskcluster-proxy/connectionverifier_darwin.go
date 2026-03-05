//go:build darwin

package main

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/user"
	"strconv"
	"strings"
	"sync"
)

type darwinVerifier struct {
	allowedUID uint32
	username   string
	proxyPID   int
	// verified caches remote addresses (ip:port strings) that have passed
	// verification, so that repeated connections from the same endpoint
	// skip the expensive lsof lookup. A TCP source port is bound to one
	// process for its lifetime, and TIME_WAIT prevents immediate reuse.
	verified sync.Map
}

func newPlatformVerifier(username string) (ConnectionVerifier, error) {
	u, err := user.Lookup(username)
	if err != nil {
		return nil, fmt.Errorf("failed to look up user %q: %w", username, err)
	}
	uid, err := strconv.ParseUint(u.Uid, 10, 32)
	if err != nil {
		return nil, fmt.Errorf("failed to parse UID %q: %w", u.Uid, err)
	}
	return &darwinVerifier{
		allowedUID: uint32(uid),
		username:   username,
		proxyPID:   os.Getpid(),
	}, nil
}

func (v *darwinVerifier) Verify(conn net.Conn) error {
	tcpAddr, ok := conn.RemoteAddr().(*net.TCPAddr)
	if !ok {
		return fmt.Errorf("connection is not TCP")
	}

	key := tcpAddr.String()
	if _, ok := v.verified.Load(key); ok {
		return nil
	}

	uid, err := lookupUIDWithLsof(tcpAddr, v.proxyPID)
	if err != nil {
		return fmt.Errorf("failed to look up UID for %s: %w", tcpAddr, err)
	}

	// Always allow root (UID 0) - the worker process runs as root and
	// needs to reach tc-proxy for credential refresh and health checks.
	if uid == 0 {
		v.verified.Store(key, struct{}{})
		return nil
	}

	if uid != v.allowedUID {
		return &ErrUnauthorizedConnection{
			ExpectedUser: v.username,
			ActualUID:    strconv.FormatUint(uint64(uid), 10),
			RemoteAddr:   conn.RemoteAddr().String(),
		}
	}
	v.verified.Store(key, struct{}{})
	return nil
}

// lookupUIDWithLsof uses lsof to find the UID of the process that owns
// the TCP connection from the given address. proxyPID is the PID of the
// proxy process itself, which must be excluded from the results because
// lsof -i matches both local and foreign addresses — the proxy's
// accepted-connection entry (foreign = client addr) would otherwise
// shadow the client's entry.
func lookupUIDWithLsof(addr *net.TCPAddr, proxyPID int) (uint32, error) {
	// -F pu: output PID ('p' prefix) and UID ('u' prefix) fields
	// -sTCP:ESTABLISHED: only established connections
	// -n -P: no DNS/port name resolution
	out, err := exec.Command("lsof",
		"-i", fmt.Sprintf("tcp@%s:%d", addr.IP, addr.Port),
		"-sTCP:ESTABLISHED",
		"-F", "pu",
		"-n", "-P",
	).Output()
	if err != nil {
		return 0, fmt.Errorf("lsof failed: %w", err)
	}

	// Parse lsof -F output: 'p' lines carry the PID, 'u' lines the UID.
	// Skip entries whose PID matches the proxy itself.
	var currentPID int
	for line := range strings.SplitSeq(string(out), "\n") {
		if strings.HasPrefix(line, "p") {
			pid, parseErr := strconv.Atoi(line[1:])
			if parseErr != nil {
				continue
			}
			currentPID = pid
		} else if strings.HasPrefix(line, "u") && currentPID != proxyPID {
			uid, parseErr := strconv.ParseUint(line[1:], 10, 32)
			if parseErr != nil {
				return 0, fmt.Errorf("failed to parse UID from lsof output %q: %w", line, parseErr)
			}
			return uint32(uid), nil
		}
	}
	return 0, fmt.Errorf("no UID found in lsof output for %s", addr)
}
