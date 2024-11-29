package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"go.uber.org/zap"
	"gopkg.in/telebot.v3"

	"github.com/go-co-op/gocron/v2"
	"github.com/samber/lo"
)

type App struct {
	finished chan struct{}

	logger *zap.Logger
	bot    *telebot.Bot
	chat   *telebot.Chat

	scheduler gocron.Scheduler
	jobByName map[string]*Job

	defaultTimeout time.Duration
	defaultRetry   Retry
}

func NewApp(config *Config, logger *zap.Logger) (*App, error) {
	bot, err := telebot.NewBot(telebot.Settings{
		Token:  config.Token,
		Poller: &telebot.LongPoller{Timeout: 10 * time.Second},
	})
	if err != nil {
		return nil, fmt.Errorf("telebot.NewBot: %w", err)
	}

	chat, err := bot.ChatByID(config.ChatID)
	if err != nil {
		return nil, fmt.Errorf("bot.GetChatByID(%v): %w", config.ChatID, err)
	}

	logger.Info(
		"chat info",
		zap.Int64("id", chat.ID),
		zap.String("name", chat.Title),
	)

	scheduler, err := gocron.NewScheduler()
	if err != nil {
		return nil, fmt.Errorf("gocron.NewScheduler: %w", err)
	}

	app := &App{
		finished: make(chan struct{}),

		logger: logger,
		bot:    bot,
		chat:   chat,

		scheduler: scheduler,

		defaultTimeout: time.Duration(config.DefaultJobTimeout) * time.Second,
		defaultRetry:   config.DefaultRetry,
	}

	jobByName := map[string]*Job{}
	for _, job := range config.Jobs {
		job := lo.ToPtr(job)
		if _, ok := jobByName[job.Name]; ok {
			return nil, fmt.Errorf("duplicate job with name: %v", job.Name)
		}

		jobByName[job.Name] = job
		_, err := scheduler.NewJob(
			app.Definition(job),
			app.Task(job),
			gocron.WithName(job.Name),
		)
		if err != nil {
			return nil, fmt.Errorf("job %v error: %w", job.Name, err)
		}
	}

	app.jobByName = jobByName
	return app, nil
}

func (app *App) Run(ctx context.Context) error {
	err := app.bot.SetCommands([]telebot.Command{
		{
			Text:        "queue",
			Description: "show job queue",
		},
		{
			Text:        "jobs",
			Description: "list jobs",
		},
		{
			Text:        "run",
			Description: "run specific job",
		},
	})
	if err != nil {
		return fmt.Errorf("bot.SetCommands: %w", err)
	}

	app.bot.Use(func(next telebot.HandlerFunc) telebot.HandlerFunc {
		return func(c telebot.Context) error {
			chat := c.Chat()
			if chat == nil || chat.ID != app.chat.ID {
				return c.Reply("Not allowed in this chat")
			}
			return next(c)
		}
	})

	app.bot.Handle("/queue", func(c telebot.Context) error {
		jobs := app.scheduler.Jobs()
		sort.Slice(jobs, func(i, j int) bool {
			left, _ := jobs[i].NextRun()
			right, _ := jobs[j].NextRun()
			return left.Before(right)
		})

		if len(jobs) == 0 {
			return c.Reply("Job queue is empty")
		}

		message := strings.Builder{}
		for _, job := range jobs {
			t, _ := job.NextRun()
			message.WriteString(t.Format(time.RFC3339))
			message.WriteString(" - ")
			message.WriteString(job.Name())
			message.WriteByte('\n')
		}

		return c.Reply(message.String())
	})

	app.bot.Handle("/jobs", func(c telebot.Context) error {
		message := strings.Builder{}
		for name, job := range app.jobByName {
			message.WriteString(name)
			message.WriteString(" - ")
			message.WriteString(job.Schedule)
			message.WriteByte('\n')
		}

		return c.Reply(message.String())
	})

	app.bot.Handle("/run", func(c telebot.Context) error {
		name := c.Message().Payload
		job, ok := app.jobByName[name]
		if !ok {
			return c.Reply("job with such name not found")
		}

		err := app.runJob(ctx, job, false)
		if err != nil {
			return c.Reply(fmt.Sprintf("Job failed: %v", err))
		}

		return c.Reply("Job completed successfully")
	})

	go app.bot.Start()
	app.scheduler.Start()
	return nil
}

