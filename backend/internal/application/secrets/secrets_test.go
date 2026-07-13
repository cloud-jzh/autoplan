package secrets

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	domainsecrets "github.com/lyming99/autoplan/backend/internal/domain/secrets"
	platformsecrets "github.com/lyming99/autoplan/backend/internal/platform/secrets"
	"github.com/lyming99/autoplan/backend/internal/platform/secrets/fake"
	"github.com/lyming99/autoplan/backend/internal/repository"
)

type secretTestClock struct{ value time.Time }

func (clock secretTestClock) Now() time.Time { return clock.value }

// secretTestStore embeds the unrelated write surface so these tests can focus
// on the secret_refs extension rather than duplicating pre-P008 repositories.
type secretTestStore struct {
	repository.WriteTransaction
	refs map[string]domainsecrets.Ref
}

func secretTestKey(kind domainsecrets.Kind, owner domainsecrets.Owner) string {
	return string(kind) + "|" + owner.Type + "|" + owner.ID
}

func (store *secretTestStore) GetSecretRef(_ context.Context, kind domainsecrets.Kind, owner domainsecrets.Owner) (domainsecrets.Ref, bool, error) {
	value, found := store.refs[secretTestKey(kind, owner)]
	return value, found, nil
}

func (store *secretTestStore) ListRetiredSecretRefs(context.Context, domainsecrets.Owner) ([]domainsecrets.Ref, error) {
	return nil, nil
}

func (store *secretTestStore) CreateSecretRef(_ context.Context, input domainsecrets.Create) (domainsecrets.Ref, error) {
	key := secretTestKey(input.Binding.Kind, input.Binding.Owner)
	if _, found := store.refs[key]; found {
		return domainsecrets.Ref{}, repository.ErrDuplicate
	}
	value := domainsecrets.Ref{ID: int64(len(store.refs) + 1), Binding: input.Binding, Provider: input.Provider,
		Reference: input.Reference, HasValue: true, CreatedAt: input.CreatedAt, UpdatedAt: input.CreatedAt, Version: input.Binding.Version}
	store.refs[key] = value
	return value, nil
}

func (store *secretTestStore) ReplaceSecretRef(_ context.Context, input domainsecrets.Replace) (domainsecrets.Ref, error) {
	key := secretTestKey(input.Binding.Kind, input.Binding.Owner)
	current, found := store.refs[key]
	if !found {
		return domainsecrets.Ref{}, repository.ErrNotFound
	}
	if current.Version != input.ExpectedVersion {
		return domainsecrets.Ref{}, repository.ErrVersionConflict
	}
	current.Binding, current.Provider, current.Reference = input.Binding, input.Provider, input.Reference
	current.HasValue, current.UpdatedAt, current.Version = true, input.UpdatedAt, input.Binding.Version
	store.refs[key] = current
	return current, nil
}

func (store *secretTestStore) RetireSecretRef(_ context.Context, input domainsecrets.Retire) (domainsecrets.Ref, error) {
	key := secretTestKey(input.Binding.Kind, input.Binding.Owner)
	current, found := store.refs[key]
	if !found {
		return domainsecrets.Ref{}, repository.ErrNotFound
	}
	if current.Version != input.ExpectedVersion {
		return domainsecrets.Ref{}, repository.ErrVersionConflict
	}
	current.HasValue, current.UpdatedAt = false, input.UpdatedAt
	store.refs[key] = current
	return current, nil
}

func (store *secretTestStore) PurgeSecretRef(_ context.Context, input domainsecrets.Delete) error {
	key := secretTestKey(input.Binding.Kind, input.Binding.Owner)
	current, found := store.refs[key]
	if !found {
		return repository.ErrNotFound
	}
	if current.Version != input.ExpectedVersion {
		return repository.ErrVersionConflict
	}
	delete(store.refs, key)
	return nil
}

func (store *secretTestStore) PurgeRetiredSecretRef(context.Context, domainsecrets.Ref) error {
	return nil
}

type secretTestWriter struct {
	mu              sync.Mutex
	store           *secretTestStore
	transactions    int
	failTransaction int
	checkErr        error
}

func (writer *secretTestWriter) Check(context.Context) error { return writer.checkErr }
func (writer *secretTestWriter) Close() error                { return nil }

