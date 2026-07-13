package attachments

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"sort"
	"strings"
	"testing"
	"time"

	domainfiles "github.com/lyming99/autoplan/backend/internal/domain/files"
	"github.com/lyming99/autoplan/backend/internal/repository"
)

const p008AttachmentTime = "2026-07-11T09:00:00.000Z"

type p008AttachmentClock struct{ value time.Time }

func (clock p008AttachmentClock) Now() time.Time { return clock.value }

type p008AttachmentWriter struct {
	tx       *p008AttachmentTransaction
	checkErr error
}

func (writer *p008AttachmentWriter) Check(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return writer.checkErr
}

func (writer *p008AttachmentWriter) TransactIntake(ctx context.Context, operation func(repository.IntakeWriteTransaction) error) error {
	if err := writer.Check(ctx); err != nil {
		return err
	}
	if operation == nil {
		return repository.ErrTransaction
	}
	return operation(writer.tx)
}

func (writer *p008AttachmentWriter) Close() error { return nil }

type p008AttachmentTransaction struct {
	repository.IntakeWriteTransaction
	projects              []repository.Project
	attachments           map[int64]domainfiles.Attachment
	operations            map[string]domainfiles.AttachmentOperation
	nextAttachmentID      int64
	createAttachmentErr   error
	createOperationErr    error
	updateOperationErr    error
	deleteAttachmentErr   error
	validateAttachmentErr error
}

func newP008AttachmentTransaction() *p008AttachmentTransaction {
	return &p008AttachmentTransaction{
		projects:         []repository.Project{{ID: 1, Name: "fixture", CreatedAt: p008AttachmentTime, UpdatedAt: p008AttachmentTime}},
		attachments:      make(map[int64]domainfiles.Attachment),
		operations:       make(map[string]domainfiles.AttachmentOperation),
		nextAttachmentID: 1,
	}
}

func (transaction *p008AttachmentTransaction) ListProjects(context.Context) ([]repository.Project, error) {
	return append([]repository.Project(nil), transaction.projects...), nil
}

func (transaction *p008AttachmentTransaction) CreateAttachment(_ context.Context, value domainfiles.Attachment) (domainfiles.Attachment, error) {
	if transaction.createAttachmentErr != nil {
		return domainfiles.Attachment{}, transaction.createAttachmentErr
	}
	value.ID = transaction.nextAttachmentID
	transaction.nextAttachmentID++
	transaction.attachments[value.ID] = value
	return value, nil
}

func (transaction *p008AttachmentTransaction) GetAttachment(_ context.Context, projectID, attachmentID int64) (domainfiles.Attachment, bool, error) {
	value, found := transaction.attachments[attachmentID]
	if !found || value.ProjectID != projectID {
		return domainfiles.Attachment{}, false, nil
	}
	return value, true, nil
}

func (transaction *p008AttachmentTransaction) ListAttachmentsForOwner(_ context.Context, projectID int64, ownerType domainfiles.AttachmentOwner, ownerID int64) ([]domainfiles.Attachment, error) {
	result := make([]domainfiles.Attachment, 0)
	for _, attachment := range transaction.attachments {
		if attachment.ProjectID == projectID && attachment.OwnerType.Canonical() == ownerType.Canonical() && attachment.OwnerID == ownerID {
			result = append(result, attachment)
		}
	}
	sort.Slice(result, func(left, right int) bool { return result[left].ID < result[right].ID })
	return result, nil
}

func (transaction *p008AttachmentTransaction) ListProjectAttachments(_ context.Context, projectID int64) ([]domainfiles.Attachment, error) {
	result := make([]domainfiles.Attachment, 0)
	for _, attachment := range transaction.attachments {
		if attachment.ProjectID == projectID {
			result = append(result, attachment)
		}
	}
	sort.Slice(result, func(left, right int) bool { return result[left].ID < result[right].ID })
	return result, nil
}

