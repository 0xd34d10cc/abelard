package main

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.uber.org/zap"
)

func main() {
	logger, err := zap.NewProduction()
	if err != nil {
		panic(err)
	}
	defer logger.Sync()

	config, err := LoadConfig("./config.json")
	if err != nil {
		logger.Fatal("Failed to read config", zap.Error(err))
	}

	logger.Info("starting", zap.Any("config", config))

	app, err := NewApp(config, logger)
	if err != nil {
		logger.Fatal("App initialization failed", zap.Error(err))
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	go func() {
		<-ctx.Done()
		logger.Info("Shutting down")
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		app.Shutdown(ctx)
	}()

	err = app.Run(ctx)
	if err != nil && !errors.Is(err, context.Canceled) {
		logger.Fatal("App failed", zap.Error(err))
	}

	app.WaitShutdown()
}
