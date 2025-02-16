package internal

import (
	"fmt"
	"net"
	"time"

	"github.com/miekg/dns"
	"github.com/ssttevee/jitaku-dns/internal/dns/upstream"
	"github.com/ssttevee/jitaku-dns/internal/dns/upstream/filter"
	"github.com/ssttevee/jitaku-dns/internal/dns/upstream/rewrite"
)

type MessageResult struct {
	Response *dns.Msg
	Elapsed  time.Duration
	Upstream upstream.Upstream
}

func (r *MessageResult) Status() string {
	if r.Upstream != nil {
		switch u := r.Upstream.(type) {
		case *filter.FilterUpstream:
			return "Blocked"

		case *rewrite.RewriteUpstream:
			return "Rewritten"

		default:
			return fmt.Sprintf("Forwarded to %s", u.String())
		}
	}

	return "Error"
}

func (r *MessageResult) Detail() string {
	if r.Upstream == nil {
		return "No upstreams available"
	}

	switch u := r.Upstream.(type) {
	case *filter.FilterUpstream:
		return u.Filter.Name()
	default:
	}

	return r.Elapsed.String()
}

type LogEntry struct {
	Time           time.Time
	Client         net.Addr
	Request        *dns.Question
	Response       string
	ResponseDetail string
}
