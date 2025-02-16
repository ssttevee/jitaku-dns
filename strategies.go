package main

import (
	"fmt"
	"math/rand"
	"time"

	"github.com/miekg/dns"
)

type EForwardStrategy string

const (
	EForwardStrategyLinear     EForwardStrategy = "linear"
	EForwardStrategyRandom     EForwardStrategy = "random"
	EForwardStrategyRoundRobin EForwardStrategy = "round-robin"
	EForwardStrategyFirstBack  EForwardStrategy = "first-back"
)

func (s EForwardStrategy) Valid() bool {
	switch s {
	case EForwardStrategyLinear, EForwardStrategyRandom, EForwardStrategyRoundRobin, EForwardStrategyFirstBack:
		return true
	}

	return false
}

func (s EForwardStrategy) String() string {
	switch s {
	case EForwardStrategyLinear:
		return "Sequential"
	case EForwardStrategyRandom:
		return "Random"
	case EForwardStrategyRoundRobin:
		return "Round Robin"
	case EForwardStrategyFirstBack:
		return "First Back"
	}

	panic(fmt.Sprintf("Unknown strategy %q", string(s)))
}

func (s EForwardStrategy) New() IForwardStrategy {
	switch s {
	case EForwardStrategyLinear:
		return &LinearStrategy{}
	case EForwardStrategyRandom:
		return NewRandomStrategy(time.Now().UnixNano())
	case EForwardStrategyRoundRobin:
		return &RoundRobinStrategy{}
	case EForwardStrategyFirstBack:
		return &FirstBackStrategy{}
	}

	panic(fmt.Sprintf("Unknown strategy %q", string(s)))
}

var EForwardStrategyValues = []EForwardStrategy{
	EForwardStrategyLinear,
	EForwardStrategyRandom,
	EForwardStrategyRoundRobin,
	EForwardStrategyFirstBack,
}

type IForwardStrategy interface {
	Enum() EForwardStrategy
	String() string
	ForwardMessage(upstreams []Upstream, msg *dns.Msg) (*dns.Msg, int, error)
}

type LinearStrategy struct{}

func (s *LinearStrategy) Enum() EForwardStrategy {
	return EForwardStrategyLinear
}

func (s *LinearStrategy) String() string {
	return s.Enum().String()
}

func (s *LinearStrategy) ForwardMessage(upstreams []Upstream, msg *dns.Msg) (*dns.Msg, int, error) {
	var lastErr error
	var j int
	for i, u := range upstreams {
		if res, err := u.ForwardMessage(msg); err != nil {
			lastErr = err
			j = i
		} else {
			return res, i, nil
		}
	}

	return nil, j, lastErr
}

type RandomStrategy struct {
	rand *rand.Rand
}

func (s *RandomStrategy) Enum() EForwardStrategy {
	return EForwardStrategyRandom
}

func NewRandomStrategy(seed int64) *RandomStrategy {
	return &RandomStrategy{
		rand: rand.New(rand.NewSource(seed)),
	}
}

func (s *RandomStrategy) String() string {
	return s.Enum().String()
}

func (s *RandomStrategy) ForwardMessage(upstreams []Upstream, msg *dns.Msg) (*dns.Msg, int, error) {
	if len(upstreams) == 0 {
		return nil, 0, nil
	}

	n := s.rand.Intn(len(upstreams))
	res, err := upstreams[n].ForwardMessage(msg)
	return res, n, err
}

type RoundRobinStrategy struct {
	pos int
}

func (s *RoundRobinStrategy) Enum() EForwardStrategy {
	return EForwardStrategyRoundRobin
}

func (s *RoundRobinStrategy) String() string {
	return s.Enum().String()
}

func (s *RoundRobinStrategy) ForwardMessage(upstreams []Upstream, msg *dns.Msg) (*dns.Msg, int, error) {
	s.pos = (s.pos + 1) % len(upstreams)
	res, err := upstreams[s.pos].ForwardMessage(msg)
	return res, s.pos, err
}

type FirstBackStrategy struct{}

func (s *FirstBackStrategy) Enum() EForwardStrategy {
	return EForwardStrategyFirstBack
}

func (s *FirstBackStrategy) String() string {
	return s.Enum().String()
}

func (s *FirstBackStrategy) ForwardMessage(upstreams []Upstream, msg *dns.Msg) (*dns.Msg, int, error) {
	type Result struct {
		message *dns.Msg
		err     error
		index   int
	}

	resChan := make(chan *Result)
	for i := range upstreams {
		go func(i int) {
			u := upstreams[i]
			res, err := u.ForwardMessage(msg)
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