func (transaction *p008AttachmentTransaction) DeleteAttachment(_ context.Context, projectID, attachmentID int64) (domainfiles.Attachment, error) {
	if transaction.deleteAttachmentErr != nil {
		return domainfiles.Attachment{}, transaction.deleteAttachmentErr
	}
	value, found := transaction.attachments[attachmentID]
	if !found || value.ProjectID != projectID {
		return domainfiles.Attachment{}, repository.ErrNotFound
	}
	delete(transaction.attachments, attachmentID)
	return value, nil
}

func (transaction *p008AttachmentTransaction) UpdateAttachmentStorageKey(_ context.Context, projectID, attachmentID int64, key string) error {
	value, found := transaction.attachments[attachmentID]
	if !found || value.ProjectID != projectID {
		return repository.ErrNotFound
	}
	value.StoredKey = key
	transaction.attachments[attachmentID] = value
	return nil
}

func (transaction *p008AttachmentTransaction) CreateAttachmentOperation(_ context.Context, value domainfiles.AttachmentOperation) error {
	if transaction.createOperationErr != nil {
		return transaction.createOperationErr
	}
	if _, exists := transaction.operations[value.ID]; exists {
		return repository.ErrDuplicate
	}
	transaction.operations[value.ID] = value
	return nil
}

func (transaction *p008AttachmentTransaction) UpdateAttachmentOperation(_ context.Context, value domainfiles.AttachmentOperation) error {
	if transaction.updateOperationErr != nil {
		return transaction.updateOperationErr
	}
	if _, exists := transaction.operations[value.ID]; !exists {
		return repository.ErrNotFound
	}
	transaction.operations[value.ID] = value
	return nil
}

func (transaction *p008AttachmentTransaction) GetAttachmentOperation(_ context.Context, operationID string) (domainfiles.AttachmentOperation, bool, error) {
	value, found := transaction.operations[operationID]
	return value, found, nil
}

func (transaction *p008AttachmentTransaction) ListAttachmentOperations(_ context.Context, projectID int64, includeComplete bool) ([]domainfiles.AttachmentOperation, error) {
	result := make([]domainfiles.AttachmentOperation, 0)
	for _, operation := range transaction.operations {
		if operation.ProjectID != projectID || (!includeComplete && !operation.State.Recoverable()) {
			continue
		}
		result = append(result, operation)
	}
	sort.Slice(result, func(left, right int) bool { return result[left].ID < result[right].ID })
	return result, nil
}

func (transaction *p008AttachmentTransaction) ValidateAttachmentOwner(context.Context, int64, domainfiles.AttachmentOwner, int64) error {
	return transaction.validateAttachmentErr
}

type p008StoredFile struct {
	bytes      []byte
	modifiedAt time.Time
}

type p008AttachmentStore struct {
	files         map[string]p008StoredFile
	stageErr      error
	promoteErr    error
	quarantineErr error
	removeErr     error
}

func newP008AttachmentStore() *p008AttachmentStore {
	return &p008AttachmentStore{files: make(map[string]p008StoredFile)}
}

func (store *p008AttachmentStore) Stage(_ context.Context, operationID string, source io.Reader, maximum int64) (domainfiles.StagedAttachment, error) {
	if store.stageErr != nil {
		return domainfiles.StagedAttachment{}, store.stageErr
	}
	data, err := io.ReadAll(source)
	if err != nil {
		return domainfiles.StagedAttachment{}, err
	}
	if int64(len(data)) > maximum {
		return domainfiles.StagedAttachment{}, domainfiles.ErrAttachmentTooLarge
	}
	stageKey := "staged/" + operationID + ".blob"
	store.files[stageKey] = p008StoredFile{bytes: append([]byte(nil), data...), modifiedAt: time.Unix(0, 0).UTC()}
	return domainfiles.StagedAttachment{
		StageKey: stageKey, ReadyKey: "ready/" + operationID + ".blob", Size: int64(len(data)), SHA256: p008Digest(data), Sample: append([]byte(nil), data...),
	}, nil
}

func (store *p008AttachmentStore) Promote(_ context.Context, stageKey, readyKey string) error {
	if store.promoteErr != nil {
		return store.promoteErr
	}
	file, found := store.files[stageKey]
	if !found {
		return fs.ErrNotExist
	}
	store.files[readyKey] = file
	delete(store.files, stageKey)
	return nil
}

