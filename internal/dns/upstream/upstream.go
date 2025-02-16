package upstream

import (
	"github.com/miekg/dns"
)

type Upstream interface {
	String() string
	ForwardMessage(msg *dns.Msg) (*dns.Msg, error)
	Close() error
}
