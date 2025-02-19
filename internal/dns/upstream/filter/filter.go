package filter

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"reflect"
	"strings"
	"time"

	"github.com/miekg/dns"
	"github.com/patriciy/adblock/adblock"
)

type FilterUpstream struct {
	url    string
	filter Filter
}

func NewFilterUpstream(ctx context.Context, hc *http.Client, url string) (*FilterUpstream, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %v", err)
	}

	res, err := hc.Do(req.WithContext(ctx))
	if err != nil {
		return nil, fmt.Errorf("failed to fetch filter: %v", err)
	}

	defer res.Body.Close()

	start := time.Now()
	f, err := parseFilter(ctx, res.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to parse filter: %v", err)
	}

	log.Printf("INFO: parsed filter in %v", time.Since(start))

	return &FilterUpstream{
		url:    url,
		filter: f,
	}, nil
}

func (f *FilterUpstream) String() string {
	return f.url
}

func (f *FilterUpstream) FilterType() reflect.Type {
	return reflect.TypeOf(f.filter)
}

func (f *FilterUpstream) shouldBlock(msg *dns.Msg) (bool, error) {
	if msg.Opcode != dns.OpcodeQuery || len(msg.Question) < 1 {
		return false, nil
	}

	for _, q := range msg.Question {
		ok, err := f.filter.ShouldBlock(q.Name)
		if err != nil {
			return false, err
		}

		if ok {
			return true, nil
		}
	}

	return false, nil
}

func (f *FilterUpstream) ForwardMessage(ctx context.Context, msg *dns.Msg) (*dns.Msg, error) {
	ok, err := f.shouldBlock(msg)
	if err != nil {
		return nil, fmt.Errorf("filter match failed: %v", err)
	}

	if ok {
		res := &dns.Msg{}
		res.SetReply(msg)
		return res, nil
	}

	return nil, nil
}

func (f *FilterUpstream) Close() error {
	return nil
}

type Filter interface {
	ShouldBlock(name string) (bool, error)
	RuleCount() int
}

type HostsFilter struct {
	hosts map[string]struct{}
}

func (f *HostsFilter) RuleCount() int {
	return len(f.hosts)
}

func (f *HostsFilter) ShouldBlock(name string) (bool, error) {
	_, ok := f.hosts[name]
	return ok, nil
}

type ABPFilter struct {
	matcher *adblock.RuleMatcher
	count   int
}

func (f *ABPFilter) RuleCount() int {
	return f.count
}

func (f *ABPFilter) ShouldBlock(name string) (bool, error) {
	name = dns.Fqdn(name)

	ok, mode, err := f.matcher.Match(&adblock.Request{
		URL: fmt.Sprintf("https://%s/", name[0:len(name)-1]),
	})
	if err != nil {
		return false, err
	}
	if ok && mode == adblock.Included {
		return true, nil
	}

	return false, nil
}

func SplitHostsFileLine(line string) (string, string) {
	halves := strings.SplitN(strings.TrimSpace(line), "#", 2)
	parts := strings.Split(strings.TrimSpace(halves[0]), " ")

	var name string
	for _, part := range parts[1:] {
		if part == "" {
			continue
		}

		name = part
		break
	}

	return parts[0], name
}

func parseHostsFileFilter(ctx context.Context, r io.Reader) (Filter, error) {
	hosts := make(map[string]struct{})

	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		ip, name := SplitHostsFileLine(scanner.Text())
		if ip != "0.0.0.0" || name == "" {
			continue
		}

		if _, ok := hosts[name]; !ok {
			hosts[name] = struct{}{}
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return &HostsFilter{
		hosts: hosts,
	}, nil
}

func parseABPFilter(ctx context.Context, r io.Reader) (Filter, error) {
	rules, err := adblock.ParseRules(r)
	if err != nil {
		return nil, err
	}

	matcher := adblock.NewMatcher()
	for i, rule := range rules {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		if err := matcher.AddRule(rule, i); err != nil {
			return nil, fmt.Errorf("failed to add rule: %v", err)
		}
	}

	return &ABPFilter{
		matcher: matcher,
	}, nil
}

func parseFilter(ctx context.Context, r io.Reader) (Filter, error) {
	buffered := bufio.NewReader(r)
	head, err := buffered.Peek(buffered.Size())
	var pos int
	for (err == nil || err == io.EOF) && pos < len(head) {
		n, token, _ := bufio.ScanLines(head[pos:], err == io.EOF)
		if n == 0 {
			// read more data
			buffered.Discard(pos)
			head, err = buffered.Peek(buffered.Size())
			pos = 0
			continue
		}

		line := strings.TrimSpace(string(token))
		if len(line) > 0 && !strings.HasPrefix(line, "#") && !strings.HasPrefix(line, "!") {
			ip, name := SplitHostsFileLine(line)
			if net.ParseIP(ip) != nil && name != "" {
				// finish parsing the rest of the file as a hosts file«
				log.Printf("DEBUG: found hosts file filter: %s", line)
				buffered.Discard(pos)
				return parseHostsFileFilter(ctx, buffered)
			}

			rule, _ := adblock.ParseRule(line)
			if rule != nil {
				// finish parsing the rest of the file as an ABP filter file«
				log.Printf("DEBUG: found ad block plus filter: %s", line)
				buffered.Discard(pos)
				return parseABPFilter(ctx, buffered)
			}
		}

		pos += n
	}

	if err != nil && err != io.EOF {
		return nil, err
	}

	return nil, fmt.Errorf("unrecognized filter format")
}
