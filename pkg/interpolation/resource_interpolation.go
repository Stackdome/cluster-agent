package interpolation

import "regexp"

func HasInterpolation(input string) bool {
	pattern := `{{\w+(?:\.\w+)*}}`
	regex := regexp.MustCompile(pattern)
	return regex.MatchString(input)
}

func findMatches(input string) []string {
	pattern := `{{\w+(?:\.\w+)*}}`
	regex := regexp.MustCompile(pattern)
	return regex.FindAllString(input, -1)
}

func findValue(key string, interpolatedValuesFn func(string) string) string {
	return interpolatedValuesFn(key)
}

func InterpolateString(input string, interpolatedValuesFn func(string) string) string {
	matches := findMatches(input)
	output := input
	for _, match := range matches {
		key := match[2 : len(match)-2]
		value := findValue(key, interpolatedValuesFn)
		output = regexp.MustCompile(match).ReplaceAllString(output, value)
	}
	return output
}
