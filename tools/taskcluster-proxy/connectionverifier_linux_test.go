//go:build linux

package main

import (
	"net"
	"os"
	"os/user"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIpPortToHexIPv4(t *testing.T) {
	tests := []struct {
		ip   string
		port int
		want string
	}{
		{"127.0.0.1", 8080, "0100007F:1F90"},
		{"0.0.0.0", 0, "00000000:0000"},
		{"192.168.1.1", 443, "0101A8C0:01BB"},
		{"10.0.0.1", 65535, "0100000A:FFFF"},
	}
	for _, tt := range tests {
		t.Run(tt.ip, func(t *testing.T) {
			got := ipPortToHex(net.ParseIP(tt.ip), tt.port)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestIpPortToHexIPv6(t *testing.T) {
	tests := []struct {
		name string
		ip   string
		port int
		want string
	}{
		{
			name: "loopback",
			ip:   "::1",
			port: 8080,
			want: "00000000000000000000000001000000:1F90",
		},
		{
			name: "all-zeros",
			ip:   "::",
			port: 0,
			want: "00000000000000000000000000000000:0000",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ipPortToHex(net.ParseIP(tt.ip), tt.port)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestLookupUIDFromProcNetSelf(t *testing.T) {
	// Create a TCP connection and verify lookupUIDFromProcNet returns
	// the current process's UID.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()

	done := make(chan net.Conn, 1)
	go func() {
		conn, _ := ln.Accept()
		done <- conn
	}()

	client, err := net.Dial("tcp", ln.Addr().String())
	require.NoError(t, err)
	defer client.Close()

	server := <-done
	defer server.Close()

	clientAddr := server.RemoteAddr().(*net.TCPAddr)

	uid, err := lookupUIDFromProcNet(clientAddr)
	require.NoError(t, err)
	assert.Equal(t, uint32(os.Getuid()), uid)
}

func TestVerifiedListenerSelfConnection(t *testing.T) {
	// End-to-end: create a verifier for the current user, connect, verify.
	// This test only runs on Linux because the Darwin verifier filters by
	// PID, and in a single-process test both sides share the same PID.
	u, err := user.Current()
	require.NoError(t, err)

	v, err := newConnectionVerifier(u.Username)
	require.NoError(t, err)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()

	vl := &verifiedListener{
		Listener: ln,
		verifier: v,
	}

	go func() {
		conn, dialErr := net.Dial("tcp", ln.Addr().String())
		if dialErr == nil {
			// Keep open; deferred ln.Close() will unblock Accept
			// and the test will clean up.
			defer conn.Close()
			select {}
		}
	}()

	conn, err := vl.Accept()
	require.NoError(t, err)
	conn.Close()
}
