/*
   Copyright 2026 GitHub Inc.
	 See https://github.com/github/gh-ost/blob/master/LICENSE
*/

package mysql

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	gomysql "github.com/go-sql-driver/mysql"
	"github.com/openark/golib/log"
)

// All MySQL TCP connections (go-sql-driver pools and the go-mysql binlog
// syncer) are dialed through DialContext so that whenever a connection fails
// we can log the layer-4 endpoints it was using, which the drivers themselves
// discard (e.g. "invalid connection", "driver: bad connection").
func init() {
	gomysql.RegisterDialContext("tcp", func(ctx context.Context, addr string) (net.Conn, error) {
		return DialContext(ctx, "tcp", addr)
	})
}

// ConnAddrs records the layer-4 endpoints of a dialed connection.
type ConnAddrs struct {
	mu     sync.Mutex
	dialed string // address as configured, e.g. "db-host:3306"
	local  string
	remote string
}

func (a *ConnAddrs) set(dialed, local, remote string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.dialed, a.local, a.remote = dialed, local, remote
}

// String returns "local->remote (dialed)", or "unknown" if nothing was dialed.
func (a *ConnAddrs) String() string {
	if a == nil {
		return "unknown"
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.remote == "" {
		return "unknown"
	}
	if a.dialed == "" || a.dialed == a.remote {
		return fmt.Sprintf("%s->%s", a.local, a.remote)
	}
	return fmt.Sprintf("%s->%s (%s)", a.local, a.remote, a.dialed)
}

type connAddrsKey struct{}

// WithConnAddrs returns a context that, when used to open a new connection
// (e.g. sql.DB.Conn), records that connection's endpoints into the returned
// ConnAddrs.
func WithConnAddrs(ctx context.Context) (context.Context, *ConnAddrs) {
	addrs := &ConnAddrs{}
	return context.WithValue(ctx, connAddrsKey{}, addrs), addrs
}

// DialContext dials a TCP connection whose I/O failures are logged together
// with its local and remote ip:port.
func DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	nd := net.Dialer{}
	conn, err := nd.DialContext(ctx, network, addr)
	if err != nil {
		return nil, fmt.Errorf("dial %s %s: %w", network, addr, err)
	}
	if tc, ok := conn.(*net.TCPConn); ok {
		_ = tc.SetKeepAlive(true)
	}
	tc := &trackedConn{
		Conn:   conn,
		dialed: addr,
		local:  conn.LocalAddr().String(),
		remote: conn.RemoteAddr().String(),
		opened: time.Now(),
	}
	if addrs, ok := ctx.Value(connAddrsKey{}).(*ConnAddrs); ok {
		addrs.set(tc.dialed, tc.local, tc.remote)
	}
	log.Debugf("mysql connection opened: %s", tc.endpoints())
	return tc, nil
}

// trackedConn wraps a net.Conn and logs its endpoints on the first I/O error
// that isn't caused by our own Close().
type trackedConn struct {
	net.Conn
	dialed string
	local  string
	remote string
	opened time.Time
	closed atomic.Bool
	logged atomic.Bool
}

func (c *trackedConn) endpoints() string {
	if c.dialed == c.remote {
		return fmt.Sprintf("%s->%s", c.local, c.remote)
	}
	return fmt.Sprintf("%s->%s (%s)", c.local, c.remote, c.dialed)
}

func (c *trackedConn) onError(op string, err error) {
	if err == nil || c.closed.Load() || errors.Is(err, net.ErrClosed) {
		return
	}
	if !c.logged.CompareAndSwap(false, true) {
		return
	}
	log.Warningf("mysql connection %s lost after %s: %s: %v",
		c.endpoints(), time.Since(c.opened).Round(time.Second), op, err)
}

func (c *trackedConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	c.onError("read", err)
	return n, err
}

func (c *trackedConn) Write(b []byte) (int, error) {
	n, err := c.Conn.Write(b)
	c.onError("write", err)
	return n, err
}

func (c *trackedConn) Close() error {
	c.closed.Store(true)
	return c.Conn.Close()
}

// SyscallConn keeps go-sql-driver's stale-connection check (conncheck.go)
// working, which requires the net.Conn to implement syscall.Conn.
func (c *trackedConn) SyscallConn() (syscall.RawConn, error) {
	sc, ok := c.Conn.(syscall.Conn)
	if !ok {
		return nil, errors.New("underlying connection does not implement syscall.Conn")
	}
	return sc.SyscallConn()
}
