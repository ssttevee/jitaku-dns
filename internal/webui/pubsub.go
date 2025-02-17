package webui

type pubsub[T any] struct {
	subs map[chan T]struct{}
}

func newPubSub[T any](ch <-chan T) *pubsub[T] {
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

	return &pubsub[T]{
		subs: subs,
	}
}

func (p *pubsub[T]) Subscribe() <-chan T {
	ch := make(chan T, 1)
	p.subs[ch] = struct{}{}
	return ch
}
