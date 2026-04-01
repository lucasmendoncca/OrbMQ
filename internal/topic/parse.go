package topic

// MatchFilter takes a filter string and a topic string and returns true if the
// topic matches the filter, and false otherwise. The filter string can contain
// single-level or multi-level wildcards. For example, the filter string "foo/bar"
// matches the topic string "foo/bar", and the filter string "foo/+" matches the
// topic strings "foo/bar", "foo/baz", etc. The filter string "foo/#" matches
// the topic strings "foo/bar/baz", "foo/bar/foo", etc. If the filter string
// does not contain any wildcards, it must match the topic string exactly.
func MatchFilter(filter, topic string) bool {
	fLevels := splitFilter(filter)
	tLevels := splitTopic(topic)

	for i := range fLevels {
		if i >= len(tLevels) {
			return false
		}

		switch fLevels[i] {
		case "#":
			return true
		case "+":
			continue
		default:
			if fLevels[i] != tLevels[i] {
				return false
			}
		}
	}

	return len(fLevels) == len(tLevels)
}

// splitFilter takes a string and splits it into a slice of strings using the '/' character
// as a delimiter. It returns a slice of strings containing the split parts of the
// original string. For example, the string "foo/bar" would be split into the slice
// ["foo", "bar"]. If the original string does not contain any '/' characters, a
// slice containing a single string element is returned. For example, the string "foo"
// would be split into the slice ["foo"].
func splitFilter(s string) []string {
	var res []string
	start := 0

	for i := 0; i < len(s); i++ {
		if s[i] == '/' {
			res = append(res, s[start:i])
			start = i + 1
		}
	}

	res = append(res, s[start:])
	return res
}

// splitTopic takes a string and splits it into a slice of strings using the '/' character
// as a delimiter. It returns a slice of strings containing the split parts of the
// original string. For example, the string "foo/bar" would be split into the slice
// ["foo", "bar"]. If the original string does not contain any '/' characters, a
// slice containing a single string element is returned. For example, the string "foo"
// would be split into the slice ["foo"].
func splitTopic(topic string) []string {
	var res []string
	start := 0

	for i := 0; i < len(topic); i++ {
		if topic[i] == '/' {
			res = append(res, topic[start:i])
			start = i + 1
		}
	}

	res = append(res, topic[start:])
	return res
}
