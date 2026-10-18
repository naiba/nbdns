package model

import (
	"context"
	"net"
	"time"

	"github.com/miekg/dns"
)

// DNS ExchangeContext observes deadlines but does not interrupt an in-progress
// read on cancellation. Close the owned socket to promptly stop losing queries.
func closeConnectionOnCancel(ctx context.Context, conn net.Conn) func() {
	if ctx.Done() == nil {
		return func() {}
	}
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		_ = conn.SetDeadline(time.Now())
		_ = conn.Close()
		close(done)
	})
	return func() {
		if !stop() {
			<-done
		}
	}
}

func exchangeDNSContext(ctx context.Context, client *dns.Client, req *dns.Msg, address string) (*dns.Msg, time.Duration, error) {
	conn, err := client.DialContext(ctx, address)
	if err != nil {
		return nil, 0, err
	}
	defer conn.Close()
	stopCancellation := closeConnectionOnCancel(ctx, conn)
	defer stopCancellation()
	return client.ExchangeWithConnContext(ctx, req, conn)
}
