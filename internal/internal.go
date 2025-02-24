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
	Cached   bool
}

type wrappedUpstream interface {
	Inner() upstream.Upstream
}

func resultStatusFromUpstream(u upstream.Upstream) string {
	if u != nil {
		switch u := u.(type) {
		case wrappedUpstream:
			return resultStatusFromUpstream(u.Inner())

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

func (r *MessageResult) Status() string {
	if r.Cached {
		return "Cached"
	}

	return resultStatusFromUpstream(r.Upstream)
}

func (r *MessageResult) Detail() string {
	if r.Upstream == nil {
		if r.Cached {
			return "From unknown upstream"
		}

		return "No upstreams available"
	}

	if r.Cached {
		return "From " + r.Upstream.String() + " (" + r.Elapsed.String() + ")"
	}

	switch u := r.Upstream.(type) {
	case *filter.FilterUpstream:
		return u.String()
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
