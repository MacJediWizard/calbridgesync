package crypto

import (
	"errors"
	"strings"
	"testing"
)

func TestNewEncryptor(t *testing.T) {
	t.Run("valid key size", func(t *testing.T) {
		key := make([]byte, KeySize)
		enc, err := NewEncryptor(key)
		if err != nil {
			t.Fatalf("expected no error, got: %v", err)
		}
		if enc == nil {
			t.Fatal("expected encryptor, got nil")
		}
	})

	t.Run("invalid key size - too short", func(t *testing.T) {
		key := make([]byte, 16) // Too short
		_, err := NewEncryptor(key)
		if !errors.Is(err, ErrInvalidKeySize) {
			t.Fatalf("expected ErrInvalidKeySize, got: %v", err)
		}
	})

	t.Run("invalid key size - too long", func(t *testing.T) {
		key := make([]byte, 64) // Too long
		_, err := NewEncryptor(key)
		if !errors.Is(err, ErrInvalidKeySize) {
			t.Fatalf("expected ErrInvalidKeySize, got: %v", err)
		}
	})

	t.Run("empty key", func(t *testing.T) {
		_, err := NewEncryptor(nil)
		if !errors.Is(err, ErrInvalidKeySize) {
			t.Fatalf("expected ErrInvalidKeySize, got: %v", err)
		}
	})
}

func TestEncryptDecrypt(t *testing.T) {
	key, err := GenerateKey()
	if err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}

	enc, err := NewEncryptor(key)
	if err != nil {
		t.Fatalf("failed to create encryptor: %v", err)
	}

	t.Run("round trip", func(t *testing.T) {
		original := "Hello, World!"
		encrypted, err := enc.Encrypt(original)
		if err != nil {
			t.Fatalf("encryption failed: %v", err)
		}

		decrypted, err := enc.Decrypt(encrypted)
		if err != nil {
			t.Fatalf("decryption failed: %v", err)
		}

		if decrypted != original {
			t.Fatalf("expected %q, got %q", original, decrypted)
		}
	})

	t.Run("empty string", func(t *testing.T) {
		original := ""
		encrypted, err := enc.Encrypt(original)
		if err != nil {
			t.Fatalf("encryption failed: %v", err)
		}

		decrypted, err := enc.Decrypt(encrypted)
		if err != nil {
			t.Fatalf("decryption failed: %v", err)
		}

		if decrypted != original {
			t.Fatalf("expected empty string, got %q", decrypted)
		}
	})

	t.Run("unicode and special characters", func(t *testing.T) {
		testCases := []string{
			"Hello, 世界!",
			"Émoji: 🎉🔐💻",
			"Special: !@#$%^&*()_+-=[]{}|;':\",./<>?",
			"Newlines:\n\r\nTabs:\t",
			"Very long string " + strings.Repeat("a", 10000),
		}

		for _, original := range testCases {
			encrypted, err := enc.Encrypt(original)
			if err != nil {
				t.Fatalf("encryption failed for %q: %v", original[:min(50, len(original))], err)
			}

			decrypted, err := enc.Decrypt(encrypted)
			if err != nil {
				t.Fatalf("decryption failed for %q: %v", original[:min(50, len(original))], err)
			}

			if decrypted != original {
				t.Fatalf("mismatch for %q", original[:min(50, len(original))])
			}
		}
	})

	t.Run("different encryptions produce different ciphertext", func(t *testing.T) {
		original := "Test message"
		encrypted1, _ := enc.Encrypt(original)
		encrypted2, _ := enc.Encrypt(original)

		if encrypted1 == encrypted2 {
			t.Fatal("encryptions should produce different ciphertext due to random nonce")
		}

		// Both should decrypt to the same value
		decrypted1, _ := enc.Decrypt(encrypted1)
		decrypted2, _ := enc.Decrypt(encrypted2)

		if decrypted1 != decrypted2 {
			t.Fatal("both decryptions should produce the same plaintext")
		}
	})
}

func TestDecryptErrors(t *testing.T) {
	key, _ := GenerateKey()
	enc, _ := NewEncryptor(key)

	t.Run("invalid base64", func(t *testing.T) {
		_, err := enc.Decrypt("not-valid-base64!!!")
		if err == nil {
			t.Fatal("expected error for invalid base64")
		}
		if !errors.Is(err, ErrInvalidCiphertext) {
			t.Fatalf("expected ErrInvalidCiphertext, got: %v", err)
		}
	})

	t.Run("ciphertext too short", func(t *testing.T) {
		// Valid base64, but too short to contain nonce
		shortData := "AQID" // Just 3 bytes
		_, err := enc.Decrypt(shortData)
		if err == nil {
			t.Fatal("expected error for short ciphertext")
		}
		if !errors.Is(err, ErrInvalidCiphertext) {
			t.Fatalf("expected ErrInvalidCiphertext, got: %v", err)
		}
	})

	t.Run("wrong key", func(t *testing.T) {
		// Encrypt with one key
		encrypted, _ := enc.Encrypt("secret data")

		// Try to decrypt with different key
		differentKey, _ := GenerateKey()
		differentEnc, _ := NewEncryptor(differentKey)

		_, err := differentEnc.Decrypt(encrypted)
		if err == nil {
			t.Fatal("expected error when decrypting with wrong key")
		}
		if !errors.Is(err, ErrDecryptionFailed) {
			t.Fatalf("expected ErrDecryptionFailed, got: %v", err)
		}
	})

	t.Run("tampered ciphertext", func(t *testing.T) {
		encrypted, _ := enc.Encrypt("secret data")

		// Tamper with the ciphertext by modifying a character
		tampered := encrypted[:len(encrypted)-2] + "XX"
		_, err := enc.Decrypt(tampered)
		if err == nil {
			t.Fatal("expected error for tampered ciphertext")
		}
	})
}

func TestGenerateKey(t *testing.T) {
	t.Run("correct length", func(t *testing.T) {
		key, err := GenerateKey()
		if err != nil {
			t.Fatalf("failed to generate key: %v", err)
		}
		if len(key) != KeySize {
			t.Fatalf("expected key length %d, got %d", KeySize, len(key))
		}
	})

	t.Run("unique keys", func(t *testing.T) {
		key1, _ := GenerateKey()
		key2, _ := GenerateKey()

		if string(key1) == string(key2) {
			t.Fatal("generated keys should be unique")
		}
	})
}
