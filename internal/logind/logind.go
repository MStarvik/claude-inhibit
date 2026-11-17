// Package logind takes systemd-logind inhibitor locks over D-Bus.
//
// logind's Inhibit method returns a file descriptor; the lock lasts exactly as
// long as that descriptor is open. If this process dies the kernel closes it,
// so a lock can never outlive us.
package logind

import (
	"fmt"
	"os"

	"github.com/godbus/dbus/v5"
)

// Client talks to logind on the system bus.
type Client struct {
	conn *dbus.Conn
}

// Connect opens a private connection to the system bus.
func Connect() (*Client, error) {
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		return nil, fmt.Errorf("connect to system bus: %w", err)
	}
	return &Client{conn: conn}, nil
}

// Close closes the bus connection. Locks already taken stay held until their
// own Close is called.
func (c *Client) Close() error { return c.conn.Close() }

// Lock is a held inhibitor lock.
type Lock struct{ f *os.File }

// Release drops the lock. It is safe to call on a nil Lock or more than once.
func (l *Lock) Release() error {
	if l == nil || l.f == nil {
		return nil
	}
	err := l.f.Close()
	l.f = nil
	return err
}

// Inhibit takes a lock. what is a colon-separated list such as "sleep:idle";
// mode is "block" or "delay". See org.freedesktop.login1(5).
func (c *Client) Inhibit(what, who, why, mode string) (*Lock, error) {
	var fd dbus.UnixFD
	err := c.conn.Object("org.freedesktop.login1", "/org/freedesktop/login1").
		Call("org.freedesktop.login1.Manager.Inhibit", 0, what, who, why, mode).
		Store(&fd)
	if err != nil {
		return nil, fmt.Errorf("logind Inhibit: %w", err)
	}
	return &Lock{f: os.NewFile(uintptr(fd), "logind-inhibitor")}, nil
}
