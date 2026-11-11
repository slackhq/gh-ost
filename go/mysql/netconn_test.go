/*
   Copyright 2026 GitHub Inc.
	 See https://github.com/github/gh-ost/blob/master/LICENSE
*/

package mysql

import (
	"context"
	"net"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDialContextRecordsAddrs(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		c, _ := ln.Accept()
		accepted <- c
	}()

	ctx, addrs := WithConnAddrs(context.Background())
	require.Equal(t, "unknown", addrs.String())
	conn, err := DialContext(ctx, "tcp", ln.Addr().String())
	require.NoError(t, err)
	server := <-accepted

	require.Equal(t, conn.LocalAddr().String()+"->"+ln.Addr().String(), addrs.String())
	_, ok := conn.(syscall.Conn)
	require.True(t, ok, "trackedConn must implement syscall.Conn for go-sql-driver's connCheck")

	// Remote close is a lost connection: the read error is logged once.
	tc := conn.(*trackedConn)
	server.Close()
	_, err = conn.Read(make([]byte, 1))
	require.Error(t, err)
	require.True(t, tc.logged.Load())

	// Our own Close is not a lost connection.
	ctx2, _ := WithConnAddrs(context.Background())
	go func() {
		c, _ := ln.Accept()
		accepted <- c
	}()
	conn2, err := DialContext(ctx2, "tcp", ln.Addr().String())
	require.NoError(t, err)
	defer (<-accepted).Close()
	conn2.Close()
	_, err = conn2.Read(make([]byte, 1))
	require.Error(t, err)
	require.False(t, conn2.(*trackedConn).logged.Load())
}

func TestConnAddrsStringIncludesDialedHostname(t *testing.T) {
	a := &ConnAddrs{}
	a.set("db-primary:3306", "10.0.0.1:50000", "10.0.0.2:3306")
	require.Equal(t, "10.0.0.1:50000->10.0.0.2:3306 (db-primary:3306)", a.String())
	var nilAddrs *ConnAddrs
	require.Equal(t, "unknown", nilAddrs.String())
}
