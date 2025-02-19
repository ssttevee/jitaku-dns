package upstream

import (
	"context"

	"github.com/miekg/dns"
)

type Upstream interface {
	String() string
	ForwardMessage(ctx context.Context, msg *dns.Msg) (*dns.Msg, error)
	Close() error
}

type NoopUpstream struct{}

func (n *NoopUpstream) String() string {
	return "noop"
}

func (n *NoopUpstream) ForwardMessage(ctx context.Context, msg *dns.Msg) (*dns.Msg, error) {
	return nil, nil
}

func (n *NoopUpstream) Close() error {
	return nil
}
