package sqlite

import (
	"context"
	"database/sql"

	domainsecrets "github.com/lyming99/autoplan/backend/internal/domain/secrets"
	"github.com/lyming99/autoplan/backend/internal/repository"
)

// field_name is the existing schema-compatible slot for a domain secret Kind.
// It stores semantic kind metadata, never a provider locator or secret value.
const secretRefSelectColumns = "id, owner_type, owner_id, field_name, provider, reference, has_value, created_at, updated_at, version"

// GetSecretRef resolves metadata only. The opaque platform reference never
// leaves this repository/application boundary through ordinary DTOs.
func (transaction *writeTransaction) GetSecretRef(
	ctx context.Context,
	kind domainsecrets.Kind,
	owner domainsecrets.Owner,
) (domainsecrets.Ref, bool, error) {
	if domainsecrets.ValidateScope(kind, owner) != nil {
		return domainsecrets.Ref{}, false, repository.ErrInvalidAutomation
	}
	value, err := scanSecretRef(transaction.tx.QueryRowContext(ctx,
		"SELECT "+secretRefSelectColumns+" FROM secret_refs WHERE owner_type = ? AND owner_id = ? AND field_name = ?",
		owner.Type, owner.ID, string(kind)))
	if err == sql.ErrNoRows {
		return domainsecrets.Ref{}, false, nil
	}
	if err != nil {
		return domainsecrets.Ref{}, false, safeSQLError(ctx, err)
	}
	return value, true, nil
}

func (transaction *writeTransaction) ListRetiredSecretRefs(ctx context.Context, owner domainsecrets.Owner) ([]domainsecrets.Ref, error) {
	if domainsecrets.ValidateOwner(owner) != nil {
		return nil, repository.ErrInvalidAutomation
	}
	rows, err := transaction.tx.QueryContext(ctx,
		"SELECT "+secretRefSelectColumns+" FROM secret_refs WHERE owner_type = ? AND owner_id = ? AND field_name LIKE 'retired.%' AND has_value = 0 ORDER BY id ASC",
		owner.Type, owner.ID)
	if err != nil {
		return nil, safeSQLError(ctx, err)
	}
	defer rows.Close()
	result := make([]domainsecrets.Ref, 0)
	for rows.Next() {
		value, scanErr := scanSecretRef(rows)
		if scanErr != nil {
			return nil, safeSQLError(ctx, scanErr)
		}
		result = append(result, value)
	}
	if err := rows.Err(); err != nil {
		return nil, safeSQLError(ctx, err)
	}
	return result, nil
}

func (transaction *writeTransaction) CreateSecretRef(ctx context.Context, input domainsecrets.Create) (domainsecrets.Ref, error) {
	if domainsecrets.ValidateCreate(input) != nil || input.Binding.Version != 1 {
		return domainsecrets.Ref{}, repository.ErrInvalidAutomation
	}
	result, err := transaction.tx.ExecContext(ctx,
		`INSERT INTO secret_refs
		 (owner_type, owner_id, field_name, provider, reference, has_value, created_at, updated_at, version)
		 VALUES (?, ?, ?, ?, ?, 1, ?, ?, 1)`,
		input.Binding.Owner.Type, input.Binding.Owner.ID, string(input.Binding.Kind), input.Provider, input.Reference,
		input.CreatedAt, input.CreatedAt)
	if err != nil {
		return domainsecrets.Ref{}, safeSQLError(ctx, err)
	}
	id, err := result.LastInsertId()
	if err != nil || id <= 0 {
		return domainsecrets.Ref{}, repository.ErrTransaction
	}
	if err := transaction.wrote("secret-refs:create"); err != nil {
		return domainsecrets.Ref{}, err
	}
	value, found, err := transaction.GetSecretRef(ctx, input.Binding.Kind, input.Binding.Owner)
	if err != nil || !found || value.ID != id {
		if err != nil {
			return domainsecrets.Ref{}, err
		}
		return domainsecrets.Ref{}, repository.ErrTransaction
	}
	return value, nil
}

