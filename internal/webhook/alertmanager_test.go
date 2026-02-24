package webhook

import (
	"encoding/json"
	"testing"
)

func TestResolveTaskName(t *testing.T) {
	tests := []struct {
		name         string
		alert        Alert
		commonLabels map[string]string
		want         string
	}{
		{
			name: "aqsh_task from alert labels",
			alert: Alert{
				Labels: map[string]string{"aqsh_task": "restart-pod", "alertname": "HighMemory"},
			},
			commonLabels: map[string]string{"aqsh_task": "common-task"},
			want:         "restart-pod",
		},
		{
			name: "fallback to commonLabels aqsh_task",
			alert: Alert{
				Labels: map[string]string{"alertname": "HighMemory"},
			},
			commonLabels: map[string]string{"aqsh_task": "common-task"},
			want:         "common-task",
		},
		{
			name: "fallback to alertname",
			alert: Alert{
				Labels: map[string]string{"alertname": "HighMemory"},
			},
			commonLabels: map[string]string{},
			want:         "HighMemory",
		},
		{
			name: "nil commonLabels falls back to alertname",
			alert: Alert{
				Labels: map[string]string{"alertname": "DiskFull"},
			},
			commonLabels: nil,
			want:         "DiskFull",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveTaskName(tt.alert, tt.commonLabels)
			if got != tt.want {
				t.Errorf("ResolveTaskName() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestAlertToEnv(t *testing.T) {
	alert := Alert{
		Status: "firing",
		Labels: map[string]string{
			"alertname": "HighMemory",
			"instance":  "web-1",
			"severity":  "critical",
			"app-name":  "frontend",
		},
		Annotations: map[string]string{
			"summary":     "Memory usage is high",
			"description": "Instance web-1 memory > 90%",
		},
		StartsAt:     "2024-01-01T00:00:00Z",
		EndsAt:       "0001-01-01T00:00:00Z",
		GeneratorURL: "http://prometheus:9090/graph",
		Fingerprint:  "abc123",
	}

	wh := AlertmanagerWebhook{
		ExternalURL: "http://alertmanager:9093",
		GroupKey:    "{}:{alertname=\"HighMemory\"}",
	}

	env := AlertToEnv(alert, wh)

	// Check fixed fields
	checks := map[string]string{
		"ALERT_STATUS":              "firing",
		"ALERT_NAME":                "HighMemory",
		"ALERT_INSTANCE":            "web-1",
		"ALERT_SEVERITY":            "critical",
		"ALERT_FINGERPRINT":         "abc123",
		"ALERT_STARTS_AT":           "2024-01-01T00:00:00Z",
		"ALERT_ENDS_AT":             "0001-01-01T00:00:00Z",
		"ALERT_GENERATOR_URL":       "http://prometheus:9090/graph",
		"ALERTMANAGER_EXTERNAL_URL": "http://alertmanager:9093",
		"ALERT_GROUP_KEY":           "{}:{alertname=\"HighMemory\"}",
	}

	for key, want := range checks {
		if got := env[key]; got != want {
			t.Errorf("env[%q] = %q, want %q", key, got, want)
		}
	}

	// Check JSON fields
	var labels map[string]string
	if err := json.Unmarshal([]byte(env["ALERT_LABELS_JSON"]), &labels); err != nil {
		t.Fatalf("ALERT_LABELS_JSON unmarshal error: %v", err)
	}
	if labels["alertname"] != "HighMemory" {
		t.Errorf("ALERT_LABELS_JSON alertname = %q, want %q", labels["alertname"], "HighMemory")
	}

	var annotations map[string]string
	if err := json.Unmarshal([]byte(env["ALERT_ANNOTATIONS_JSON"]), &annotations); err != nil {
		t.Fatalf("ALERT_ANNOTATIONS_JSON unmarshal error: %v", err)
	}
	if annotations["summary"] != "Memory usage is high" {
		t.Errorf("ALERT_ANNOTATIONS_JSON summary = %q, want %q", annotations["summary"], "Memory usage is high")
	}

	// Check expanded labels (ALERT_LABEL_<KEY>)
	if got := env["ALERT_LABEL_ALERTNAME"]; got != "HighMemory" {
		t.Errorf("env[ALERT_LABEL_ALERTNAME] = %q, want %q", got, "HighMemory")
	}
	if got := env["ALERT_LABEL_APP_NAME"]; got != "frontend" {
		t.Errorf("env[ALERT_LABEL_APP_NAME] = %q, want %q (hyphen should become underscore)", got, "frontend")
	}

	// Check expanded annotations (ALERT_ANNOTATION_<KEY>)
	if got := env["ALERT_ANNOTATION_SUMMARY"]; got != "Memory usage is high" {
		t.Errorf("env[ALERT_ANNOTATION_SUMMARY] = %q, want %q", got, "Memory usage is high")
	}
}

func TestSanitizeEnvKey(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"alertname", "ALERTNAME"},
		{"app-name", "APP_NAME"},
		{"my.label.key", "MYLABELKEY"},
		{"ALREADY_UPPER", "ALREADY_UPPER"},
		{"with spaces", "WITHSPACES"},
		{"special!@#chars", "SPECIALCHARS"},
		{"under_score", "UNDER_SCORE"},
		{"123numeric", "123NUMERIC"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := sanitizeEnvKey(tt.input)
			if got != tt.want {
				t.Errorf("sanitizeEnvKey(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
