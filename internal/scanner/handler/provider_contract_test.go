package handler

import (
	"testing"

	"github.com/eoctet/tendkit/internal/model"
)

func assertActiveProvider(t *testing.T, provider model.ProviderType) {
	t.Helper()
	for _, retired := range []string{"none", "command", "vscode", "chrome", "firefox", "url_json", "url_text"} {
		if string(provider) == retired {
			t.Fatalf("retired provider %q", provider)
		}
	}
	if !provider.Valid() {
		t.Fatalf("invalid provider %q", provider)
	}
}
