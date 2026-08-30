package event

import "encoding/json"

func encodeJSON(value map[string]any) ([]byte, error) {
	return json.Marshal(value)
}

func decodeJSON(text string) (map[string]any, error) {
	var out map[string]any
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		return nil, err
	}
	return out, nil
}
