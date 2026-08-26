package persistence

import (
	"context"
	"database/sql"
	"errors"
	"os"

	"github.com/GZ-Alinx/awsinfra/internal/statebackend"
)

func (s *Services) GetTerraformStateBackend(ctx context.Context) (statebackend.StoredConfig, error) {
	var record statebackend.StoredConfig
	err := s.db.QueryRowContext(ctx, `SELECT enabled, bucket_name, region_name, key_prefix, kms_key_id,
		access_key_id, secret_access_key_cipher, session_token_cipher, account_id, principal_arn,
		principal_user_id, updated_by, verified_at, updated_at
		FROM terraform_state_backend WHERE config_key = 'default'`).Scan(
		&record.Enabled, &record.Bucket, &record.Region, &record.KeyPrefix, &record.KMSKeyID,
		&record.AccessKeyID, &record.SecretAccessKeyCipher, &record.SessionTokenCipher, &record.AccountID,
		&record.PrincipalARN, &record.PrincipalUserID, &record.UpdatedBy, &record.VerifiedAt, &record.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return statebackend.StoredConfig{}, os.ErrNotExist
	}
	return record, err
}

func (s *Services) SaveTerraformStateBackend(ctx context.Context, record statebackend.StoredConfig) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO terraform_state_backend (
		config_key, enabled, bucket_name, region_name, key_prefix, kms_key_id, access_key_id,
		secret_access_key_cipher, session_token_cipher, account_id, principal_arn, principal_user_id,
		updated_by, verified_at
	) VALUES ('default', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON DUPLICATE KEY UPDATE enabled=VALUES(enabled), bucket_name=VALUES(bucket_name),
		region_name=VALUES(region_name), key_prefix=VALUES(key_prefix), kms_key_id=VALUES(kms_key_id),
		access_key_id=VALUES(access_key_id), secret_access_key_cipher=VALUES(secret_access_key_cipher),
		session_token_cipher=VALUES(session_token_cipher), account_id=VALUES(account_id),
		principal_arn=VALUES(principal_arn), principal_user_id=VALUES(principal_user_id),
		updated_by=VALUES(updated_by), verified_at=VALUES(verified_at)`,
		record.Enabled, record.Bucket, record.Region, record.KeyPrefix, record.KMSKeyID, record.AccessKeyID,
		record.SecretAccessKeyCipher, record.SessionTokenCipher, record.AccountID, record.PrincipalARN,
		record.PrincipalUserID, record.UpdatedBy, record.VerifiedAt,
	)
	return err
}
