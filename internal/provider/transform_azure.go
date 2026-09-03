package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
)

func init() {
	Register(azureTransform{byID{"azure"}})
	Register(azureCognitiveTransform{byID{"azure-cognitive-services"}})
}

// azureTransform ports AzurePlugin from
// packages/core/src/plugin/provider/azure.ts, plus the URL and header
// construction that TypeScript gets from @ai-sdk/azure itself
// (azure-openai-provider.ts in that package).
//
// Azure differs from stock OpenAI in three ways: the endpoint is derived from
// a resource name rather than being fixed, requests carry an api-version query
// parameter, and the key goes in an `api-key` header instead of a bearer
// token.
type azureTransform struct{ byID }

// defaultAPIVersion matches `options.apiVersion ?? 'v1'` in
// @ai-sdk/azure@4.0.58. It is the API surface version, not a date; a user who
// needs a dated preview version sets provider.azure.options.apiVersion.
const defaultAPIVersion = "v1"

func (azureTransform) Apply(_ context.Context, r *Resolved) error {
	resourceName := r.Option("resourceName")
	if resourceName == "" {
		resourceName = os.Getenv("AZURE_RESOURCE_NAME")
	}

	base := r.BaseURL
	if base == "" && resourceName != "" {
		base = fmt.Sprintf("https://%s.openai.azure.com/openai", resourceName)
	}
	if base == "" {
		// Same failure the TS plugin raises, with the same two remedies.
		return fmt.Errorf(
			"provider %q: AZURE_RESOURCE_NAME is missing — set it in the environment, or set provider.azure.options.resourceName or options.baseURL in gocode.json",
			r.ID)
	}
	r.BaseURL = base

	apiVersion := r.Option("apiVersion")
	if apiVersion == "" {
		apiVersion = defaultAPIVersion
	}
	// useDeploymentBasedUrls selects the older per-deployment routing that some
	// models still require, matching the option of the same name in the SDK.
	deploymentBased := strings.EqualFold(r.Option("useDeploymentBasedUrls"), "true")

	r.Options.Endpoint = func(base, model string) string {
		return azureURL(base, model, apiVersion, deploymentBased, "/chat/completions")
	}
	// Azure authenticates with api-key, so the bearer header must not be sent.
	// Setting Sign is what suppresses it; see llm.Options.Authenticate.
	key := r.APIKey
	r.Options.Sign = func(req *http.Request, _ []byte) error {
		req.Header.Set("api-key", key)
		return nil
	}
	return nil
}

// azureURL ports the url() closure in @ai-sdk/azure's azure-openai-provider.
func azureURL(base, model, apiVersion string, deploymentBased bool, path string) string {
	base = strings.TrimRight(base, "/")
	info := azureBaseURLInfo(base)

	var full string
	versioned := false
	switch {
	case deploymentBased:
		full = fmt.Sprintf("%s/deployments/%s%s", base, model, path)
		versioned = true
	case !info.isAzureOpenAI || info.isVersioned:
		// A custom gateway, or a base URL that already names its version,
		// owns its own routing: leave both alone.
		full = base + path
	default:
		full = base + "/v1" + path
		versioned = !info.isFoundryProject
	}
	if versioned {
		if parsed, err := url.Parse(full); err == nil {
			query := parsed.Query()
			query.Set("api-version", apiVersion)
			parsed.RawQuery = query.Encode()
			full = parsed.String()
		}
	}
	return full
}

type azureURLInfo struct {
	isAzureOpenAI    bool
	isFoundryProject bool
	isVersioned      bool
}

// azureBaseURLInfo ports getAzureOpenAIBaseURLInfo.
func azureBaseURLInfo(base string) azureURLInfo {
	parsed, err := url.Parse(base)
	if err != nil {
		return azureURLInfo{}
	}
	host := parsed.Hostname()
	isAzure := strings.HasSuffix(host, ".openai.azure.com") ||
		strings.HasSuffix(host, ".services.ai.azure.com") ||
		strings.HasSuffix(host, ".cognitiveservices.azure.com")
	path := strings.TrimRight(parsed.Path, "/")
	return azureURLInfo{
		isAzureOpenAI:    isAzure,
		isFoundryProject: strings.HasSuffix(host, ".services.ai.azure.com") && strings.HasPrefix(path, "/api/projects/"),
		isVersioned:      isAzure && strings.HasSuffix(strings.ToLower(path), "/openai/v1"),
	}
}

// azureCognitiveTransform ports AzureCognitiveServicesPlugin. Unlike azure
// proper it goes through the plain OpenAI-compatible path — only the base URL
// is derived — so it needs no endpoint or auth override.
type azureCognitiveTransform struct{ byID }

func (azureCognitiveTransform) Apply(_ context.Context, r *Resolved) error {
	if r.BaseURL != "" {
		return nil
	}
	resourceName := os.Getenv("AZURE_COGNITIVE_SERVICES_RESOURCE_NAME")
	if resourceName == "" {
		return nil
	}
	r.BaseURL = fmt.Sprintf("https://%s.cognitiveservices.azure.com/openai", resourceName)
	return nil
}

var (
	_ Transform = azureTransform{}
	_ Transform = azureCognitiveTransform{}
)