func (store *p008AttachmentStore) Quarantine(_ context.Context, readyKey, quarantineKey string) error {
	if store.quarantineErr != nil {
		return store.quarantineErr
	}
	file, found := store.files[readyKey]
	if !found {
		return fs.ErrNotExist
	}
	store.files[quarantineKey] = file
	delete(store.files, readyKey)
	return nil
}

func (store *p008AttachmentStore) Remove(_ context.Context, key string) error {
	if store.removeErr != nil {
		return store.removeErr
	}
	if _, found := store.files[key]; !found {
		return fs.ErrNotExist
	}
	delete(store.files, key)
	return nil
}

func (store *p008AttachmentStore) Open(_ context.Context, key string) (io.ReadCloser, error) {
	file, found := store.files[key]
	if !found {
		return nil, fs.ErrNotExist
	}
	return io.NopCloser(bytes.NewReader(file.bytes)), nil
}

func (store *p008AttachmentStore) Inspect(_ context.Context, key string) (domainfiles.StoredAttachmentFile, string, error) {
	file, found := store.files[key]
	if !found {
		return domainfiles.StoredAttachmentFile{}, "", fs.ErrNotExist
	}
	return domainfiles.StoredAttachmentFile{Key: key, Size: int64(len(file.bytes)), ModifiedAt: file.modifiedAt}, p008Digest(file.bytes), nil
}

func (store *p008AttachmentStore) Sample(_ context.Context, key string, limit int) ([]byte, error) {
	file, found := store.files[key]
	if !found {
		return nil, fs.ErrNotExist
	}
	if limit < len(file.bytes) {
		return append([]byte(nil), file.bytes[:limit]...), nil
	}
	return append([]byte(nil), file.bytes...), nil
}

func (store *p008AttachmentStore) List(context.Context) ([]domainfiles.StoredAttachmentFile, error) {
	keys := make([]string, 0, len(store.files))
	for key := range store.files {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]domainfiles.StoredAttachmentFile, 0, len(keys))
	for _, key := range keys {
		file := store.files[key]
		result = append(result, domainfiles.StoredAttachmentFile{Key: key, Size: int64(len(file.bytes)), ModifiedAt: file.modifiedAt})
	}
	return result, nil
}

func p008Digest(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

func newP008AttachmentService(transaction *p008AttachmentTransaction, store *p008AttachmentStore) *Service {
	clock, err := time.Parse(time.RFC3339Nano, p008AttachmentTime)
	if err != nil {
		panic(err)
	}
	return NewService(Dependencies{Writer: &p008AttachmentWriter{tx: transaction}, Store: store, Clock: p008AttachmentClock{value: clock}})
}

func TestP008AttachmentInputMatrixRejectsUnsafeBytesBeforeMetadata(t *testing.T) {
	cases := []struct {
		name        string
		displayName string
		mimeType    string
		content     []byte
		want        error
	}{
		{name: "zero bytes", displayName: "fixture.txt", mimeType: "text/plain", content: nil, want: domainfiles.ErrAttachmentContent},
		{name: "forged png", displayName: "fixture.png", mimeType: "image/png", content: []byte("not-a-png"), want: domainfiles.ErrAttachmentContent},
		{name: "dangerous name", displayName: "../fixture.txt", mimeType: "text/plain", content: []byte("safe"), want: domainfiles.ErrInvalidAttachment},
		{name: "absolute name", displayName: "C:\\private\\fixture.txt", mimeType: "text/plain", content: []byte("safe"), want: domainfiles.ErrInvalidAttachment},
		{name: "unc name", displayName: "\\\\server\\share\\fixture.txt", mimeType: "text/plain", content: []byte("safe"), want: domainfiles.ErrInvalidAttachment},
		{name: "device name", displayName: "NUL.txt", mimeType: "text/plain", content: []byte("safe"), want: domainfiles.ErrInvalidAttachment},
		{name: "nul name", displayName: "fixture\x00.txt", mimeType: "text/plain", content: []byte("safe"), want: domainfiles.ErrInvalidAttachment},
		{name: "svg name", displayName: "fixture.svg", mimeType: "text/plain", content: []byte("safe"), want: domainfiles.ErrInvalidAttachment},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			transaction := newP008AttachmentTransaction()
			store := newP008AttachmentStore()
			service := newP008AttachmentService(transaction, store)
			_, err := service.Upload(context.Background(), UploadCommand{
				OperationID: "input-" + strings.ReplaceAll(item.name, " ", "-"), ProjectID: 1, OwnerType: domainfiles.OwnerRequirement, OwnerID: 1,
				DisplayName: item.displayName, MIMEType: item.mimeType, Content: bytes.NewReader(item.content),
			})
			if !errors.Is(err, item.want) || len(transaction.attachments) != 0 || len(transaction.operations) != 0 || len(store.files) != 0 {
				t.Fatalf("input rejection drifted: err=%v attachments=%d operations=%d files=%d", err, len(transaction.attachments), len(transaction.operations), len(store.files))
			}
		})
	}
}

