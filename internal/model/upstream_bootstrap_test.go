package model

import (
	"context"
	"errors"
	"net"
	"testing"

	"github.com/naiba/nbdns/pkg/logger"
)

func TestFailedBootstrapDoesNotDialUnspecifiedAddress(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	_, port, _ := net.SplitHostPort(listener.Addr().String())
	up := &Upstream{Address: "tcp://resolver.invalid:" + port}
	up.Init(&Config{Timeout: 1}, logger.New(false))
	lookupErr := errors.New("bootstrap lookup failed")
	up.InitConnectionPool(func(string) (net.IP, error) { return nil, lookupErr })
	conn, err := up.dialUpstreamContext(context.Background(), "tcp", up.hostAndPort)
	if conn != nil {
		_ = conn.Close()
		t.Error("failed bootstrap unexpectedly opened a connection")
	}
	if !errors.Is(err, lookupErr) {
		t.Fatalf("lost bootstrap failure: %v", err)
	}
}