// ReplaceSecretRef atomically switches the active opaque reference. The caller
// must provision the new provider value first and compensate it on failure.
func (transaction *writeTransaction) ReplaceSecretRef(ctx context.Context, input domainsecrets.Replace) (domainsecrets.Ref, error) {
	if domainsecrets.ValidateReplace(input) != nil || input.Binding.Version != input.ExpectedVersion+1 {
		return domainsecrets.Ref{}, repository.ErrInvalidAutomation
	}
	current, found, err := transaction.GetSecretRef(ctx, input.Binding.Kind, input.Binding.Owner)
	if err != nil {
		return domainsecrets.Ref{}, err
	}
	if !found {
		return domainsecrets.Ref{}, repository.ErrNotFound
	}
	if !current.HasValue || current.Version != input.ExpectedVersion {
		return domainsecrets.Ref{}, repository.ErrVersionConflict
	}
	var pending int
	if err := transaction.tx.QueryRowContext(ctx,
		"SELECT COUNT(1) FROM secret_refs WHERE owner_type = ? AND owner_id = ? AND field_name LIKE 'retired.%' AND has_value = 0",
		current.Binding.Owner.Type, current.Binding.Owner.ID).Scan(&pending); err != nil {
		return domainsecrets.Ref{}, safeSQLError(ctx, err)
	}
	if pending != 0 {
		return domainsecrets.Ref{}, repository.ErrDuplicate
	}
	result, err := transaction.tx.ExecContext(ctx,
		`UPDATE secret_refs SET provider = ?, reference = ?, has_value = 1, updated_at = ?, version = version + 1
		 WHERE owner_type = ? AND owner_id = ? AND field_name = ? AND version = ?`,
		input.Provider, input.Reference, input.UpdatedAt, input.Binding.Owner.Type, input.Binding.Owner.ID,
		string(input.Binding.Kind), input.ExpectedVersion)
	if err != nil {
		return domainsecrets.Ref{}, safeSQLError(ctx, err)
	}
	if err := requireOneRow(result); err != nil {
		if err == repository.ErrNotFound {
			return domainsecrets.Ref{}, repository.ErrVersionConflict
		}
		return domainsecrets.Ref{}, err
	}
	if err := transaction.wrote("secret-refs:replace"); err != nil {
		return domainsecrets.Ref{}, err
	}
	retirement := domainsecrets.RetirementKind(current.Binding)
	result, err = transaction.tx.ExecContext(ctx,
		`INSERT INTO secret_refs
		 (owner_type, owner_id, field_name, provider, reference, has_value, created_at, updated_at, version)
		 VALUES (?, ?, ?, ?, ?, 0, ?, ?, 1)`,
		current.Binding.Owner.Type, current.Binding.Owner.ID, string(retirement), current.Provider, current.Reference,
		input.UpdatedAt, input.UpdatedAt)
	if err != nil {
		return domainsecrets.Ref{}, safeSQLError(ctx, err)
	}
	if err := transaction.wrote("secret-refs:retire"); err != nil {
		return domainsecrets.Ref{}, err
	}
	updated, found, err := transaction.GetSecretRef(ctx, input.Binding.Kind, input.Binding.Owner)
	if err != nil || !found {
		if err != nil {
			return domainsecrets.Ref{}, err
		}
		return domainsecrets.Ref{}, repository.ErrTransaction
	}
	return updated, nil
}

