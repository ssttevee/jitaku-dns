package main

type PubSub[T any] struct {
	subs map[chan T]struct{}
}

func NewPubSub[T any](ch <-chan T) *PubSub[T] {
	subs := make(map[chan T]struct{})
	go func() {
		for v := range ch {
			for c := range subs {
				select {
				case c <- v:
				default:
					delete(subs, c)
				}
			}
		}
	}()

	return &PubSub[T]{
		subs: subs,
	}
}

func (p *PubSub[T]) Subscribe() <-chan T {
	ch := make(chan T, 1)
	p.subs[ch] = struct{}{}
	return ch
}
