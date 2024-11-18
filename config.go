package main

import (
	"fmt"
	"net/http"
	"os"

	"github.com/goccy/go-yaml"
)

type Config struct {
	// Telegram config
	Token  string `yaml:"token"`
	ChatID int64  `yaml:"chat_id"`

	// Jobs config
	Jobs              []Job `yaml:"jobs"`
	DefaultRetry      Retry `yaml:"default_retry"`
	DefaultJobTimeout int64 `yaml:"default_timeout_sec"`
	MaxActiveJobs     int64 `yaml:"max_active_jobs"`
}

type Job struct {
	Name           string `yaml:"name"`
	Schedule       string `yaml:"schedule"`
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

	if config.MaxActiveJobs == 0 {
		return nil, fmt.Errorf("set max_active_jobs")
	}

	if config.DefaultJobTimeout == 0 {
		return nil, fmt.Errorf("set default_job_timeout")
	}

	for i, job := range config.Jobs {
		if len(job.Method) == 0 {
			config.Jobs[i].Method = http.MethodPost
		}
	}

	return &config, nil
}