func (app *App) Shutdown(ctx context.Context) {
	app.bot.Stop()
	err := app.scheduler.Shutdown()
	if err != nil {
		app.logger.Error("scheduler.Shutdown", zap.Error(err))
	}
	app.finished <- struct{}{}
}

func (app *App) WaitShutdown() {
	<-app.finished
}

func (app *App) Definition(job *Job) gocron.JobDefinition {
	if job.Schedule == "manual" {
		return gocron.OneTimeJob(
			gocron.OneTimeJobStartDateTime(
				// basically never
				time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC),
			),
		)
	}

	return gocron.CronJob(job.Schedule, false)
}

func (app *App) Task(job *Job) gocron.Task {
	return gocron.NewTask(func() error {
		return app.runJob(context.Background(), job, true)
	})
}

func (app *App) timeoutFor(job *Job) time.Duration {
	if job.TimeoutSeconds > 0 {
		return time.Duration(job.TimeoutSeconds) * time.Second
	}

	return app.defaultTimeout
}

func (app *App) retryFor(job *Job) Retry {
	if job.Retry != nil {
		return *job.Retry
	}

	return app.defaultRetry
}

func (app *App) runJob(ctx context.Context, job *Job, alert bool) error {
	retry := app.retryFor(job)
	app.logger.Info(
		"running job",
		zap.String("name", job.Name),
		zap.Int64("retry_count", retry.Count),
		zap.Int64("retry_delay", retry.DelaySeconds),
	)

	err := app.withRetry(ctx, job, retry, func(ctx context.Context) error {
		var body io.Reader
		if len(job.Body) > 0 {
			body = strings.NewReader(job.Body)
		}

		req, err := http.NewRequestWithContext(ctx, job.Method, job.URL, body)
		if err != nil {
			return fmt.Errorf("http.NewRequest: %w", err)
		}

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return fmt.Errorf("http.Do: %w", err)
		}
		defer resp.Body.Close()

		switch resp.StatusCode {
		case http.StatusOK:
			return nil
		case http.StatusCreated:
			return nil
		}

		text, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("request failed wtih %v: %v", resp.StatusCode, string(text))
	})

	if err != nil {
		app.logger.Error("job failed", zap.String("name", job.Name), zap.Error(err))

		if alert {
			err = errors.Join(err, app.sendAlert(ctx, job, err))
		}
	} else {
		app.logger.Info("job completed", zap.String("name", job.Name))
	}

	return err
}

func (app *App) withRetry(ctx context.Context, job *Job, retry Retry, f func(ctx context.Context) error) error {
	timeout := app.timeoutFor(job)
	tryOnce := func() error {
		ctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		return f(ctx)
	}

	err := tryOnce()
	for err != nil && !errors.Is(err, context.Canceled) && retry.Count > 0 {
		retry.Count--
		duration := time.Duration(retry.DelaySeconds) * time.Second
		app.logger.Warn(
			"job will retry",
			zap.String("job", job.Name),
			zap.Error(err),
			zap.Duration("in", duration),
			zap.Int64("tries_remaining", retry.Count),
		)

		select {
		case <-time.After(duration):
			// do nothing
		case <-ctx.Done():
			return ctx.Err()
		}

		err = tryOnce()
	}
	return err
}

func (app *App) sendAlert(_ context.Context, job *Job, jobErr error) error {
	// TOD: cancellation?
	message := fmt.Sprintf("Job %v failed: %v", job.Name, jobErr)
	_, err := app.bot.Send(app.chat, message)
	return err
}