func (writer *secretTestWriter) Transact(ctx context.Context, operation func(repository.WriteTransaction) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	writer.mu.Lock()
	defer writer.mu.Unlock()
	writer.transactions++
	before := make(map[string]domainsecrets.Ref, len(writer.store.refs))
	for key, value := range writer.store.refs {
		before[key] = value
	}
	if err := operation(writer.store); err != nil {
		writer.store.refs = before
		return err
	}
	if writer.failTransaction == writer.transactions {
		writer.store.refs = before
		return repository.ErrCommit
	}
	return nil
}

func newSecretService() (*Service, *secretTestWriter, *fake.Provider) {
	store := &secretTestStore{refs: make(map[string]domainsecrets.Ref)}
	writer := &secretTestWriter{store: store}
	provider := fake.New(fake.Options{Name: "fixture-provider"})
	service := NewService(Dependencies{Writer: writer, Provider: provider,
		Clock: secretTestClock{value: time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)}})
	return service, writer, provider
}

func TestSecretsLifecycleIsVersionedAndStatusIsSanitized(t *testing.T) {
	service, _, _ := newSecretService()
	owner := domainsecrets.Owner{Type: "ai_config", ID: "7"}
	created, err := service.Create(context.Background(), CreateRequest{Kind: domainsecrets.KindAIConfigAPIKey, Owner: owner, Value: []byte("synthetic-secret-material")})
	if err != nil || !created.HasValue || created.Version != 1 {
		t.Fatalf("create status=%#v error=%v", created, err)
	}
	replaced, err := service.Replace(context.Background(), ReplaceRequest{Kind: domainsecrets.KindAIConfigAPIKey, Owner: owner, ExpectedVersion: 1, Value: []byte("replacement-material")})
	if err != nil || replaced.Version != 2 || !replaced.HasValue {
		t.Fatalf("replace status=%#v error=%v", replaced, err)
	}
	if _, err := service.Replace(context.Background(), ReplaceRequest{Kind: domainsecrets.KindAIConfigAPIKey, Owner: owner, ExpectedVersion: 1, Value: []byte("stale-material")}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale replace error=%v", err)
	}
	if err := service.Delete(context.Background(), DeleteRequest{Kind: domainsecrets.KindAIConfigAPIKey, Owner: owner, ExpectedVersion: 2}); err != nil {
		t.Fatalf("delete error=%v", err)
	}
	if _, err := service.Status(context.Background(), domainsecrets.KindAIConfigAPIKey, owner); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted status error=%v", err)
	}
	encoded, err := json.Marshal(replaced)
	if err != nil || string(encoded) == "" || string(encoded) == "{}" {
		t.Fatalf("safe status json=%q error=%v", encoded, err)
	}
	if string(encoded) != `{"kind":"ai_config_api_key","has_value":true,"version":2}` {
		t.Fatalf("status JSON exposed an unexpected field: %s", encoded)
	}
}

func TestSecretsCommitFailureCompensatesProviderWrite(t *testing.T) {
	service, writer, provider := newSecretService()
	writer.failTransaction = 3 // cleanup, lookup, then secret-ref metadata commit.
	owner := domainsecrets.Owner{Type: "ai_config", ID: "8"}
	_, err := service.Create(context.Background(), CreateRequest{Kind: domainsecrets.KindAIConfigAPIKey, Owner: owner, Value: []byte("transactional-material")})
	if !errors.Is(err, repository.ErrCommit) {
		t.Fatalf("commit failure error=%v", err)
	}
	binding := domainsecrets.Binding{Kind: domainsecrets.KindAIConfigAPIKey, Owner: owner, Version: 1}
	exists, existsErr := provider.Exists(context.Background(), binding, "fake_000000000000000000000001")
	if existsErr != nil || exists {
		t.Fatalf("compensated provider record exists=%t error=%v", exists, existsErr)
	}
}

func TestSecretsProviderAndContextFailuresAreMappedWithoutPayload(t *testing.T) {
	service, _, provider := newSecretService()
	provider.SetFailure(fake.OperationPut, platformsecrets.ErrUnavailable)
	owner := domainsecrets.Owner{Type: "ai_config", ID: "9"}
	if _, err := service.Create(context.Background(), CreateRequest{Kind: domainsecrets.KindAIConfigAPIKey, Owner: owner, Value: []byte("provider-failure-material")}); !errors.Is(err, ErrProvider) {
		t.Fatalf("provider failure error=%v", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := service.Create(cancelled, CreateRequest{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled create error=%v", err)
	}
}
