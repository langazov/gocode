package session

import "encoding/json"

// estimateTokens approximates token count as chars/4, matching the coarse
// heuristic used for compaction budgeting.
func estimateTokens(text string) int {
	return (len(text) + 3) / 4
}

func estimateJSON(value any) int {
	encoded, err := json.Marshal(value)
	if err != nil {
		return 0
	}
	return estimateTokens(string(encoded))
}
