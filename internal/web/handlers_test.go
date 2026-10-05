package web

import (
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/macjediwizard/calbridgesync/internal/config"
)

func init() {
	gin.SetMode(gin.TestMode)
}

func TestNewHandlers(t *testing.T) {
	t.Run("creates handlers with all nil dependencies", func(t *testing.T) {
		handlers := NewHandlers(nil, nil, nil, nil, nil, nil, nil, nil, nil)

		if handlers == nil {
			t.Fatal("expected non-nil handlers")
		}
	})

	t.Run("creates handlers with config", func(t *testing.T) {
		cfg := &config.Config{}
		handlers := NewHandlers(cfg, nil, nil, nil, nil, nil, nil, nil, nil)

		if handlers == nil {
			t.Fatal("expected non-nil handlers")
		}
		if handlers.cfg != cfg {
			t.Error("expected cfg to be set")
		}
	})
}

func TestHandlersStruct(t *testing.T) {
	t.Run("Handlers struct has expected fields", func(t *testing.T) {
		// Verify struct can be created with all fields
		h := &Handlers{
			cfg:        &config.Config{},
			db:         nil, // Would be *db.DB in real use
			oidc:       nil,
			session:    nil,
			encryptor:  nil,
			syncEngine: nil,
			scheduler:  nil,
			health:     nil,
			notifier:   nil,
		}

		if h.cfg == nil {
			t.Error("expected cfg to be set")
		}
	})
}
