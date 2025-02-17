package rewrite

import (
	"net"
	"strings"

	"github.com/miekg/dns"
)

type RewriteUpstream struct {
	ConfigLines []string

	V4 map[string][]net.IP
	V6 map[string][]net.IP
}

func NewRewriteUpstream(lines []string) *RewriteUpstream {
	up := &RewriteUpstream{
		ConfigLines: lines,
		V4:          make(map[string][]net.IP),
		V6:          make(map[string][]net.IP),
	}

	for _, line := range up.ConfigLines {
		halves := strings.SplitN(strings.TrimSpace(line), "#", 2)
		parts := strings.Split(strings.TrimSpace(halves[0]), " ")

		ip := net.ParseIP(parts[0])
		if ip == nil {
			continue
		}

		var name string
		for _, part := range parts[1:] {
			if part == "" {
				continue
			}

			name = part
			break
		}

		if v4 := ip.To4(); v4 != nil {
			up.V4[name] = append(up.V4[name], v4)
		} else {
			up.V6[name] = append(up.V6[name], ip)
		}
	}

	return up
}

func (c *RewriteUpstream) Empty() bool {
	return len(c.V4) == 0 && len(c.V6) == 0
}

func (c *RewriteUpstream) String() string {
	return "rewrite"
}

func (c *RewriteUpstream) Close() error {
	return nil
}

func (c *RewriteUpstream) ForwardMessage(msg *dns.Msg) (*dns.Msg, error) {
	if msg.Opcode != dns.OpcodeQuery || len(msg.Question) < 1 {
		return nil, nil
	}

	res := &dns.Msg{}
	res.SetReply(msg)
	for _, q := range msg.Question {
		if q.Qclass != dns.ClassINET {
			continue
		}

		if q.Qtype == dns.TypeA || q.Qtype == dns.TypeANY {
			ips, ok := c.V4[q.Name]
			if !ok && strings.HasSuffix(q.Name, ".") {
				ips, ok = c.V4[q.Name[:len(q.Name)-1]]
			}

			for _, ip := range ips {
				res.Answer = append(res.Answer, &dns.A{
					Hdr: dns.RR_Header{
						Name:   q.Name,
						Rrtype: dns.TypeA,
						Class:  dns.ClassINET,
						Ttl:    300,
					},
					A: ip,
				})
			}
		}

		if q.Qtype == dns.TypeAAAA || q.Qtype == dns.TypeANY {
			ips, ok := c.V6[q.Name]
			if !ok && strings.HasSuffix(q.Name, ".") {
				ips, ok = c.V6[q.Name[:len(q.Name)-1]]
			}

			for _, ip := range ips {
				res.Answer = append(res.Answer, &dns.AAAA{
					Hdr: dns.RR_Header{
						Name:   q.Name,
						Rrtype: dns.TypeAAAA,
						Class:  dns.ClassINET,
						Ttl:    300,
					},
					AAAA: ip,
				})
			}
		}
	}

	if len(res.Answer) == 0 {
		return nil, nil
	}

	return res, nil
}
