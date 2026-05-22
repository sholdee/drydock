package appset

import (
	"bytes"
	"encoding/json"
	"fmt"
	"text/template"

	"github.com/Masterminds/sprig/v3"
	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func renderApplicationTemplate(appset argoappv1.ApplicationSet, params map[string]any) (argoappv1.Application, error) {
	meta := appset.Spec.Template.ApplicationSetTemplateMeta
	templateApp, err := cloneTemplateApp(argoappv1.Application{
		ObjectMeta: metav1.ObjectMeta{
			Name:        meta.Name,
			Namespace:   meta.Namespace,
			Labels:      meta.Labels,
			Annotations: meta.Annotations,
			Finalizers:  meta.Finalizers,
		},
		Spec: appset.Spec.Template.Spec,
	})
	if err != nil {
		return argoappv1.Application{}, err
	}

	data, err := json.Marshal(templateApp)
	if err != nil {
		return argoappv1.Application{}, err
	}

	var raw any
	if err := json.Unmarshal(data, &raw); err != nil {
		return argoappv1.Application{}, err
	}
	renderedRaw, err := renderTemplateValue(raw, params, appset.Spec.GoTemplateOptions)
	if err != nil {
		return argoappv1.Application{}, err
	}

	rendered, err := json.Marshal(renderedRaw)
	if err != nil {
		return argoappv1.Application{}, err
	}
	var out argoappv1.Application
	if err := json.Unmarshal(rendered, &out); err != nil {
		return argoappv1.Application{}, fmt.Errorf("decode rendered Application: %w", err)
	}
	return out, nil
}

func renderTemplateValue(value any, params map[string]any, options []string) (any, error) {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			rendered, err := renderTemplateValue(child, params, options)
			if err != nil {
				return nil, err
			}
			typed[key] = rendered
		}
		return typed, nil
	case []any:
		for i, child := range typed {
			rendered, err := renderTemplateValue(child, params, options)
			if err != nil {
				return nil, err
			}
			typed[i] = rendered
		}
		return typed, nil
	case string:
		return renderString(typed, params, options)
	default:
		return value, nil
	}
}

func renderString(input string, params map[string]any, options []string) (string, error) {
	tmpl := template.New("appset").Funcs(sprig.TxtFuncMap())
	for _, option := range options {
		tmpl = tmpl.Option(option)
	}
	parsed, err := tmpl.Parse(input)
	if err != nil {
		return "", err
	}
	var out bytes.Buffer
	if err := parsed.Execute(&out, params); err != nil {
		return "", err
	}
	return out.String(), nil
}
