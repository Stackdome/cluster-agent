package interpolation

import (
	"bytes"
	"fmt"
	"strings"
	"text/template"
)

type Interpolator struct {
	context *InterpolationContext
	funcMap template.FuncMap
}

type TemplateError struct {
	Message  string
	Original error
}

func (e *TemplateError) Error() string {
	return e.Message
}

func NewInterpolator(ctx *InterpolationContext) *Interpolator {
	funcMap := template.FuncMap{}

	for resourceName, resource := range ctx.Resources {
		resourceNameUpper := strings.ToUpper(strings.ReplaceAll(resourceName, "-", "_"))

		// 1. Internal service endpoint
		internalFuncName := fmt.Sprintf("STACKDOME_%s_INTERNAL", resourceNameUpper)
		funcMap[internalFuncName] = func() (string, error) {
			if resource.Status.InternalService == nil {
				return "", fmt.Errorf("resource '%s' has no internal service", resourceName)
			}
			return *resource.Status.InternalService, nil
		}

		// 2. Default public URL (if only one port exists)
		if len(resource.Status.PublicIngresses) == 1 {
			publicFuncName := fmt.Sprintf("STACKDOME_%s_PUBLIC", resourceNameUpper)
			ingress := resource.Status.PublicIngresses[0]
			funcMap[publicFuncName] = func() (string, error) {
				if !ingress.ExposedToPublic {
					return "", fmt.Errorf("resource '%s' has no public ingress", resourceName)
				}
				return ingress.URL, nil
			}
		}

		// 3. Port-specific public URLs
		for _, ingress := range resource.Status.PublicIngresses {
			portSpecificFuncName := fmt.Sprintf("STACKDOME_%s_PUBLIC_%d",
				resourceNameUpper, ingress.TargetPort)

			ingressURL := ingress.URL
			isPublic := ingress.ExposedToPublic
			port := ingress.TargetPort

			funcMap[portSpecificFuncName] = func() (string, error) {
				if !isPublic {
					return "", fmt.Errorf("resource '%s' has no public ingress for port %d", resourceName, port)
				}
				return ingressURL, nil
			}
		}
	}

	return &Interpolator{
		context: ctx,
		funcMap: funcMap,
	}
}

func (i *Interpolator) InterpolateString(templateString string) (string, error) {
	tmpl, err := template.New("interpolator").
		Funcs(i.funcMap).
		Option("missingkey=error").
		Parse(templateString)
	if err != nil {
		return "", i.wrapParseError(err)
	}

	var result bytes.Buffer
	if err := tmpl.Execute(&result, nil); err != nil {
		return "", i.wrapExecutionError(err)
	}

	return result.String(), nil
}

func (i *Interpolator) InterpolateEnvVars(envVars map[string]string) (map[string]string, error) {
	result := make(map[string]string, len(envVars))
	var errors []string

	for key, value := range envVars {
		if strings.Contains(value, "{{") {
			interpolated, err := i.InterpolateString(value)
			if err != nil {
				errors = append(errors, fmt.Sprintf("%s: %v", key, err))
				result[key] = value // Keep original value on error
			} else {
				result[key] = interpolated
			}
		} else {
			result[key] = value // No interpolation needed
		}
	}

	if len(errors) > 0 {
		return result, fmt.Errorf("interpolation errors: %s", strings.Join(errors, "; "))
	}
	return result, nil
}

// wrapParseError handles template parsing errors
func (i *Interpolator) wrapParseError(err error) error {
	errMsg := err.Error()
	// Handle syntax errors
	if strings.Contains(errMsg, "unclosed action") ||
		strings.Contains(errMsg, `unexpected "}" in operand`) ||
		strings.Contains(errMsg, `unexpected "{" in operand`) {
		return &TemplateError{
			Message:  "Invalid template: Make sure all '{{' have matching '}}' pairs",
			Original: err,
		}
	}

	if strings.Contains(errMsg, "bad character") {
		return &TemplateError{
			Message:  "Invalid template: Contains characters that aren't allowed in template expressions",
			Original: err,
		}
	}

	// Handle undefined functions (non-existent resources)
	if strings.Contains(errMsg, "function") && strings.Contains(errMsg, "not defined") {
		// Extract function name using string operations
		parts := strings.Split(errMsg, "\"")
		if len(parts) >= 3 {
			funcName := parts[1]
			if strings.HasPrefix(funcName, "STACKDOME_") {
				return &TemplateError{
					Message:  fmt.Sprintf("Resource reference '%s' is not available", funcName),
					Original: err,
				}
			}
		}

		return &TemplateError{
			Message:  "Unknown function in template",
			Original: err,
		}
	}

	// Generic parse error fallback
	return &TemplateError{
		Message:  "Invalid template syntax",
		Original: err,
	}
}

// wrapExecutionError handles template execution errors
func (i *Interpolator) wrapExecutionError(err error) error {
	errMsg := err.Error()

	if strings.Contains(errMsg, "resource") && strings.Contains(errMsg, "not found") {
		return &TemplateError{
			Message:  "The referenced resource doesn't exist",
			Original: err,
		}
	}

	if strings.Contains(errMsg, "has no internal service") {
		return &TemplateError{
			Message:  "The referenced resource doesn't have an internal service",
			Original: err,
		}
	}

	if strings.Contains(errMsg, "has no public ingress") {
		return &TemplateError{
			Message:  "The referenced resource doesn't have a public URL",
			Original: err,
		}
	}

	if strings.Contains(errMsg, "no ingress found") && strings.Contains(errMsg, "with port") {
		return &TemplateError{
			Message:  "The referenced resource doesn't have a public URL for the specified port",
			Original: err,
		}
	}

	return &TemplateError{
		Message:  "Error processing template",
		Original: err,
	}
}
