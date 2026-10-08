package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"memoryhub/internal/auth"
	"memoryhub/internal/httpapi"
	"memoryhub/internal/hub"
	"memoryhub/internal/llm"
	"memoryhub/internal/store"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	migrate := flag.Bool("migrate", false, "apply database migrations")
	seed := flag.Bool("seed-demo", false, "seed explicit development user and devices")
	token := flag.String("token", "", "issue a development phone or car token")
	flag.Parse()
	secret := os.Getenv("DEV_AUTH_SECRET")
	if *token != "" {
		if len(secret) < 32 || (*token != "phone" && *token != "car") {
			log.Fatal("DEV_AUTH_SECRET must be 32+ bytes and token device must be phone or car")
		}
		fmt.Println(auth.Sign([]byte(secret), auth.Principal{Tenant: "demo", User: "demo-user", Device: *token, Agent: "demo-agent", Expires: time.Now().Add(24 * time.Hour).Unix()}))
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		log.Fatal("DATABASE_URL is required")
	}
	pool, err := store.Open(ctx, url)
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()
	if *migrate {
		if err = store.Migrate(ctx, pool); err != nil {
			log.Fatal(err)
		}
		log.Println("migrations applied")
		return
	}
	if *seed {
		if err = store.Seed(ctx, pool); err != nil {
			log.Fatal(err)
		}
		log.Println("demo identities seeded")
		return
	}
	if os.Getenv("DEV_AUTH_ENABLED") != "true" || len(secret) < 32 {
		log.Fatal("this build requires explicit DEV_AUTH_ENABLED=true and DEV_AUTH_SECRET (32+ bytes); production auth is not implemented")
	}
	address := os.Getenv("LISTEN_ADDR")
	if address == "" {
		address = "127.0.0.1:8080"
	}
	service := &hub.Service{Pool: pool, Key: []byte(secret), Lease: 30 * time.Second}
	var demo *httpapi.DemoConfig
	if os.Getenv("WEB_DEMO_ENABLED") == "true" {
		host, port, splitErr := net.SplitHostPort(address)
		if splitErr != nil {
			log.Fatal("LISTEN_ADDR must be host:port for the web demo")
		}
		if host == "" || host == "0.0.0.0" || host == "::" {
			host = "127.0.0.1"
		}
		demo = &httpapi.DemoConfig{HubURL: "http://" + net.JoinHostPort(host, port), ModelName: os.Getenv("LLM_MODEL")}
		cfg, configErr := llm.FromEnv()
		if configErr == nil {
			model, modelErr := llm.New(cfg)
			configErr = modelErr
			if modelErr == nil {
				demo.Model = model
			}
		}
		if configErr != nil {
			log.Printf("web demo model unavailable: %s", configErr)
		}
	}
	server := &http.Server{Addr: address, Handler: httpapi.NewWithDemo(service, []byte(secret), demo), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 * 1024}
	done := make(chan error, 1)
	go func() { done <- server.ListenAndServe() }()
	log.Printf("MemoryHub listening on %s (development authentication)", address)
	select {
	case err = <-done:
		if err != http.ErrServerClosed {
			log.Fatal(err)
		}
	case <-ctx.Done():
		closeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		server.Shutdown(closeCtx)
	}
}
