package topic

import "sync"

var subsPool = sync.Pool{
	New: func() any {
		return make([]Subscription, 0, 16)
	},
}
