package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"kvazar/internal/bot"
)

func main() {
	cfg := bot.Config{
		Token:      pickToken(),
		FFMpegPath: os.Getenv("KVZ_FFMPEG_PATH"),
		YTDLPPath:  os.Getenv("KVZ_YTDLP_PATH"),
		Status:     os.Getenv("KVZ_STATUS"),
	}

	if cfg.Token == "" {
		log.Fatal("kvazar: please provide a Discord bot token via KVZ_DISCORD_TOKEN or DISCORD_TOKEN")
	}

	instance, err := bot.New(cfg)
	if err != nil {
		log.Fatalf("kvazar: failed to initialise bot: %v", err)
	}

	if err := instance.Open(context.Background()); err != nil {
		log.Fatalf("kvazar: failed to open session: %v", err)
	}

	// Start health check HTTP server
	port := os.Getenv("KVZ_HEALTH_PORT")
	if port == "" {
		port = "8080"
	}
	go startHealthServer(port, instance)

	log.Println("kvazar is online — press Ctrl+C to exit")

	waitForShutdown()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := instance.Close(shutdownCtx); err != nil {
		log.Printf("kvazar: graceful shutdown encountered errors: %v", err)
	}

	log.Println("kvazar stopped. stay cosmic.")
}

func pickToken() string {
	if token := os.Getenv("KVZ_DISCORD_TOKEN"); token != "" {
		return token
	}
	return os.Getenv("DISCORD_TOKEN")
}

func waitForShutdown() {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
	signal.Stop(sigCh)
}

func startHealthServer(port string, instance *bot.Kvazar) {
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `{"status":"ok","service":"kvazar"}`)
	})

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "Kvazar Discord Bot - Health Check OK")
	})

	addr := ":" + port
	log.Printf("Health check server listening on %s", addr)
	if err := http.ListenAndServe(addr, nil); err != nil {
		log.Printf("Health check server error: %v", err)
	}
}
