package controlplane

import "time"

var adapterSampleTime = time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)

type AdapterPayload struct {
	Queues   []AdapterQueue   `json:"queues"`
	Sessions []AdapterSession `json:"sessions"`
	Logs     []AdapterLog     `json:"logs"`
	Prompts  []AdapterPrompt  `json:"prompts"`
	Config   AdapterConfig    `json:"config"`
	Agents   []AdapterAgent   `json:"agents"`
	Skills   []AdapterSkill   `json:"skills"`
	Health   AdapterHealth    `json:"health"`
}

type AdapterQueue struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Status    string    `json:"status"`
	Depth     int       `json:"depth"`
	Capacity  int       `json:"capacity"`
	Workers   int       `json:"workers"`
	UpdatedAt time.Time `json:"updated_at"`
}

type AdapterSession struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Surface    string    `json:"surface"`
	Status     string    `json:"status"`
	WorkflowID string    `json:"workflow_id,omitempty"`
	StartedAt  time.Time `json:"started_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type AdapterLog struct {
	ID        string    `json:"id"`
	Level     string    `json:"level"`
	Source    string    `json:"source"`
	Message   string    `json:"message"`
	CreatedAt time.Time `json:"created_at"`
}

type AdapterPrompt struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Surface     string   `json:"surface"`
	Template    string   `json:"template"`
	Variables   []string `json:"variables"`
	Description string   `json:"description"`
}

type AdapterConfig struct {
	Environment string         `json:"environment"`
	Version     string         `json:"version"`
	BaseURL     string         `json:"base_url"`
	Features    []string       `json:"features"`
	Limits      map[string]int `json:"limits"`
}

type AdapterAgent struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Role         string   `json:"role"`
	Status       string   `json:"status"`
	Model        string   `json:"model"`
	Capabilities []string `json:"capabilities"`
}

type AdapterSkill struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Category    string `json:"category"`
	Status      string `json:"status"`
	Description string `json:"description"`
}

type AdapterHealth struct {
	Status    string           `json:"status"`
	Services  []AdapterService `json:"services"`
	CheckedAt time.Time        `json:"checked_at"`
}

type AdapterService struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Details string `json:"details,omitempty"`
}