// RetireSecretRef writes a durable non-active tombstone before the application
// deletes provider material. The value version is retained for AEAD binding.
func (transaction *writeTransaction) RetireSecretRef(ctx context.Context, input domainsecrets.Retire) (domainsecrets.Ref, error) {
	if domainsecrets.ValidateRetire(input) != nil {
		return domainsecrets.Ref{}, repository.ErrInvalidAutomation
	}
	current, found, err := transaction.GetSecretRef(ctx, input.Binding.Kind, input.Binding.Owner)
	if err != nil {
		return domainsecrets.Ref{}, err
	}
	if !found {
		return domainsecrets.Ref{}, repository.ErrNotFound
	}
	if current.Version != input.ExpectedVersion {
		return domainsecrets.Ref{}, repository.ErrVersionConflict
	}
	if !current.HasValue {
		return current, nil
	}
	result, err := transaction.tx.ExecContext(ctx,
		"UPDATE secret_refs SET has_value = 0, updated_at = ? WHERE owner_type = ? AND owner_id = ? AND field_name = ? AND version = ? AND has_value = 1",
		input.UpdatedAt, input.Binding.Owner.Type, input.Binding.Owner.ID, string(input.Binding.Kind), input.ExpectedVersion)
	if err != nil {
		return domainsecrets.Ref{}, safeSQLError(ctx, err)
	}
	if err := requireOneRow(result); err != nil {
		if err == repository.ErrNotFound {
			return domainsecrets.Ref{}, repository.ErrVersionConflict
		}
		return domainsecrets.Ref{}, err
	}
	if err := transaction.wrote("secret-refs:retire-delete"); err != nil {
		return domainsecrets.Ref{}, err
	}
	retired, found, err := transaction.GetSecretRef(ctx, input.Binding.Kind, input.Binding.Owner)
	if err != nil || !found {
		if err != nil {
			return domainsecrets.Ref{}, err
		}
		return domainsecrets.Ref{}, repository.ErrTransaction
	}
	return retired, nil
}

func (transaction *writeTransaction) PurgeSecretRef(ctx context.Context, input domainsecrets.Delete) error {
	if domainsecrets.ValidateDelete(input) != nil {
		return repository.ErrInvalidAutomation
	}
	result, err := transaction.tx.ExecContext(ctx,
		"DELETE FROM secret_refs WHERE owner_type = ? AND owner_id = ? AND field_name = ? AND version = ? AND has_value = 0",
		input.Binding.Owner.Type, input.Binding.Owner.ID, string(input.Binding.Kind), input.ExpectedVersion)
	if err != nil {
		return safeSQLError(ctx, err)
	}
	if err := requireOneRow(result); err != nil {
		if err == repository.ErrNotFound {
			return repository.ErrVersionConflict
		}
		return err
	}
	return transaction.wrote("secret-refs:purge")
}

func (transaction *writeTransaction) PurgeRetiredSecretRef(ctx context.Context, current domainsecrets.Ref) error {
	if domainsecrets.ValidateRef(current) != nil {
		return repository.ErrInvalidAutomation
	}
	retirement := domainsecrets.RetirementKind(current.Binding)
	result, err := transaction.tx.ExecContext(ctx,
		"DELETE FROM secret_refs WHERE owner_type = ? AND owner_id = ? AND field_name = ? AND version = 1 AND has_value = 0",
		current.Binding.Owner.Type, current.Binding.Owner.ID, string(retirement))
	if err != nil {
		return safeSQLError(ctx, err)
	}
	if err := requireOneRow(result); err != nil {
		if err == repository.ErrNotFound {
			return repository.ErrVersionConflict
		}
		return err
	}
	return transaction.wrote("secret-refs:purge-retired")
}

func scanSecretRef(row rowScanner) (domainsecrets.Ref, error) {
	var value domainsecrets.Ref
	var kind, ownerType string
	var hasValue int64
	if err := row.Scan(&value.ID, &ownerType, &value.Binding.Owner.ID, &kind, &value.Provider, &value.Reference,
		&hasValue, &value.CreatedAt, &value.UpdatedAt, &value.Version); err != nil {
		return domainsecrets.Ref{}, err
	}
	if hasValue != 0 && hasValue != 1 {
		return domainsecrets.Ref{}, repository.ErrInvalidStore
	}
	value.Binding.Owner.Type = ownerType
	value.Binding.Kind = domainsecrets.Kind(kind)
	value.Binding.Version = value.Version
	value.HasValue = hasValue == 1
	if domainsecrets.ValidateRef(value) != nil {
		return domainsecrets.Ref{}, repository.ErrInvalidStore
	}
	return value, nil
}
