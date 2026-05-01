package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const defaultValuesURLTimeout = 5 * time.Second

type AllowedValue struct {
	Name  string
	Value string
}

func substituteURL(template, identity, groups, task string) string {
	r := strings.NewReplacer(
		"${identity}", url.QueryEscape(identity),
		"${groups}", url.QueryEscape(groups),
		"${task}", url.QueryEscape(task),
	)
	return r.Replace(template)
}

func fetchAllowedValues(ctx context.Context, rawURL string) ([]AllowedValue, error) {
	return fetchAllowedValuesWithTimeout(ctx, rawURL, defaultValuesURLTimeout)
}

func fetchAllowedValuesWithTimeout(ctx context.Context, rawURL string, timeout time.Duration) ([]AllowedValue, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("timeout fetching values from %s", rawURL)
		}
		return nil, fmt.Errorf("fetching values: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("values URL returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1MB limit
	if err != nil {
		return nil, fmt.Errorf("reading values response: %w", err)
	}

	return parseValuesResponse(body)
}

func parseValuesResponse(body []byte) ([]AllowedValue, error) {
	// Try name-value array: [{"name": "...", "value": "..."}]
	var nvList []struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	}
	if err := json.Unmarshal(body, &nvList); err == nil && len(nvList) > 0 && nvList[0].Value != "" {
		result := make([]AllowedValue, len(nvList))
		for i, nv := range nvList {
			result[i] = AllowedValue{Name: nv.Name, Value: nv.Value}
		}
		return result, nil
	}

	// Try simple string array: ["value1", "value2"]
	var strList []string
	if err := json.Unmarshal(body, &strList); err == nil {
		result := make([]AllowedValue, len(strList))
		for i, s := range strList {
			result[i] = AllowedValue{Name: s, Value: s}
		}
		return result, nil
	}

	// Try key-value object: {"Label": "value"}
	var kvMap map[string]string
	if err := json.Unmarshal(body, &kvMap); err == nil {
		result := make([]AllowedValue, 0, len(kvMap))
		for name, value := range kvMap {
			result = append(result, AllowedValue{Name: name, Value: value})
		}
		return result, nil
	}

	return nil, fmt.Errorf("invalid values response format")
}

func isValueAllowed(submitted string, allowed []AllowedValue) bool {
	for _, av := range allowed {
		if av.Value == submitted {
			return true
		}
	}
	return false
}
