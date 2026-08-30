// Package session ports the V2 session core primitives from
// packages/core/src/session: durable prompt admission and inbox projection.
package session

type Delivery string

const (
	DeliverySteer Delivery = "steer"
	DeliveryQueue Delivery = "queue"
)

type Source struct {
	Start int    `json:"start"`
	End   int    `json:"end"`
	Text  string `json:"text"`
}

type FileAttachment struct {
	URI         string  `json:"uri"`
	Mime        string  `json:"mime"`
	Name        string  `json:"name,omitempty"`
	Description string  `json:"description,omitempty"`
	Source      *Source `json:"source,omitempty"`
}

type AgentAttachment struct {
	Name   string  `json:"name"`
	Source *Source `json:"source,omitempty"`
}

type Prompt struct {
	Text   string            `json:"text"`
	Files  []FileAttachment  `json:"files,omitempty"`
	Agents []AgentAttachment `json:"agents,omitempty"`
}
