package topic

type Tree struct {
	root *node
}

type node struct {
	children map[string]*node
	subs     map[string]Subscription
}

type Subscriber interface {
	ID() string
	Enqueue([]byte) error
}

type Subscription struct {
	ClientID   string
	Subscriber Subscriber
	QoS        byte
}

func NewTree() *Tree {
	return &Tree{
		root: &node{
			children: make(map[string]*node),
			subs:     make(map[string]Subscription),
		},
	}
}

// Subscribe adds a client subscription to the tree.
func (t *Tree) Subscribe(filter string, sub Subscription) {
	levels := splitFilter(filter)

	cur := t.root
	for _, lvl := range levels {
		if cur.children[lvl] == nil {
			cur.children[lvl] = &node{
				children: make(map[string]*node),
				subs:     make(map[string]Subscription),
			}
		}
		cur = cur.children[lvl]
	}

	cur.subs[sub.ClientID] = sub
}

// Clone returns a deep copy of the tree.
func (t *Tree) Clone() *Tree {
	return &Tree{
		root: t.root.clone(),
	}
}

// Match returns unique subscriptions that match the given topic.
// If a client matches through multiple filters, the highest granted QoS wins.
func (t *Tree) Match(topic string) []Subscription {
	matched := make(map[string]Subscription)
	t.match(t.root, topic, 0, matched)

	subs := subsPool.Get().([]Subscription)
	subs = subs[:0]
	for _, sub := range matched {
		subs = append(subs, sub)
	}

	return subs
}

// PutSubs returns a slice of subscriptions to the pool.
func PutSubs(subs []Subscription) {
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
func (t *Tree) UnsubscribeAll(clientID string) {
	t.unsubscribeAll(t.root, clientID)
}

func (n *node) clone() *node {
	nn := &node{
		children: make(map[string]*node, len(n.children)),
		subs:     make(map[string]Subscription, len(n.subs)),
	}

	for k, v := range n.children {
		nn.children[k] = v.clone()
	}

	for id, sub := range n.subs {
		nn.subs[id] = sub
	}

	return nn
}

func (t *Tree) match(n *node, topic string, idx int, out map[string]Subscription) {
	if n == nil {
		return
	}

	if idx >= len(topic) {
		for _, sub := range n.subs {
			t.addMatch(out, sub)
		}
		if hash := n.children["#"]; hash != nil {
			for _, sub := range hash.subs {
				t.addMatch(out, sub)
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

	t.match(n.children[level], topic, nextIdx, out)
	t.match(n.children["+"], topic, nextIdx, out)

	if hash := n.children["#"]; hash != nil {
		for _, sub := range hash.subs {
			t.addMatch(out, sub)
		}
	}
}

func (t *Tree) addMatch(out map[string]Subscription, sub Subscription) {
	existing, ok := out[sub.ClientID]
	if !ok || sub.QoS > existing.QoS {
		out[sub.ClientID] = sub
	}
}

func (t *Tree) unsubscribeAll(n *node, clientID string) {
	if n == nil {
		return
	}

	delete(n.subs, clientID)

	for _, child := range n.children {
		t.unsubscribeAll(child, clientID)
	}
}
