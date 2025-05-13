package interpolation

import (
	"bytes"
	"fmt"
	"text/template"
)

type TemplateFunctions struct {
	context *InterpolationContext
}

// Template functions for interpolation
func (f *TemplateFunctions) URL(resourceName string, port ...int) (string, error) {
	resource, ok := f.context.Resources[resourceName]
	if !ok {
		return "", fmt.Errorf("resource '%s' not found", resourceName)
	}

	if len(resource.Status.PublicIngresses) == 0 {
		return "", fmt.Errorf("resource '%s' has no public ingresses", resourceName)
	}

	// If no port specified, return the first ingress URL
	if len(port) == 0 {
		switch {
		case len(resource.Status.PublicIngresses) > 1:
			return "", fmt.Errorf("multiple public ingresses found for resource '%s', specify a port",
				resourceName)
		case len(resource.Status.PublicIngresses) == 1 && !resource.Status.PublicIngresses[0].ExposedToPublic:
			return "", fmt.Errorf("resource '%s' has no public ingress", resourceName)
		default:
			return resource.Status.PublicIngresses[0].URL, nil
		}
	}

	targetPort := port[0]

	for _, ingress := range resource.Status.PublicIngresses {
		if ingress.TargetPort == targetPort {
			if !ingress.ExposedToPublic {
				return "", fmt.Errorf("resource '%s' has no public ingress for port %d", resourceName, targetPort)
			}
			return ingress.URL, nil
		}
	}

	return "", fmt.Errorf("no ingress found for resource '%s' with port %d",
		resourceName, targetPort)
}

func (f *TemplateFunctions) Internal(resourceName string) (string, error) {
	resource, ok := f.context.Resources[resourceName]
	if !ok {
		return "", fmt.Errorf("resource '%s' not found", resourceName)
	}

	if resource.Status.InternalService == nil {
		return "", fmt.Errorf("resource '%s' has no internal service", resourceName)
	}

	return *resource.Status.InternalService, nil
}

type Interpolator struct {
	context   *InterpolationContext
	funcMap   template.FuncMap
	functions *TemplateFunctions
}

func NewInterpolator(ctx *InterpolationContext) *Interpolator {
	functions := &TemplateFunctions{context: ctx}

	funcMap := template.FuncMap{
		"STACKDOME_PUBLIC_URL":        functions.URL,
		"STACKDOME_INTERNAL_ENDPOINT": functions.Internal,
	}

	return &Interpolator{
		context:   ctx,
		funcMap:   funcMap,
		functions: functions,
	}
}

func (i *Interpolator) InterpolateString(templateString string) (string, error) {
	tmpl, err := template.New("interpolator").
		Funcs(i.funcMap).
		Option("missingkey=error").
		Parse(templateString)
	if err != nil {
		return "", fmt.Errorf("template parse error: %w", err)
	}

	var result bytes.Buffer
	if err := tmpl.Execute(&result, i.context); err != nil {
		return "", fmt.Errorf("template execution error: %w", err)
	}

	return result.String(), nil
}
