package sqlite

import (
	"context"
	"database/sql"
	"strings"

	domainfiles "github.com/lyming99/autoplan/backend/internal/domain/files"
	"github.com/lyming99/autoplan/backend/internal/repository"
)

func (transaction *writeTransaction) CreateAttachment(
	ctx context.Context,
	value domainfiles.Attachment,
) (domainfiles.Attachment, error) {
	value.OwnerType = value.OwnerType.Canonical()
	if domainfiles.ValidateAttachment(value) != nil {
		return domainfiles.Attachment{}, repository.ErrInvalidIntake
	}
	if err := transaction.ensureAttachmentOwner(ctx, value.ProjectID, value.OwnerType, value.OwnerID); err != nil {
		return domainfiles.Attachment{}, err
	}
	result, err := transaction.tx.ExecContext(ctx,
		`INSERT INTO attachments
		 (project_id, owner_type, owner_id, original_name, stored_path, mime_type, size, hash, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		value.ProjectID, string(value.OwnerType), value.OwnerID, value.DisplayName, value.StoredKey,
		value.MIMEType, value.Size, value.SHA256, value.CreatedAt,
	)
	if err != nil {
		return domainfiles.Attachment{}, safeSQLError(ctx, err)
	}
	id, err := result.LastInsertId()
	if err != nil || id <= 0 {
		return domainfiles.Attachment{}, repository.ErrTransaction
	}
	if err := transaction.wrote("attachments:create"); err != nil {
		return domainfiles.Attachment{}, err
	}
	value.ID = id
	return value, nil
}

func (transaction *writeTransaction) GetAttachment(
	ctx context.Context,
	projectID int64,
	attachmentID int64,
) (domainfiles.Attachment, bool, error) {
	if projectID <= 0 || attachmentID <= 0 {
		return domainfiles.Attachment{}, false, nil
	}
	result, err := scanAttachment(transaction.tx.QueryRowContext(ctx,
		`SELECT id, project_id, owner_type, owner_id, original_name, stored_path,
		        COALESCE(mime_type, ''), size, hash, created_at
		   FROM attachments WHERE project_id = ? AND id = ?`, projectID, attachmentID))
	if err == sql.ErrNoRows {
		return domainfiles.Attachment{}, false, nil
	}
	if err != nil {
		return domainfiles.Attachment{}, false, safeSQLError(ctx, err)
	}
	return result, true, nil
}

func (transaction *writeTransaction) ListAttachmentsForOwner(
	ctx context.Context,
	projectID int64,
	ownerType domainfiles.AttachmentOwner,
	ownerID int64,
) ([]domainfiles.Attachment, error) {
	if projectID <= 0 || ownerID <= 0 || !ownerType.Valid() {
		return nil, repository.ErrInvalidIntake
	}
	ownerTypes := []string{string(ownerType.Canonical())}
	if ownerType.Canonical() == domainfiles.OwnerRequirement {
		ownerTypes = append(ownerTypes, string(domainfiles.OwnerRequirementLegacy))
	}
	query := `SELECT id, project_id, owner_type, owner_id, original_name, stored_path,
		 COALESCE(mime_type, ''), size, hash, created_at FROM attachments
		 WHERE project_id = ? AND owner_id = ? AND owner_type IN (` + placeholders(len(ownerTypes)) + `)
		 ORDER BY created_at DESC, id DESC`
	arguments := append([]any{projectID, ownerID}, stringsToAny(ownerTypes)...)
	return transaction.listAttachments(ctx, query, arguments...)
}

func (transaction *writeTransaction) ListProjectAttachments(
	ctx context.Context,
	projectID int64,
) ([]domainfiles.Attachment, error) {
	if projectID <= 0 {
		return nil, repository.ErrInvalidIntake
	}
	return transaction.listAttachments(ctx,
		`SELECT id, project_id, owner_type, owner_id, original_name, stored_path,
		 COALESCE(mime_type, ''), size, hash, created_at FROM attachments
		 WHERE project_id = ? ORDER BY created_at DESC, id DESC`, projectID)
}

func (transaction *writeTransaction) DeleteAttachment(
	ctx context.Context,
	projectID int64,
	attachmentID int64,
) (domainfiles.Attachment, error) {
	value, found, err := transaction.GetAttachment(ctx, projectID, attachmentID)
	if err != nil {
		return domainfiles.Attachment{}, err
	}
	if !found {
		return domainfiles.Attachment{}, repository.ErrNotFound
	}
	result, err := transaction.tx.ExecContext(ctx, "DELETE FROM attachments WHERE project_id = ? AND id = ?", projectID, attachmentID)
	if err != nil {
		return domainfiles.Attachment{}, safeSQLError(ctx, err)
	}
	if err := requireOneRow(result); err != nil {
		return domainfiles.Attachment{}, err
	}
	if err := transaction.wrote("attachments:delete"); err != nil {
		return domainfiles.Attachment{}, err
	}
	return value, nil
}

func (transaction *writeTransaction) UpdateAttachmentStorageKey(
	ctx context.Context,
	projectID int64,
	attachmentID int64,
	storedKey string,
) error {
	if projectID <= 0 || attachmentID <= 0 || !domainfiles.StorageKeyValid(storedKey) || !strings.HasPrefix(storedKey, "ready/") {
		return repository.ErrInvalidIntake
	}
	result, err := transaction.tx.ExecContext(ctx,
		"UPDATE attachments SET stored_path = ? WHERE project_id = ? AND id = ?", storedKey, projectID, attachmentID)
	if err != nil {
		return safeSQLError(ctx, err)
	}
	if err := requireOneRow(result); err != nil {
		return err
	}
	return transaction.wrote("attachments:update-storage-key")
}

func (transaction *writeTransaction) listAttachments(ctx context.Context, query string, arguments ...any) ([]domainfiles.Attachment, error) {
	rows, err := transaction.tx.QueryContext(ctx, query, arguments...)
	if err != nil {
		return nil, safeSQLError(ctx, err)
	}
	defer rows.Close()
	result := make([]domainfiles.Attachment, 0)
	for rows.Next() {
		value, scanErr := scanAttachment(rows)
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

func (transaction *writeTransaction) ensureAttachmentOwner(
	ctx context.Context,
	projectID int64,
	ownerType domainfiles.AttachmentOwner,
	ownerID int64,
) error {
	if projectID <= 0 || ownerID <= 0 || !ownerType.Valid() {
		return repository.ErrInvalidIntake
	}
	table := "requirements"
	if ownerType.Canonical() == domainfiles.OwnerFeedback {
		table = "feedback"
	}
	var ownerProjectID sql.NullInt64
	err := transaction.tx.QueryRowContext(ctx,
		"SELECT project_id FROM "+table+" WHERE id = ?", ownerID).Scan(&ownerProjectID)
	if err == sql.ErrNoRows {
		return repository.ErrNotFound
	}
	if err != nil {
		return safeSQLError(ctx, err)
	}
	if !ownerProjectID.Valid || ownerProjectID.Int64 != projectID {
		return repository.ErrProjectMismatch
	}
	return nil
}

func (transaction *writeTransaction) ValidateAttachmentOwner(
	ctx context.Context,
	projectID int64,
	ownerType domainfiles.AttachmentOwner,
	ownerID int64,
) error {
	return transaction.ensureAttachmentOwner(ctx, projectID, ownerType, ownerID)
}

func scanAttachment(row rowScanner) (domainfiles.Attachment, error) {
	var result domainfiles.Attachment
	var ownerType string
	if err := row.Scan(
		&result.ID, &result.ProjectID, &ownerType, &result.OwnerID, &result.DisplayName,
		&result.StoredKey, &result.MIMEType, &result.Size, &result.SHA256, &result.CreatedAt,
	); err != nil {
		return domainfiles.Attachment{}, err
	}
	result.OwnerType = domainfiles.AttachmentOwner(strings.ToLower(strings.TrimSpace(ownerType)))
	if result.MIMEType == "" {
		result.MIMEType = "application/octet-stream"
	}
	if result.ID <= 0 || result.ProjectID <= 0 || !result.OwnerType.Valid() || result.OwnerID <= 0 ||
		result.DisplayName == "" || result.Size < 0 || result.SHA256 == "" || result.CreatedAt == "" {
		return domainfiles.Attachment{}, repository.ErrInvalidStore
	}
	return result, nil
}
