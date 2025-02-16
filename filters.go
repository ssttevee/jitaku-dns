package main

import (
	"fmt"
	"log"
	"net/http"

	"github.com/miekg/dns"
	"github.com/patriciy/adblock/adblock"
)

type FilterUpstream struct {
	Filter Filter
}

func (f *FilterUpstream) String() string {
	return f.Filter.Name()
}

func (f *FilterUpstream) ForwardMessage(msg *dns.Msg) (*dns.Msg, error) {
	ok, err := f.Filter.ShouldBlock(msg)
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
	Name() string
	ShouldBlock(msg *dns.Msg) (bool, error)
}

type ABPFilter struct {
	url     string
	matcher *adblock.RuleMatcher
	count   int
}

func (f *ABPFilter) Name() string {
	return f.url
}

func (f *ABPFilter) RuleCount() int {
	return f.count
}

func (f *ABPFilter) ShouldBlock(msg *dns.Msg) (bool, error) {
	if msg.Opcode != dns.OpcodeQuery || len(msg.Question) < 1 {
		return false, nil
	}

	for _, q := range msg.Question {
		ok, mode, err := f.matcher.Match(&adblock.Request{
			URL: fmt.Sprintf("https://%s/", q.Name),
		})
		if err != nil {
			return false, err
		}
		if ok && mode == adblock.Included {
			return true, nil
		}
	}

	return false, nil
}

func fetchAndParseFilter(hc *http.Client, url string) (Filter, error) {
	res, err := hc.Get("https://adguardteam.github.io/HostlistsRegistry/assets/filter_1.txt")
	if err != nil {
		return nil, fmt.Errorf("failed to fetch adblock list: %v", err)
	}

	defer res.Body.Close()

	rules, err := adblock.ParseRules(res.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to parse adblock rules: %v", err)
	}

	log.Printf("INFO: parsed %d rules", len(rules))

	matcher := adblock.NewMatcher()
	for i, rule := range rules {
		if err := matcher.AddRule(rule, i); err != nil {
			return nil, fmt.Errorf("failed to add rule: %v", err)
		}
	}

	return &ABPFilter{
		url:     url,
		matcher: matcher,
	}, nil
}
