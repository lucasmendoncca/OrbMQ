package topic

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
