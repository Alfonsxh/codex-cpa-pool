// Package reasoningpolicy defines the operator's request ceiling, independently
// of upstream model capabilities and usage/billing multipliers.
package reasoningpolicy

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
)

const SettingKey = "gateway.max_reasoning_effort"

var levels = [...]string{"none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra"}

func Levels() []string { return append([]string(nil), levels[:]...) }

func rank(value string) int {
	for index, level := range levels {
		if value == level {
			return index
		}
	}
	return -1
}

func ValidLimit(value string) bool { return value == "" || rank(value) >= 0 }

func FromSettings(settings map[string]any) (string, error) {
	value, found := settings[SettingKey]
	if !found {
		return "", nil
	}
	limit, ok := value.(string)
	if !ok {
		return "", errors.New("invalid maximum reasoning effort")
	}
	if limit == "unlimited" {
		return "", nil
	}
	if limit == "" || !ValidLimit(limit) {
		return "", errors.New("invalid maximum reasoning effort")
	}
	return limit, nil
}

func clamp(value, limit string) string {
	if limit != "" && rank(strings.ToLower(strings.TrimSpace(value))) > rank(limit) {
		return limit
	}
	return value
}

// Rewrite touches only explicit effort selectors. Unknown/automatic/omitted
// values and lower levels remain unchanged; a no-op preserves the original bytes.
// RawMessage preserves numbers, tool schemas, images and encrypted reasoning data.
func Rewrite(body []byte, limit string) ([]byte, error) {
	if limit == "" {
		return body, nil
	}
	if !ValidLimit(limit) {
		return nil, errors.New("invalid maximum reasoning effort")
	}
	object, err := decodeObject(body)
	if err != nil {
		return nil, err
	}
	changed := clampField(object, "reasoning_effort", limit)
	if raw := object["reasoning"]; len(raw) != 0 && bytes.HasPrefix(bytes.TrimSpace(raw), []byte("{")) {
		reasoning, err := decodeObject(raw)
		if err != nil {
			return nil, err
		}
		if clampField(reasoning, "effort", limit) {
			object["reasoning"], _ = json.Marshal(reasoning)
			changed = true
		}
	}
	// CLIProxyAPI also accepts an explicit named effort in the model suffix.
	var model string
	if json.Unmarshal(object["model"], &model) == nil && strings.HasSuffix(model, ")") {
		if start := strings.LastIndexByte(model, '('); start > 0 {
			effort := model[start+1 : len(model)-1]
			if mapped := clamp(effort, limit); mapped != effort {
				object["model"], _ = json.Marshal(model[:start+1] + mapped + ")")
				changed = true
			}
		}
	}
	if !changed {
		return body, nil
	}
	return json.Marshal(object)
}

func clampField(object map[string]json.RawMessage, field, limit string) bool {
	var value string
	if json.Unmarshal(object[field], &value) != nil {
		return false
	}
	mapped := clamp(value, limit)
	if mapped == value {
		return false
	}
	object[field], _ = json.Marshal(mapped)
	return true
}

// MessageType validates the envelope before deciding whether to rewrite it, so
// duplicate type fields cannot select different events in different JSON parsers.
func MessageType(body []byte) (string, error) {
	object, err := decodeObject(body)
	if err != nil {
		return "", err
	}
	var kind string
	if err := json.Unmarshal(object["type"], &kind); err != nil {
		return "", err
	}
	return kind, nil
}

func decodeObject(raw []byte) (map[string]json.RawMessage, error) {
	if !json.Valid(raw) {
		return nil, errors.New("request must be a valid JSON object")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	first, err := decoder.Token()
	if err != nil || first != json.Delim('{') {
		return nil, errors.New("request must be a JSON object")
	}
	object := make(map[string]json.RawMessage)
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return nil, errors.New("invalid request object")
		}
		name := key.(string)
		if _, duplicate := object[name]; duplicate {
			return nil, errors.New("duplicate request field")
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, errors.New("invalid request object")
		}
		object[name] = value
	}
	return object, nil
}
