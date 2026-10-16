package upstream

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"syscall"
	"testing"
)

// A server that can't be reached is described in one sentence naming the
// host, without the Go error chain (upstream: dial …: dial tcp …) and
// without repeating "Couldn't reach the server", which the UI shows as the
// class's label.
func TestUnreachableMessage(t *testing.T) {
	dial := func(errno syscall.Errno) error {
		op := &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", errno)}
		return &url.Error{Op: "Post", URL: "https://10.0.0.5/mcp", Err: fmt.Errorf("upstream: dial 10.0.0.5: %w", errors.Join(op))}
	}
	for _, c := range []struct {
		err  error
		want string
	}{
		{dial(syscall.ENETUNREACH), "There is no network route to 10.0.0.5 (host or network unreachable)."},
		{dial(syscall.ECONNREFUSED), "The connection to 10.0.0.5 was refused: nothing is listening on that port."},
		{&url.Error{Op: "Post", URL: "https://status.example.com/mcp", Err: &net.DNSError{Err: "no such host", Name: "status.example.com", IsNotFound: true}},
			"No address was found for status.example.com (DNS lookup failed)."},
		{&url.Error{Op: "Post", URL: "https://status.example.com/mcp", Err: errors.New("something odd")}, "The connection to status.example.com failed."},
	} {
		got := unreachableMessage(c.err)
		if got != c.want {
			t.Errorf("got %q, want %q", got, c.want)
		}
		if strings.Contains(got, "upstream") || strings.Contains(got, "dial tcp") || strings.Contains(got, "Couldn't reach") {
			t.Errorf("%q shows internals or repeats the label", got)
		}
	}
}
