package webhook

import (
	"encoding/json"
	"regexp"
	"strings"
)

// AlertmanagerWebhook represents the payload sent by Alertmanager webhook receiver.
type AlertmanagerWebhook struct {
	Version      string            `json:"version"`
	GroupKey     string            `json:"groupKey"`
	Status       string            `json:"status"`
	Receiver     string            `json:"receiver"`
	ExternalURL  string            `json:"externalURL"`
	CommonLabels map[string]string `json:"commonLabels"`
	Alerts       []Alert           `json:"alerts"`
}

// Alert represents a single alert in the Alertmanager webhook payload.
type Alert struct {
	Status       string            `json:"status"`
	Labels       map[string]string `json:"labels"`
	Annotations  map[string]string `json:"annotations"`
	StartsAt     string            `json:"startsAt"`
	EndsAt       string            `json:"endsAt"`
	GeneratorURL string            `json:"generatorURL"`
	Fingerprint  string            `json:"fingerprint"`
}

// ResolveTaskName determines the aqsh task name for an alert.
// Lookup order: alert.Labels["aqsh_task"] -> commonLabels["aqsh_task"] -> alert.Labels["alertname"]
func ResolveTaskName(alert Alert, commonLabels map[string]string) string {
	if t := alert.Labels["aqsh_task"]; t != "" {
		return t
	}
	if t := commonLabels["aqsh_task"]; t != "" {
		return t
	}
	return alert.Labels["alertname"]
}

var envKeyRegexp = regexp.MustCompile(`[^A-Z0-9_]`)

// sanitizeEnvKey converts a string to a valid env var key component.
// Only [A-Z0-9_] are allowed.
func sanitizeEnvKey(key string) string {
	return envKeyRegexp.ReplaceAllString(strings.ToUpper(strings.ReplaceAll(key, "-", "_")), "")
}

// AlertToEnv converts an alert and its parent webhook into a map of environment variables.
func AlertToEnv(alert Alert, wh AlertmanagerWebhook) map[string]string {
	env := map[string]string{
		"ALERT_STATUS":             alert.Status,
		"ALERT_NAME":               alert.Labels["alertname"],
		"ALERT_INSTANCE":           alert.Labels["instance"],
		"ALERT_SEVERITY":           alert.Labels["severity"],
		"ALERT_FINGERPRINT":        alert.Fingerprint,
		"ALERT_STARTS_AT":          alert.StartsAt,
		"ALERT_ENDS_AT":            alert.EndsAt,
		"ALERT_GENERATOR_URL":      alert.GeneratorURL,
		"ALERTMANAGER_EXTERNAL_URL": wh.ExternalURL,
		"ALERT_GROUP_KEY":          wh.GroupKey,
	}

	// Serialize labels and annotations as JSON
	if labelsJSON, err := json.Marshal(alert.Labels); err == nil {
		env["ALERT_LABELS_JSON"] = string(labelsJSON)
	}
	if annotationsJSON, err := json.Marshal(alert.Annotations); err == nil {
		env["ALERT_ANNOTATIONS_JSON"] = string(annotationsJSON)
	}

	// Expand individual labels as ALERT_LABEL_<KEY>
	for k, v := range alert.Labels {
		env["ALERT_LABEL_"+sanitizeEnvKey(k)] = v
	}

	// Expand individual annotations as ALERT_ANNOTATION_<KEY>
	for k, v := range alert.Annotations {
		env["ALERT_ANNOTATION_"+sanitizeEnvKey(k)] = v
	}

	return env
}
