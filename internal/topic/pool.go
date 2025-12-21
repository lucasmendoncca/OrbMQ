package topic

import "sync"

var subsPool = sync.Pool{
	New: func() any {
		return make([]Subscriber, 0, 16)
	},
}
