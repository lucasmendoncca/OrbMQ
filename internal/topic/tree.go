package topic

type Tree struct {
	root *node
}

type node struct {
	children map[string]*node
	subs     map[string]Subscriber
}

type Subscriber interface {
	ID() string
	Enqueue([]byte) error
}

func NewTree() *Tree {
	return &Tree{
		root: &node{
			children: make(map[string]*node),
			subs:     make(map[string]Subscriber),
		},
	}
}

// Subscribe adds a client to the tree's subscription list.
// It will receive all messages published to topics that match the filter.
// The filter string is a topic name, or a topic name with a single-level or
// multi-level wildcard. For example: "foo/bar", "foo/+", "foo/#".
// A client can subscribe to multiple topics by calling Subscribe multiple times.
// If a client is already subscribed to a topic, calling Subscribe again will not
// cause the client to receive duplicate messages.
func (t *Tree) Subscribe(filter string, sub Subscriber) {
	levels := splitFilter(filter)

	cur := t.root
	for _, lvl := range levels {
		if cur.children[lvl] == nil {
			cur.children[lvl] = &node{
				children: make(map[string]*node),
				subs:     make(map[string]Subscriber),
			}
		}
		cur = cur.children[lvl]
	}

	cur.subs[sub.ID()] = sub
}

// Clone returns a deep copy of the tree. It is used by the
// Broker's Clone function to create a copy of the tree.
// The returned tree is a new, independent copy of the original tree.
func (t *Tree) Clone() *Tree {
	return &Tree{
		root: t.root.clone(),
	}
}

// Match returns a list of subscribers that are subscribed to the given topic.
// The topic string can contain single-level or multi-level wildcards.
// For example, "foo/bar", "foo/+", "foo/#".
// If no subscribers match the given topic, an empty list is returned.
func (t *Tree) Match(topic string) []Subscriber {
	subs := subsPool.Get().([]Subscriber)
	subs = subs[:0]

	t.match(t.root, topic, 0, &subs)
	return subs
}

// PutSubs returns a slice of Subscribers to the subsPool, to be reused
// by the Match function. It is used to avoid unnecessary memory allocations
// when the Match function is called with a large number of subscribers.
// If the capacity of the slice is greater than 1024, the slice is not returned to
// the pool, as it is unlikely to be reused.
func PutSubs(subs []Subscriber) {
	if cap(subs) > 1024 {
		return
	}
	subsPool.Put(subs[:0])
}

// Unsubscribe removes the subscriber with the given clientID from the node
// matching the specific filter. Other subscriptions for the same client are left intact.
func (t *Tree) Unsubscribe(filter string, clientID string) {
	levels := splitFilter(filter)

	cur := t.root
	for _, lvl := range levels {
		if cur.children[lvl] == nil {
			return
		}
		cur = cur.children[lvl]
	}

	delete(cur.subs, clientID)
}

// UnsubscribeAll removes all subscriptions for the given clientID from the tree.
// It is used by the Broker's UnsubscribeAll function to remove all subscriptions
// for a client when the client disconnects.
func (t *Tree) UnsubscribeAll(clientID string) {
	t.unsubscribeAll(t.root, clientID)
}

// clone returns a deep copy of the node. It is used by the Tree's
// Clone function to create a copy of the tree.
func (n *node) clone() *node {
	nn := &node{
		children: make(map[string]*node, len(n.children)),
		subs:     make(map[string]Subscriber, len(n.subs)),
	}

	for k, v := range n.children {
		nn.children[k] = v.clone()
	}

	for id, sub := range n.subs {
		nn.subs[id] = sub
	}

	return nn
}

// match is a helper function that returns a list of subscribers that are
// subscribed to the given topic. It is used by the Match function to
// recursively traverse the tree and find matching subscribers.
//
// The function takes a node, a slice of strings representing the topic
// levels, and a slice of Subscribers to store the matching subscribers.
func (t *Tree) match(n *node, topic string, idx int, out *[]Subscriber) {
	if n == nil {
		return
	}

	if idx >= len(topic) {
		for _, sub := range n.subs {
			*out = append(*out, sub)
		}
		if hash := n.children["#"]; hash != nil {
			for _, sub := range hash.subs {
				*out = append(*out, sub)
			}
		}
		return
	}

	next := idx
	for next < len(topic) && topic[next] != '/' {
		next++
	}

	level := topic[idx:next]

	var nextIdx int
	if next < len(topic) && topic[next] == '/' {
		nextIdx = next + 1
	} else {
		nextIdx = next
	}

	// exact match
	t.match(n.children[level], topic, nextIdx, out)

	// '+'
	t.match(n.children["+"], topic, nextIdx, out)

	// '#'
	if hash := n.children["#"]; hash != nil {
		for _, sub := range hash.subs {
			*out = append(*out, sub)
		}
	}
}

// unsubscribeAll removes all subscriptions for the given clientID from the tree.
// It is used by the Broker's UnsubscribeAll function to remove all subscriptions
// for a client when the client disconnects.
func (t *Tree) unsubscribeAll(n *node, clientID string) {
	if n == nil {
		return
	}

	delete(n.subs, clientID)

	for _, child := range n.children {
		t.unsubscribeAll(child, clientID)
	}
}
