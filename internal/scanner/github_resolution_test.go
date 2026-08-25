package scanner

import (
	"context"
	"testing"

	"github.com/eoctet/tendkit/internal/model"
	"github.com/eoctet/tendkit/internal/scanner/handler"
)

func TestResolveGitHubRequiresKnownCanonicalProject(t *testing.T) {
	app := model.Application{URL: "https://example.com/name", Provider: model.ProviderConfig{Type: model.ProviderDefault}}
	resolved, err := (Scanner{GitHub: handler.NewGitHubResolver("bad", "bad", nil)}).resolveGitHub(context.Background(), app)
	if err != nil || resolved.Provider.Type != model.ProviderDefault {
		t.Fatalf("unknown project was guessed: %#v %v", resolved, err)
	}
}
