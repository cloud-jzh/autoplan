package secrets

import (
	"context"
	"errors"
	"testing"

	domainsecrets "github.com/lyming99/autoplan/backend/internal/domain/secrets"
	"github.com/lyming99/autoplan/backend/internal/platform/secrets/fake"
)

func boolPointer(value bool) *bool { return &value }

func TestPreferredUsesFallbackOnlyForUnavailablePrimary(t *testing.T) {
	primary := fake.New(fake.Options{Name: "primary", Available: boolPointer(false)})
	fallback := fake.New(fake.Options{Name: "fallback"})
	provider, err := NewPreferred(PreferenceOptions{Primary: primary, Fallback: fallback, AllowFallback: true})
	if err != nil {
		t.Fatal(err)
	}
	binding := domainsecrets.Binding{Kind: domainsecrets.KindAIConfigAPIKey, Owner: domainsecrets.Owner{Type: "ai_config", ID: "7"}, Version: 1}
	result, err := provider.PutWithProvider(context.Background(), binding, []byte("fallback-material"))
	if err != nil || result.Provider != "fallback" {
		t.Fatalf("fallback put result=%#v error=%v", result, err)
	}
	exists, err := fallback.Exists(context.Background(), binding, result.Reference)
	if err != nil || !exists {
		t.Fatalf("fallback record exists=%t error=%v", exists, err)
	}
}

func TestPreferredDoesNotDowngradeLockedOrCancelledPrimary(t *testing.T) {
	primary := fake.New(fake.Options{Name: "primary"})
	fallback := fake.New(fake.Options{Name: "fallback"})
	provider, err := NewPreferred(PreferenceOptions{Primary: primary, Fallback: fallback, AllowFallback: true})
	if err != nil {
		t.Fatal(err)
	}
	primary.SetFailure(fake.OperationPut, ErrLocked)
	binding := domainsecrets.Binding{Kind: domainsecrets.KindAIConfigAPIKey, Owner: domainsecrets.Owner{Type: "ai_config", ID: "8"}, Version: 1}
	if _, err := provider.PutWithProvider(context.Background(), binding, []byte("locked-material")); !errors.Is(err, ErrLocked) {
		t.Fatalf("locked primary error=%v", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := provider.Capabilities(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled capability error=%v", err)
	}
}
