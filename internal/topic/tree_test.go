package topic

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stubSubscriber struct {
	id string
}

func (s *stubSubscriber) ID() string {
	return s.id
}

func (s *stubSubscriber) Enqueue([]byte) error {
	return nil
}

func TestNewTree_ShouldReturnEmptyTree_WhenCalled(t *testing.T) {
	tree := NewTree()

	require.NotNil(t, tree)
	assert.Empty(t, tree.Match("sensors/temperature"))
}

func TestTreeMatch_ShouldReturnSubscribedClient_WhenTopicMatchesExactFilter(t *testing.T) {
	tree := NewTree()
	sub := Subscription{
		ClientID:   "client-1",
		Subscriber: &stubSubscriber{id: "sub-1"},
		QoS:        1,
	}

	tree.Subscribe("sensors/temperature", sub)

	matches := tree.Match("sensors/temperature")
	require.Len(t, matches, 1)
	assert.Equal(t, sub, matches[0])
	PutSubs(matches)
}

func TestTreeMatch_ShouldReturnHighestQoSPerClient_WhenClientMatchesMultipleFilters(t *testing.T) {
	tree := NewTree()

	tree.Subscribe("building/+/temperature", Subscription{
		ClientID:   "client-1",
		Subscriber: &stubSubscriber{id: "sub-1"},
		QoS:        0,
	})
	tree.Subscribe("building/room-1/#", Subscription{
		ClientID:   "client-1",
		Subscriber: &stubSubscriber{id: "sub-1-high"},
		QoS:        2,
	})
	tree.Subscribe("building/room-1/temperature", Subscription{
		ClientID:   "client-2",
		Subscriber: &stubSubscriber{id: "sub-2"},
		QoS:        1,
	})

	matches := tree.Match("building/room-1/temperature")
	require.Len(t, matches, 2)

	matchesByClient := subscriptionsByClientID(matches)
	require.Contains(t, matchesByClient, "client-1")
	require.Contains(t, matchesByClient, "client-2")
	assert.Equal(t, byte(2), matchesByClient["client-1"].QoS)
	assert.Equal(t, "sub-1-high", matchesByClient["client-1"].Subscriber.ID())
	assert.Equal(t, byte(1), matchesByClient["client-2"].QoS)
	PutSubs(matches)
}

func TestTreeMatch_ShouldIncludeHashSubscription_WhenTopicEndsAtParentLevel(t *testing.T) {
	tree := NewTree()
	hashSub := Subscription{
		ClientID:   "client-1",
		Subscriber: &stubSubscriber{id: "sub-1"},
		QoS:        1,
	}

	tree.Subscribe("factory/#", hashSub)

	matches := tree.Match("factory")
	require.Len(t, matches, 1)
	assert.Equal(t, hashSub, matches[0])
	PutSubs(matches)
}

func TestTreeClone_ShouldRemainIndependent_WhenOriginalTreeChangesAfterClone(t *testing.T) {
	original := NewTree()
	original.Subscribe("devices/alpha", Subscription{
		ClientID:   "client-1",
		Subscriber: &stubSubscriber{id: "sub-1"},
		QoS:        1,
	})

	cloned := original.Clone()
	require.NotNil(t, cloned)

	original.Subscribe("devices/beta", Subscription{
		ClientID:   "client-2",
		Subscriber: &stubSubscriber{id: "sub-2"},
		QoS:        0,
	})
	original.Unsubscribe("devices/alpha", "client-1")

	clonedAlphaMatches := cloned.Match("devices/alpha")
	require.Len(t, clonedAlphaMatches, 1)
	assert.Equal(t, "client-1", clonedAlphaMatches[0].ClientID)
	PutSubs(clonedAlphaMatches)

	clonedBetaMatches := cloned.Match("devices/beta")
	assert.Empty(t, clonedBetaMatches)
	PutSubs(clonedBetaMatches)

	originalAlphaMatches := original.Match("devices/alpha")
	assert.Empty(t, originalAlphaMatches)
	PutSubs(originalAlphaMatches)
}

func TestTreeUnsubscribe_ShouldRemoveOnlySpecifiedFilterSubscription_WhenClientHasMultipleSubscriptions(t *testing.T) {
	tree := NewTree()
	sub := Subscription{
		ClientID:   "client-1",
		Subscriber: &stubSubscriber{id: "sub-1"},
		QoS:        1,
	}

	tree.Subscribe("alerts/critical", sub)
	tree.Subscribe("alerts/warning", sub)

	tree.Unsubscribe("alerts/critical", "client-1")

	criticalMatches := tree.Match("alerts/critical")
	assert.Empty(t, criticalMatches)
	PutSubs(criticalMatches)

	warningMatches := tree.Match("alerts/warning")
	require.Len(t, warningMatches, 1)
	assert.Equal(t, sub, warningMatches[0])
	PutSubs(warningMatches)
}

func TestTreeUnsubscribe_ShouldKeepTreeUnchanged_WhenFilterDoesNotExist(t *testing.T) {
	tree := NewTree()
	sub := Subscription{
		ClientID:   "client-1",
		Subscriber: &stubSubscriber{id: "sub-1"},
		QoS:        1,
	}

	tree.Subscribe("alerts/critical", sub)

	tree.Unsubscribe("alerts/missing", "client-1")

	matches := tree.Match("alerts/critical")
	require.Len(t, matches, 1)
	assert.Equal(t, sub, matches[0])
	PutSubs(matches)
}

func TestTreeUnsubscribeAll_ShouldRemoveClientFromAllMatchingFilters_WhenClientExistsInMultipleBranches(t *testing.T) {
	tree := NewTree()

	tree.Subscribe("factory/line-1/status", Subscription{
		ClientID:   "client-1",
		Subscriber: &stubSubscriber{id: "sub-1"},
		QoS:        1,
	})
	tree.Subscribe("factory/line-2/#", Subscription{
		ClientID:   "client-1",
		Subscriber: &stubSubscriber{id: "sub-1"},
		QoS:        2,
	})
	tree.Subscribe("factory/line-2/status", Subscription{
		ClientID:   "client-2",
		Subscriber: &stubSubscriber{id: "sub-2"},
		QoS:        0,
	})

	tree.UnsubscribeAll("client-1")

	lineOneMatches := tree.Match("factory/line-1/status")
	assert.Empty(t, lineOneMatches)
	PutSubs(lineOneMatches)

	lineTwoMatches := tree.Match("factory/line-2/status")
	require.Len(t, lineTwoMatches, 1)
	assert.Equal(t, "client-2", lineTwoMatches[0].ClientID)
	PutSubs(lineTwoMatches)
}

func TestTreeMatch_ShouldReturnEmptySlice_WhenTreeRootIsNil(t *testing.T) {
	tree := &Tree{}

	matches := tree.Match("factory/line-1/status")

	assert.Empty(t, matches)
	PutSubs(matches)
}

func TestTreeUnsubscribeAll_ShouldNotPanic_WhenTreeRootIsNil(t *testing.T) {
	tree := &Tree{}

	require.NotPanics(t, func() {
		tree.UnsubscribeAll("client-1")
	})
}

func TestPutSubs_ShouldIgnoreSlice_WhenCapacityIsGreaterThanThreshold(t *testing.T) {
	subs := make([]Subscription, 0, 1025)

	require.NotPanics(t, func() {
		PutSubs(subs)
	})
}

func subscriptionsByClientID(subs []Subscription) map[string]Subscription {
	result := make(map[string]Subscription, len(subs))

	for _, sub := range subs {
		result[sub.ClientID] = sub
	}

	return result
}
