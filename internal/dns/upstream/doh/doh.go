package doh

import (
	"net/http"

	"github.com/miekg/dns"
	"github.com/ssttevee/jitaku-dns/internal/dns/dohutil"
)

type DoHUpstream struct {
	Client *http.Client
	URL    string
}

func (d *DoHUpstream) String() string {
	return d.URL
}

func (d *DoHUpstream) ForwardMessage(msg *dns.Msg) (*dns.Msg, error) {
	client := http.DefaultClient
	if d.Client != nil {
		client = d.Client
	}

	return dohutil.Exchange(client, d.URL, msg)
}

func (d *DoHUpstream) Close() error {
	return nil
}
