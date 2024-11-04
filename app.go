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

	"github.com/samber/lo"
	"go.uber.org/zap"
	"gopkg.in/telebot.v3"
)

type App struct {
	finished chan struct{}

	logger *zap.Logger
	bot    *telebot.Bot
	chat   *telebot.Chat

	tasksByID      map[string]*Task
	taskQueue      *TaskQueue
	taskTickets    chan struct{}
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

	tasksByID := map[string]*Task{}
	for _, task := range config.Tasks {
		if _, ok := tasksByID[task.ID]; ok {
			return nil, fmt.Errorf("duplicate task id %v", task.ID)
		}

		tasksByID[task.ID] = lo.ToPtr(task)
	}

	queue, err := NewTaskQueue(config.Tasks)
	if err != nil {
		return nil, fmt.Errorf("NewTaskQueue: %w", err)
	}

	tickets := make(chan struct{}, config.MaxActiveTasks)
	return &App{
		finished: make(chan struct{}),

		logger: logger,
		bot:    bot,
		chat:   chat,

		tasksByID:   tasksByID,
		taskQueue:   queue,
		taskTickets: tickets,

		defaultTimeout: time.Duration(config.DefaultTaskTimeout) * time.Second,
		defaultRetry:   config.DefaultRetry,
	}, nil
}

func (app *App) Run(ctx context.Context) error {
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
		tasks := app.taskQueue.List()
		sort.Slice(tasks, func(i, j int) bool {
			return tasks[i].At.Before(tasks[j].At)
		})

		message := strings.Builder{}
		for _, task := range tasks {
			message.WriteString(task.String())
			message.WriteByte('\n')
		}

		return c.Reply(message.String())
	})

	app.bot.Handle("/tasks", func(c telebot.Context) error {
		message := strings.Builder{}
		for _, task := range app.tasksByID {
			message.WriteString(fmt.Sprintf("%v (%v)", task.ID, task.When))
			message.WriteByte('\n')
		}

		return c.Reply(message.String())
	})

	app.bot.Handle("/run", func(c telebot.Context) error {
		ID := c.Message().Payload
		task, ok := app.tasksByID[ID]
		if !ok {
			return c.Reply("task with such ID not found")
		}

		err := app.runTask(ctx, task, false)
		if err != nil {
			return c.Reply(fmt.Sprintf("Task failed: %v", err))
		}

		return c.Reply("Task completed successfully")
	})

	app.bot.Handle("/help", func(c telebot.Context) error {
		return c.Reply(
			"/tasks - list all tasks\n" +
				"/run <task_id> - run specific task\n" +
				"/queue - show current task queue",
		)
	})

	go app.bot.Start()

	for {
		nextTask := app.taskQueue.Next()
		if nextTask == nil {
			app.logger.Info("No more tasks")
			break
		}

		duration := time.Until(nextTask.At.Add(time.Millisecond))
		app.logger.Info(
			"waiting for next task",
			zap.String("id", nextTask.Task.ID),
			zap.Time("until", nextTask.At),
		)

		// Wait untill task is active
		select {
		case <-time.After(duration):
			// do nothing
		case <-ctx.Done():
			return ctx.Err()
		}

		// Perform it asynchronously
		go app.runTask(ctx, nextTask.Task, true)
		// Get it back into the queue
		app.taskQueue.PushTask(app.taskQueue.PopNextTask())
	}

	return nil
}

func (app *App) Shutdown(ctx context.Context) {
	app.bot.Stop()
	app.finished <- struct{}{}
}

func (app *App) WaitShutdown() {
	<-app.finished
}

func (app *App) aquireTicket(ctx context.Context) error {
	select {
	case app.taskTickets <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (app *App) releaseTicket() {
	<-app.taskTickets
}

func (app *App) timeoutFor(task *Task) time.Duration {
	if task.TimeoutSeconds > 0 {
		return time.Duration(task.TimeoutSeconds) * time.Second
	}

	return app.defaultTimeout
}

func (app *App) retryFor(task *Task) Retry {
	if task.Retry != nil {
		return *task.Retry
	}

	return app.defaultRetry
}

func (app *App) runTask(ctx context.Context, task *Task, alert bool) error {
	app.logger.Info("starting task", zap.String("id", task.ID))
	err := app.aquireTicket(ctx)
	if err != nil {
		app.logger.Error("task failed to get a ticket", zap.String("id", task.ID), zap.Error(err))
		return fmt.Errorf("app.aquireTicket: %w", err)
	}
	defer app.releaseTicket()

	retry := app.retryFor(task)
	app.logger.Info(
		"running task",
		zap.String("id", task.ID),
		zap.Int64("retry_count", retry.Count),
		zap.Int64("retry_delay", retry.DelaySeconds),
	)
	err = app.withRetry(ctx, task, retry, func(ctx context.Context) error {
		var body io.Reader
		if len(task.Body) > 0 {
			body = strings.NewReader(task.Body)
		}

		req, err := http.NewRequestWithContext(ctx, task.Method, task.URL, body)
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
		return fmt.Errorf("request failed wtih %v: %v", resp.StatusCode, text)
	})

	if err != nil {
		app.logger.Error("task failed", zap.String("id", task.ID), zap.Error(err))

		if alert {
			err = errors.Join(err, app.sendAlert(ctx, task, err))
		}
	} else {
		app.logger.Info("task completed", zap.String("id", task.ID))
	}

	return err
}

func (app *App) withRetry(ctx context.Context, task *Task, retry Retry, f func(ctx context.Context) error) error {
	timeout := app.timeoutFor(task)
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
			"task will retry",
			zap.String("id", task.ID),
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

func (app *App) sendAlert(_ context.Context, task *Task, taskErr error) error {
	// TOD: cancellation?
	message := fmt.Sprintf("Task %v failed: %v", task.ID, taskErr)
	_, err := app.bot.Send(app.chat, message)
	return err
}
