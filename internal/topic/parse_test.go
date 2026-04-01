package topic

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMatchFilter_ShouldReturnTrue_WhenFilterAndTopicMatchExactly(t *testing.T) {
	result := MatchFilter("sensors/temperature", "sensors/temperature")

	assert.True(t, result)
}

func TestMatchFilter_ShouldMatchExpectedTopics_WhenUsingSupportedWildcards(t *testing.T) {
	testCases := []struct {
		name   string
		filter string
		topic  string
	}{
		{
			name:   "single level wildcard matches one segment",
			filter: "home/+/temperature",
			topic:  "home/kitchen/temperature",
		},
		{
			name:   "multi level wildcard matches remaining segments",
			filter: "home/#",
			topic:  "home/kitchen/temperature",
		},
		{
			name:   "single level wildcard matches empty segment",
			filter: "home/+/temperature",
			topic:  "home//temperature",
		},
		{
			name:   "multi level wildcard matches empty trailing segment",
			filter: "home/#",
			topic:  "home/",
		},
		{
			name:   "exact filter matches topic with leading slash",
			filter: "/system/status",
			topic:  "/system/status",
		},
	}

	require.NotEmpty(t, testCases)

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			result := MatchFilter(testCase.filter, testCase.topic)

			assert.True(t, result)
		})
	}
}

func TestMatchFilter_ShouldReturnFalse_WhenTopicDoesNotSatisfyFilter(t *testing.T) {
	testCases := []struct {
		name   string
		filter string
		topic  string
	}{
		{
			name:   "exact filter has different segment",
			filter: "home/living-room/temperature",
			topic:  "home/kitchen/temperature",
		},
		{
			name:   "topic has fewer levels than filter",
			filter: "home/room/temperature",
			topic:  "home/room",
		},
		{
			name:   "topic has more levels than exact filter",
			filter: "home/room",
			topic:  "home/room/temperature",
		},
		{
			name:   "single level wildcard does not match missing level at end",
			filter: "home/+/temperature",
			topic:  "home/kitchen",
		},
		{
			name:   "multi level wildcard after different prefix does not match",
			filter: "office/#",
			topic:  "home/kitchen/temperature",
		},
	}

	require.NotEmpty(t, testCases)

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			result := MatchFilter(testCase.filter, testCase.topic)

			assert.False(t, result)
		})
	}
}
