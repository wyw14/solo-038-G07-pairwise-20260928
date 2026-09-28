package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/wyw14/solo-038/internal/api"
	"github.com/wyw14/solo-038/internal/service"
)

func main() {
	address := os.Getenv("WEBPRESS_ADDR")
	if address == "" {
		address = "127.0.0.1:21245"
	}
	dataDir := os.Getenv("WEBPRESS_DATA")
	if dataDir == "" {
		dataDir = "data"
	}
	runtime, err := service.NewRuntime(service.Config{DataDir: dataDir, FestoonCapacity: 120, RegisterGap: 500 * time.Millisecond, RegisterTolerance: 0.25, WashupTolerance: 0.5})
	if err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := runtime.Start(ctx); err != nil {
		log.Fatal(err)
	}
	server := &http.Server{Addr: address, Handler: api.NewRouter(runtime), ReadHeaderTimeout: 3 * time.Second, IdleTimeout: 30 * time.Second}
	result := make(chan error, 1)
	go func() {
		result <- server.ListenAndServe()
	}()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			log.Printf("shutdown failed: %v", err)
		}
	case err := <-result:
		if !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
	}
}