func TestP008AttachmentFaultInjectionLeavesRecoverableOperationOrNoBytes(t *testing.T) {
	t.Run("cancelled and stage-permission failures do not create metadata", func(t *testing.T) {
		for _, item := range []struct {
			name      string
			configure func(*p008AttachmentStore)
			context   context.Context
			want      error
		}{
			{
				name: "cancelled",
				context: func() context.Context {
					ctx, cancel := context.WithCancel(context.Background())
					cancel()
					return ctx
				}(),
				want: context.Canceled,
			},
			{
				name:      "stage permission",
				configure: func(store *p008AttachmentStore) { store.stageErr = fs.ErrPermission },
				context:   context.Background(),
				want:      fs.ErrPermission,
			},
		} {
			t.Run(item.name, func(t *testing.T) {
				transaction := newP008AttachmentTransaction()
				store := newP008AttachmentStore()
				if item.configure != nil {
					item.configure(store)
				}
				service := newP008AttachmentService(transaction, store)
				_, err := service.Upload(item.context, UploadCommand{
					OperationID: "stage-" + strings.ReplaceAll(item.name, " ", "-"), ProjectID: 1, OwnerType: domainfiles.OwnerRequirement, OwnerID: 1,
					DisplayName: "fixture.txt", MIMEType: "text/plain", Content: strings.NewReader("fixture"),
				})
				if !errors.Is(err, item.want) || len(transaction.attachments) != 0 || len(transaction.operations) != 0 || len(store.files) != 0 {
					t.Fatalf("pre-stage failure drifted: err=%v attachments=%d operations=%d files=%d", err, len(transaction.attachments), len(transaction.operations), len(store.files))
				}
			})
		}
	})

	t.Run("disk-full stage failure does not create partial metadata", func(t *testing.T) {
		transaction := newP008AttachmentTransaction()
		store := newP008AttachmentStore()
		diskFull := errors.New("synthetic disk-full stage fault")
		store.stageErr = diskFull
		service := newP008AttachmentService(transaction, store)
		_, err := service.Upload(context.Background(), UploadCommand{
			OperationID: "stage-disk-full", ProjectID: 1, OwnerType: domainfiles.OwnerRequirement, OwnerID: 1,
			DisplayName: "fixture.txt", MIMEType: "text/plain", Content: strings.NewReader("fixture"),
		})
		if !errors.Is(err, diskFull) || len(transaction.attachments) != 0 || len(transaction.operations) != 0 || len(store.files) != 0 {
			t.Fatalf("disk-full pre-stage failure drifted: err=%v attachments=%d operations=%d files=%d", err, len(transaction.attachments), len(transaction.operations), len(store.files))
		}
	})

	t.Run("metadata failure and cleanup failure retain a recovery anchor", func(t *testing.T) {
		transaction := newP008AttachmentTransaction()
		transaction.createAttachmentErr = repository.ErrTransaction
		store := newP008AttachmentStore()
		store.removeErr = errors.New("synthetic remove fault")
		service := newP008AttachmentService(transaction, store)
		_, err := service.Upload(context.Background(), UploadCommand{
			OperationID: "metadata-fault", ProjectID: 1, OwnerType: domainfiles.OwnerRequirement, OwnerID: 1,
			DisplayName: "fixture.txt", MIMEType: "text/plain", Content: strings.NewReader("fixture"),
		})
		operation, found := transaction.operations["metadata-fault"]
		if !errors.Is(err, domainfiles.ErrAttachmentRecovery) || !found || operation.State != domainfiles.StateFailed ||
			len(transaction.attachments) != 0 || len(store.files) != 1 {
			t.Fatalf("recoverable metadata failure drifted: err=%v operation=%#v attachments=%d files=%d", err, operation, len(transaction.attachments), len(store.files))
		}
	})

	t.Run("rename/promote failure reports recovery without losing metadata", func(t *testing.T) {
		transaction := newP008AttachmentTransaction()
		store := newP008AttachmentStore()
		store.promoteErr = errors.New("synthetic rename fault")
		service := newP008AttachmentService(transaction, store)
		result, err := service.Upload(context.Background(), UploadCommand{
			OperationID: "promote-fault", ProjectID: 1, OwnerType: domainfiles.OwnerRequirement, OwnerID: 1,
			DisplayName: "fixture.txt", MIMEType: "text/plain", Content: strings.NewReader("fixture"),
		})
		operation := transaction.operations["promote-fault"]
		if err != nil || !result.RecoveryRequired || result.State != domainfiles.StateFailed || operation.State != domainfiles.StateFailed ||
			len(transaction.attachments) != 1 || len(store.files) != 1 {
			t.Fatalf("promote fault drifted: result=%#v err=%v operation=%#v attachments=%d files=%d", result, err, operation, len(transaction.attachments), len(store.files))
		}
	})

	t.Run("same display name produces distinct durable attachment identities", func(t *testing.T) {
		transaction := newP008AttachmentTransaction()
		store := newP008AttachmentStore()
		service := newP008AttachmentService(transaction, store)
		for _, operationID := range []string{"same-name-one", "same-name-two"} {
			result, err := service.Upload(context.Background(), UploadCommand{
				OperationID: operationID, ProjectID: 1, OwnerType: domainfiles.OwnerRequirement, OwnerID: 1,
				DisplayName: "same.txt", MIMEType: "text/plain", Content: strings.NewReader("fixture"),
			})
			if err != nil || result.Attachment.ID <= 0 || result.RecoveryRequired {
				t.Fatalf("same-name upload %q drifted: result=%#v err=%v", operationID, result, err)
			}
		}
		if len(transaction.attachments) != 2 || len(store.files) != 2 {
			t.Fatalf("same-name uploads collided: attachments=%#v files=%#v", transaction.attachments, store.files)
		}
	})

	t.Run("owner count limit preserves existing metadata and cleans staged bytes", func(t *testing.T) {
		transaction := newP008AttachmentTransaction()
		for id := int64(1); id <= domainfiles.MaximumAttachmentCount; id++ {
			transaction.attachments[id] = domainfiles.Attachment{
				ID: id, ProjectID: 1, OwnerType: domainfiles.OwnerRequirement, OwnerID: 1,
				DisplayName: "existing.txt", StoredKey: "ready/existing.blob", MIMEType: "text/plain", Size: 1,
				SHA256: strings.Repeat("a", 64), CreatedAt: p008AttachmentTime,
			}
		}
		transaction.nextAttachmentID = domainfiles.MaximumAttachmentCount + 1
		store := newP008AttachmentStore()
		service := newP008AttachmentService(transaction, store)
		_, err := service.Upload(context.Background(), UploadCommand{
			OperationID: "count-limit", ProjectID: 1, OwnerType: domainfiles.OwnerRequirement, OwnerID: 1,
			DisplayName: "limit.txt", MIMEType: "text/plain", Content: strings.NewReader("fixture"),
		})
		operation, found := transaction.operations["count-limit"]
		if !errors.Is(err, domainfiles.ErrAttachmentLimit) || len(transaction.attachments) != domainfiles.MaximumAttachmentCount ||
			!found || operation.State != domainfiles.StateFailed || len(store.files) != 0 {
			t.Fatalf("attachment count limit drifted: err=%v attachments=%d operations=%d files=%d", err, len(transaction.attachments), len(transaction.operations), len(store.files))
		}
	})
}
