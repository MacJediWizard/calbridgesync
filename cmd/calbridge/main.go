package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/macjediwizard/calbridge/internal/auth"
	"github.com/macjediwizard/calbridge/internal/caldav"
	"github.com/macjediwizard/calbridge/internal/config"
	"github.com/macjediwizard/calbridge/internal/crypto"
	"github.com/macjediwizard/calbridge/internal/db"
	"github.com/macjediwizard/calbridge/internal/health"
	"github.com/macjediwizard/calbridge/internal/scheduler"
	"github.com/macjediwizard/calbridge/internal/web"
)

const (
	readTimeout     = 10 * time.Second
	writeTimeout    = 30 * time.Second
	idleTimeout     = 120 * time.Second
	shutdownTimeout = 30 * time.Second
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	log.Println("Starting CalBridge...")

	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
	}

	// Set Gin mode
	if cfg.IsProduction() {
		gin.SetMode(gin.ReleaseMode)
	}

	// Initialize database
	database, err := db.New(cfg.Database.Path)
	if err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}
	defer func() {
		if err := database.Close(); err != nil {
			log.Printf("Error closing database: %v", err)
		}
	}()

	// Initialize encryptor
	encryptor, err := crypto.NewEncryptor(cfg.Security.EncryptionKey)
	if err != nil {
		log.Fatalf("Failed to initialize encryptor: %v", err)
	}

	// Initialize OIDC provider
	ctx := context.Background()
	oidcProvider, err := auth.NewOIDCProvider(
		ctx,
		cfg.OIDC.Issuer,
		cfg.OIDC.ClientID,
		cfg.OIDC.ClientSecret,
		cfg.OIDC.RedirectURL,
	)
	if err != nil {
		log.Fatalf("Failed to initialize OIDC provider: %v", err)
	}

	// Initialize session manager
	sessionManager := auth.NewSessionManager(cfg.Security.SessionSecret, cfg.IsProduction())

	// Initialize sync engine
	syncEngine := caldav.NewSyncEngine(database, encryptor)

	// Initialize scheduler
	sched := scheduler.New(database, syncEngine)

	// Initialize health checker
	healthChecker := health.NewChecker(database, cfg.OIDC.Issuer, cfg.CalDAV.DefaultDestURL)

	// Initialize handlers
	handlers := web.NewHandlers(
		cfg,
		database,
		oidcProvider,
		sessionManager,
		encryptor,
		syncEngine,
		sched,
		healthChecker,
	)

	// Load templates
	templates, err := web.LoadTemplates()
	if err != nil {
		log.Fatalf("Failed to load templates: %v", err)
	}

	// Setup Gin router
	router := gin.New()
	router.Use(gin.Recovery())
	router.Use(web.RequestLogger())
	router.Use(web.SecurityHeaders())

	// Set templates
	router.SetHTMLTemplate(templates)

	// Setup routes
	web.SetupRoutes(router, handlers, sessionManager)

	// Create HTTP server
	addr := fmt.Sprintf(":%d", cfg.Server.Port)
	server := &http.Server{
		Addr:         addr,
		Handler:      router,
		ReadTimeout:  readTimeout,
		WriteTimeout: writeTimeout,
		IdleTimeout:  idleTimeout,
	}

	// Start scheduler
	if err := sched.Start(); err != nil {
		log.Fatalf("Failed to start scheduler: %v", err)
	}

	// Start server in goroutine
	go func() {
		log.Printf("Server listening on %s", addr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("Server error: %v", err)
		}
	}()

	// Wait for interrupt signal
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("Shutting down server...")

	// Stop scheduler
	sched.Stop()

	// Graceful shutdown
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("Server forced to shutdown: %v", err)
	}

	log.Println("Server stopped")
}
