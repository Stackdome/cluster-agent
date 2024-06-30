package interpolation

import (
	"regexp"
	"strings"
)

// HasInterpolation checks if the input string has interpolation.
// Interpolation is defined as a string that starts with {{ and ends with }} and a .Attribute.
// Example: {{db.ExternalAddress}}
// db = The name of the resource referenced.
// ExternalAddress = The attribute of the resource referenced.
func HasInterpolation(input string) bool {
	pattern := `\{\{(\w+):(\d+)\}\}`
	regex := regexp.MustCompile(pattern)
	return regex.MatchString(input)
}

func findMatches(input string) []string {
	pattern := `\{\{(\w+):(\d+)\}\}`
	regex := regexp.MustCompile(pattern)
	return regex.FindAllString(input, -1)
}

func findValue(resource string, port string, interpolatedValuesFn func(string, string) string) string {
	return interpolatedValuesFn(resource, port)
}

func InterpolateString(input string, interpolatedValuesFn func(string, string) string) string {
	matches := findMatches(input)
	output := input
	for _, match := range matches {
		// Remove the {{ and }} from the match.
		value := match[2 : len(match)-2]
		resource, port := strings.Split(value, ":")[0], strings.Split(value, ":")[1]
		resolvedValue := findValue(resource, port, interpolatedValuesFn)
		output = regexp.MustCompile(match).ReplaceAllString(output, resolvedValue)
	}
	return output
}
