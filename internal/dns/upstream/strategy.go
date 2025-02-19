package upstream

import (
	"context"
	"fmt"
	"math/rand"
	"time"

	"github.com/miekg/dns"
)

var DefaultStrategy = &LinearStrategy{}

type ForwardStrategyKind string

const (
	ForwardStrategyKindLinear     ForwardStrategyKind = "linear"
	ForwardStrategyKindRandom     ForwardStrategyKind = "random"
	ForwardStrategyKindRoundRobin ForwardStrategyKind = "round-robin"
	ForwardStrategyKindFirstBack  ForwardStrategyKind = "first-back"
)

var ForwardStrategyKinds = []ForwardStrategyKind{
	ForwardStrategyKindLinear,
	ForwardStrategyKindRandom,
	ForwardStrategyKindRoundRobin,
	ForwardStrategyKindFirstBack,
}

func (s ForwardStrategyKind) Valid() bool {
	switch s {
	case ForwardStrategyKindLinear, ForwardStrategyKindRandom, ForwardStrategyKindRoundRobin, ForwardStrategyKindFirstBack:
		return true
	}

	return false
}

func (s ForwardStrategyKind) String() string {
	switch s {
	case ForwardStrategyKindLinear:
		return "Sequential"
	case ForwardStrategyKindRandom:
		return "Random"
	case ForwardStrategyKindRoundRobin:
		return "Round Robin"
	case ForwardStrategyKindFirstBack:
		return "First Back"
	}

	panic(fmt.Sprintf("Unknown strategy %q", string(s)))
}

func (s ForwardStrategyKind) New() ForwardStrategy {
	switch s {
	case ForwardStrategyKindLinear:
		return &LinearStrategy{}
	case ForwardStrategyKindRandom:
		return NewRandomStrategy(time.Now().UnixNano())
	case ForwardStrategyKindRoundRobin:
		return &RoundRobinStrategy{}
	case ForwardStrategyKindFirstBack:
		return &FirstBackStrategy{}
	}

	panic(fmt.Sprintf("Unknown strategy %q", string(s)))
}

type ForwardStrategy interface {
	Enum() ForwardStrategyKind
	String() string
	ForwardMessage(ctx context.Context, upstreams []Upstream, msg *dns.Msg) (*dns.Msg, int, error)
}

type LinearStrategy struct{}

func (s *LinearStrategy) Enum() ForwardStrategyKind {
	return ForwardStrategyKindLinear
}

func (s *LinearStrategy) String() string {
	return s.Enum().String()
}

func (s *LinearStrategy) ForwardMessage(ctx context.Context, upstreams []Upstream, msg *dns.Msg) (*dns.Msg, int, error) {
	var lastErr error
	var j int
	for i, u := range upstreams {
		if res, err := u.ForwardMessage(ctx, msg); err != nil {
			lastErr = err
			j = i
		} else if res != nil {
			return res, i, nil
		}
	}

	return nil, j, lastErr
}

type RandomStrategy struct {
	rand *rand.Rand
}

func (s *RandomStrategy) Enum() ForwardStrategyKind {
	return ForwardStrategyKindRandom
}

func NewRandomStrategy(seed int64) *RandomStrategy {
	return &RandomStrategy{
		rand: rand.New(rand.NewSource(seed)),
	}
}

func (s *RandomStrategy) String() string {
	return s.Enum().String()
}

func (s *RandomStrategy) ForwardMessage(ctx context.Context, upstreams []Upstream, msg *dns.Msg) (*dns.Msg, int, error) {
	if len(upstreams) == 0 {
		return nil, 0, nil
	}

	n := s.rand.Intn(len(upstreams))
	res, err := upstreams[n].ForwardMessage(ctx, msg)
	return res, n, err
}

type RoundRobinStrategy struct {
	pos int
}

func (s *RoundRobinStrategy) Enum() ForwardStrategyKind {
	return ForwardStrategyKindRoundRobin
}

func (s *RoundRobinStrategy) String() string {
	return s.Enum().String()
}

func (s *RoundRobinStrategy) ForwardMessage(ctx context.Context, upstreams []Upstream, msg *dns.Msg) (*dns.Msg, int, error) {
	s.pos = (s.pos + 1) % len(upstreams)
	res, err := upstreams[s.pos].ForwardMessage(ctx, msg)
	return res, s.pos, err
}

type FirstBackStrategy struct{}

func (s *FirstBackStrategy) Enum() ForwardStrategyKind {
	return ForwardStrategyKindFirstBack
}

func (s *FirstBackStrategy) String() string {
	return s.Enum().String()
}

func (s *FirstBackStrategy) ForwardMessage(ctx context.Context, upstreams []Upstream, msg *dns.Msg) (*dns.Msg, int, error) {
	type Result struct {
		message *dns.Msg
		err     error
		index   int
	}

	resChan := make(chan *Result)
	for i := range upstreams {
		go func(i int) {
			u := upstreams[i]
			res, err := u.ForwardMessage(ctx, msg)
			if err != nil {
				resChan <- nil
			} else {
				resChan <- &Result{
					message: res,
					index:   i,
				}
			}
		}(i)
	}

	var err error
	var i int
	for range upstreams {
		select {
		case res := <-resChan:
			if res != nil {
				if res.message != nil {
					return res.message, res.index, nil
				} else if res.err != nil {
					err = res.err
					i = res.index
				}
			}
		}
	}

	return nil, i, err
}
