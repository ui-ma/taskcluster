package main

import (
	"net"
	"os/user"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNoopVerifierAllowsAll(t *testing.T) {
	v := &noopVerifier{}

	// Create a real TCP connection pair
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

	// noopVerifier should allow the connection
	assert.NoError(t, v.Verify(client))
	assert.NoError(t, v.Verify(server))
}

// rejectVerifier is a mock that always rejects connections.
type rejectVerifier struct{}

func (r *rejectVerifier) Verify(_ net.Conn) error {
	return &ErrUnauthorizedConnection{
		ExpectedUser: "allowed-user",
		ActualUID:    "9999",
		RemoteAddr:   "127.0.0.1:0",
	}
}

func TestVerifiedListenerRejectsUnauthorized(t *testing.T) {
	// Set up a raw listener wrapped by a verifiedListener with a reject verifier
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()

	vl := &verifiedListener{
		Listener: ln,
		verifier: &rejectVerifier{},
	}

	// Connect a client — the verifiedListener should reject it and keep
	// waiting, so Accept will block until we close the underlying listener.
	client, err := net.Dial("tcp", ln.Addr().String())
	require.NoError(t, err)
	defer client.Close()

	// Close the underlying listener to unblock Accept
	go func() {
		// Give Accept time to reject the first connection
		client2, err := net.Dial("tcp", ln.Addr().String())
		if err == nil {
			client2.Close()
		}
		ln.Close()
	}()

	_, err = vl.Accept()
	// Should get a "use of closed network connection" error since we
	// closed the listener — all real connections were rejected
	require.Error(t, err)
}

func TestVerifiedListenerAcceptsAuthorized(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()

	vl := &verifiedListener{
		Listener: ln,
		verifier: &noopVerifier{},
	}

	go func() {
		conn, err := net.Dial("tcp", ln.Addr().String())
		if err == nil {
			defer conn.Close()
		}
	}()

	conn, err := vl.Accept()
	require.NoError(t, err)
	defer conn.Close()
}

func TestErrUnauthorizedConnectionMessage(t *testing.T) {
	err := &ErrUnauthorizedConnection{
		ExpectedUser: "task-user",
		ActualUID:    "1234",
		RemoteAddr:   "127.0.0.1:54321",
	}
	msg := err.Error()
	assert.Contains(t, msg, "1234")
	assert.Contains(t, msg, "task-user")
	assert.Contains(t, msg, "127.0.0.1:54321")
}

func TestNewConnectionVerifierEmptyUsername(t *testing.T) {
	v, err := newConnectionVerifier("")
	require.NoError(t, err)
	_, ok := v.(*noopVerifier)
	assert.True(t, ok, "empty username should return noopVerifier")
}

func TestNewConnectionVerifierCurrentUser(t *testing.T) {
	// Use current user so the lookup succeeds on any platform
	v, err := newConnectionVerifier(currentUsername(t))
	require.NoError(t, err)
	assert.NotNil(t, v)
}

func TestNewConnectionVerifierBadUsername(t *testing.T) {
	_, err := newConnectionVerifier("nonexistent-user-abc123xyz")
	require.Error(t, err)
}

func currentUsername(t *testing.T) string {
	t.Helper()
	u, err := user.Current()
	require.NoError(t, err)
	return u.Username
}
