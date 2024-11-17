package main

import (
	"fmt"
	"net/http"
	"os"

	"github.com/goccy/go-yaml"
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
	Token  string `yaml:"token"`
	ChatID int64  `yaml:"chat_id"`

	// Tasks config
	Tasks              []Task `yaml:"tasks"`
	DefaultRetry       Retry  `yaml:"default_retry"`
	DefaultTaskTimeout int64  `yaml:"default_timeout_sec"`
	MaxActiveTasks     int64  `yaml:"max_active_tasks"`
}

type Task struct {
	ID             string `yaml:"id"`
	When           string `yaml:"when"`
	Method         string `yaml:"method"` // POST by default
	URL            string `yaml:"url"`
	Body           string `yaml:"body"` // Empty by default
	Retry          *Retry `yaml:"retry"`
	TimeoutSeconds int64  `yaml:"timeout_sec"`
}

type Retry struct {
	// How many times to retry
	Count int64 `yaml:"count"`
	// Delay between retries
	DelaySeconds int64 `yaml:"delay_sec"`
}

func LoadConfig(path string) (*Config, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("os.ReadFile: %w", err)
	}

	var config Config
	err = yaml.Unmarshal(content, &config)
	if err != nil {
		return nil, fmt.Errorf("yaml.Unmarshal: %w", err)
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
