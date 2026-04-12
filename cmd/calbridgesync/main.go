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

	"github.com/macjediwizard/calbridgesync/internal/auth"
	"github.com/macjediwizard/calbridgesync/internal/backup"
	"github.com/macjediwizard/calbridgesync/internal/caldav"
	"github.com/macjediwizard/calbridgesync/internal/config"
	"github.com/macjediwizard/calbridgesync/internal/crypto"
	"github.com/macjediwizard/calbridgesync/internal/db"
	"github.com/macjediwizard/calbridgesync/internal/health"
	"github.com/macjediwizard/calbridgesync/internal/notify"
	"github.com/macjediwizard/calbridgesync/internal/scheduler"
	"github.com/macjediwizard/calbridgesync/internal/web"
)

const (
	readTimeout     = 10 * time.Second
	writeTimeout    = 30 * time.Second
	idleTimeout     = 120 * time.Second
	shutdownTimeout = 30 * time.Second
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	log.Println("Starting CalBridgeSync...")

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

	// Initialize session manager with configurable timeouts
	sessionManager := auth.NewSessionManager(
		cfg.Security.SessionSecret,
		cfg.IsProduction(),
		cfg.Security.SessionMaxAgeSecs,
		cfg.Security.OAuthStateMaxAgeSecs,
	)

	// As of #79 the Google OAuth client_id and client_secret are
	// stored per-source in the database, so the global env-var
	// oauth2.Config that the sync engine used to take has been
	// removed. The redirect URL is still instance-level (Google
	// requires it to be pre-registered in each user's Google Cloud
	// Console project) and is read from cfg.GoogleOAuth.RedirectURL
	// inside the web handlers when starting an OAuth flow.
	if cfg.GoogleOAuth.Enabled() {
		log.Printf("Google OAuth2 enabled (redirect=%s, per-source credentials in DB)", cfg.GoogleOAuth.RedirectURL)
	}

	// Initialize sync engine. The engine builds a per-source oauth2
	// config at sync time from the credentials stored on each Google
	// source row, so no instance-level OAuth config is passed in.
	syncEngine := caldav.NewSyncEngine(database, encryptor)

	// Initialize notifier for alerts
	notifyCfg := &notify.Config{
		WebhookEnabled:  cfg.Alerts.WebhookEnabled,
		WebhookURL:      cfg.Alerts.WebhookURL,
		EmailEnabled:    cfg.Alerts.EmailEnabled,
		SMTPHost:        cfg.Alerts.SMTPHost,
		SMTPPort:        cfg.Alerts.SMTPPort,
		SMTPUsername:    cfg.Alerts.SMTPUsername,
		SMTPPassword:    cfg.Alerts.SMTPPassword,
		SMTPFrom:        cfg.Alerts.SMTPFrom,
		SMTPTo:          cfg.Alerts.SMTPTo,
		SMTPTLS:         cfg.Alerts.SMTPTLS,
		CooldownPeriod:  time.Duration(cfg.Alerts.CooldownMinutes) * time.Minute,
		MaxSendAttempts: cfg.Alerts.MaxSendAttempts,
		InitialBackoff:  time.Duration(cfg.Alerts.InitialBackoffMS) * time.Millisecond,
	}

	// Validate notification config if any alerts are enabled
	if notifyCfg.WebhookEnabled || notifyCfg.EmailEnabled {
		if err := notify.ValidateConfig(notifyCfg); err != nil {
			log.Fatalf("Invalid alert configuration: %v", err)
		}
	}

	notifier := notify.New(notifyCfg)

	if notifier.IsEnabled() {
		log.Printf("Alert notifications enabled (webhook: %v, email: %v, cooldown: %d min)",
			cfg.Alerts.WebhookEnabled, cfg.Alerts.EmailEnabled, cfg.Alerts.CooldownMinutes)
	}

	// Initialize scheduler with configurable log retention
	sched := scheduler.New(database, syncEngine, notifier, cfg.LogRetentionDays)

	// Initialize automated backup if enabled
	if cfg.Backup.Enabled {
		backupMgr, err := backup.New(cfg.Database.Path, cfg.Backup.Dir, cfg.Backup.RetentionCount)
		if err != nil {
			log.Printf("WARNING: automated backup disabled: %v", err)
		} else {
			sched.SetBackupManager(backupMgr)
			log.Printf("Automated backup enabled (dir=%s, retention=%d)", cfg.Backup.Dir, cfg.Backup.RetentionCount)
		}
	}

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
		notifier,
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

	// Set custom HTML renderer with layout support
	router.HTMLRender = templates

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
