package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
)

type TaskPeriod = string

const (
	TaskPeriodManual = "manual"
	TaskPeriodMinute = "every minute"
	TaskPeriodHourly = "every hour"
	TaskPeriodDaily  = "every day"
)

type Config struct {
	// Telegram config
	Token  string `json:"token"`
	ChatID int64  `json:"chat_id"`

	// Tasks config
	Tasks              []Task `json:"tasks"`
	DefaultRetry       Retry  `json:"default_retry"`
	DefaultTaskTimeout int64  `json:"default_timeout_sec"`
	MaxActiveTasks     int64  `json:"max_active_tasks"`
}

type Task struct {
	ID             string `json:"id"`
	When           string `json:"when"`
	Method         string `json:"method"` // POST by default
	URL            string `json:"url"`
	Body           string `json:"body"` // Empty by default
	Retry          *Retry `json:"retry"`
	TimeoutSeconds int64  `json:"timeout_sec"`
}

type Retry struct {
	// How many times to retry
	Count int64 `json:"count"`
	// Delay between retries
	DelaySeconds int64 `json:"delay_sec"`
}

func LoadConfig(path string) (*Config, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("os.ReadFile: %w", err)
	}

	var config Config
	err = json.Unmarshal(content, &config)
	if err != nil {
		return nil, fmt.Errorf("json.Unmarshal: %w", err)
	}

	if config.MaxActiveTasks == 0 {
		return nil, fmt.Errorf("set max_active_tasks")
	}

	if config.DefaultTaskTimeout == 0 {
		return nil, fmt.Errorf("set default_task_timeout")
	}

	for i, task := range config.Tasks {
		if len(task.Method) == 0 {
			config.Tasks[i].Method = http.MethodPost
		}
	}

	return &config, nil
}
