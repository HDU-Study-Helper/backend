package dbbridge

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	"academic/backend/internal/model"
	"github.com/jackc/pgx/v5"
)

type users struct{ db *DB }

var _ model.UserRepository = (*users)(nil)

const userColumns = `id, username, display_name, password_hash, role, disabled, created_at, updated_at`

func scanUser(row pgx.Row) (model.User, error) {
	var u model.User
	err := row.Scan(&u.ID, &u.Username, &u.DisplayName, &u.PasswordHash, &u.Role, &u.Disabled, &u.CreatedAt, &u.UpdatedAt)
	return u, storageError(err)
}

func validUserText(s string, max int, required bool) bool {
	return utf8.ValidString(s) && !strings.ContainsRune(s, 0) && len(s) <= max && (!required || strings.TrimSpace(s) != "")
}

func validUserID(id string) bool { return validUserText(id, 128, true) }

func validUser(u model.User) bool {
	return validUserID(u.ID) && validUserText(u.Username, 128, true) &&
		validUserText(u.DisplayName, 256, false) && validUserText(u.PasswordHash, 1024, true) &&
		(u.Role == model.RoleAdmin || u.Role == model.RoleUser)
}

// Every writer takes the same transaction lock before inspecting admin state.
// Row locks additionally serialize session creation with account changes.
func lockUsers(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(730022401)`)
	return storageError(err)
}

func insertUser(ctx context.Context, tx pgx.Tx, u model.User) (model.User, error) {
	return scanUser(tx.QueryRow(ctx, `INSERT INTO users
		(id, username, display_name, password_hash, role, disabled, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,clock_timestamp(),clock_timestamp()) RETURNING `+userColumns,
		u.ID, u.Username, u.DisplayName, u.PasswordHash, u.Role, u.Disabled))
}

func (r *users) Create(ctx context.Context, u model.User) (model.User, error) {
	if u.ID == "" {
		u.ID = newID()
	}
	if !validUser(u) {
		return model.User{}, model.ErrInvalidArgument
	}
	var result model.User
	err := r.db.transaction(ctx, func(tx pgx.Tx) error {
		if err := lockUsers(ctx, tx); err != nil {
			return err
		}
		var err error
		result, err = r.insertWithRoles(ctx, tx, u)
		return err
	})
	if err != nil {
		return model.User{}, err
	}
	return result, nil
}

func (r *users) GetByID(ctx context.Context, id string) (model.User, error) {
	if !validUserID(id) {
		return model.User{}, model.ErrInvalidArgument
	}
	return scanUser(r.db.pool.QueryRow(ctx, `SELECT `+userColumns+` FROM users WHERE id=$1`, id))
}

func (r *users) GetByUsername(ctx context.Context, username string) (model.User, error) {
	if !validUserText(username, 128, true) {
		return model.User{}, model.ErrInvalidArgument
	}
	return scanUser(r.db.pool.QueryRow(ctx, `SELECT `+userColumns+` FROM users WHERE username=$1`, username))
}

func (r *users) List(ctx context.Context, limit int, after string) ([]model.User, error) {
	if limit == 0 {
		limit = 20
	}
	if limit < 1 || limit > 101 || (after != "" && !validUserID(after)) {
		return nil, model.ErrInvalidArgument
	}
	rows, err := r.db.pool.Query(ctx, `SELECT `+userColumns+` FROM users WHERE id > $1 ORDER BY id LIMIT $2`, after, limit)
	if err != nil {
		return nil, storageError(err)
	}
	defer rows.Close()
	result := make([]model.User, 0)
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, u)
	}
	if err := rows.Err(); err != nil {
		return nil, storageError(err)
	}
	return result, nil
}

func protectLastAdmin(ctx context.Context, tx pgx.Tx, u model.User) error {
	if u.Role != model.RoleAdmin || u.Disabled {
		return nil
	}
	var other bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM users WHERE role='admin' AND NOT disabled AND id<>$1)`, u.ID).Scan(&other); err != nil {
		return storageError(err)
	}
	if !other {
		return model.ErrLastAdmin
	}
	return nil
}

func (r *users) Update(ctx context.Context, id string, update model.UserUpdate) (model.User, error) {
	if !validUserID(id) ||
		(update.DisplayName != nil && !validUserText(*update.DisplayName, 256, false)) ||
		(update.PasswordHash != nil && !validUserText(*update.PasswordHash, 1024, true)) ||
		(update.Role != nil && *update.Role != model.RoleAdmin && *update.Role != model.RoleUser) {
		return model.User{}, model.ErrInvalidArgument
	}
	var result model.User
	err := r.db.transaction(ctx, func(tx pgx.Tx) error {
		if err := lockUsers(ctx, tx); err != nil {
			return err
		}
		old, err := scanUser(tx.QueryRow(ctx, `SELECT `+userColumns+` FROM users WHERE id=$1 FOR UPDATE`, id))
		if err != nil {
			return err
		}
		u := old
		if update.DisplayName != nil {
			u.DisplayName = *update.DisplayName
		}
		if update.PasswordHash != nil {
			u.PasswordHash = *update.PasswordHash
		}
		if update.Role != nil {
			u.Role = *update.Role
		}
		if update.Disabled != nil {
			u.Disabled = *update.Disabled
		}
		if u.Role != model.RoleAdmin || u.Disabled {
			if err := protectLastAdmin(ctx, tx, old); err != nil {
				return err
			}
		}
		result, err = scanUser(tx.QueryRow(ctx, `UPDATE users SET display_name=$2,password_hash=$3,role=$4,disabled=$5,updated_at=clock_timestamp() WHERE id=$1 RETURNING `+userColumns,
			id, u.DisplayName, u.PasswordHash, u.Role, u.Disabled))
		if err != nil {
			return err
		}
		if update.Disabled != nil && *update.Disabled {
			if _, err = tx.Exec(ctx, `UPDATE platform_connections SET lease_token=nextval('platform_fencing_seq'),lease_until='epoch' WHERE user_id=$1`, id); err != nil {
				return storageError(err)
			}
			if _, err = tx.Exec(ctx, `UPDATE dws_leases SET token=nextval('dws_fencing_seq'),expires_at=clock_timestamp() WHERE user_id=$1`, id); err != nil {
				return storageError(err)
			}
			if _, err = tx.Exec(ctx, `UPDATE dws_jobs SET state='cancelled',updated_at=clock_timestamp() WHERE user_id=$1 AND state IN ('queued','running')`, id); err != nil {
				return storageError(err)
			}
		}
		if update.PasswordHash != nil || update.Role != nil || update.Disabled != nil {
			_, err = tx.Exec(ctx, `DELETE FROM sessions WHERE user_id=$1`, id)
		}
		return storageError(err)
	})
	if err != nil {
		return model.User{}, err
	}
	return result, nil
}

func (r *users) Delete(ctx context.Context, id string) error {
	if !validUserID(id) {
		return model.ErrInvalidArgument
	}
	return r.db.transaction(ctx, func(tx pgx.Tx) error {
		if err := lockUsers(ctx, tx); err != nil {
			return err
		}
		u, err := scanUser(tx.QueryRow(ctx, `SELECT `+userColumns+` FROM users WHERE id=$1 FOR UPDATE`, id))
		if err != nil {
			return err
		}
		if err := protectLastAdmin(ctx, tx, u); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `DELETE FROM users WHERE id=$1`, id)
		return storageError(err)
	})
}

func (r *users) BootstrapAdmin(ctx context.Context, u model.User) (model.User, bool, error) {
	u.Role = model.RoleAdmin
	u.Disabled = false
	if u.ID == "" {
		u.ID = newID()
	}
	if !validUser(u) {
		return model.User{}, false, model.ErrInvalidArgument
	}
	var result model.User
	created := false
	err := r.db.transaction(ctx, func(tx pgx.Tx) error {
		if err := lockUsers(ctx, tx); err != nil {
			return err
		}
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM users)`).Scan(&exists); err != nil {
			return storageError(err)
		}
		var err error
		if exists {
			result, err = scanUser(tx.QueryRow(ctx, `SELECT `+userColumns+` FROM users WHERE role='admin' AND NOT disabled ORDER BY id LIMIT 1`))
			if err == model.ErrNotFound {
				return model.ErrConflict
			}
			return err
		}
		result, err = r.insertWithRoles(ctx, tx, u)
		created = err == nil
		return err
	})
	if err != nil {
		return model.User{}, false, err
	}
	return result, created, nil
}

func (r *users) CreateSession(ctx context.Context, s model.Session) error {
	if len(s.Hash) != 32 || !validUserID(s.UserID) || s.ExpiresAt.IsZero() || !s.ExpiresAt.After(time.Now()) {
		return model.ErrInvalidArgument
	}
	return r.db.transaction(ctx, func(tx pgx.Tx) error {
		var disabled bool
		var passwordHash string
		if err := tx.QueryRow(ctx, `SELECT disabled,password_hash FROM users WHERE id=$1 FOR UPDATE`, s.UserID).Scan(&disabled, &passwordHash); err != nil {
			return storageError(err)
		}
		if disabled || (s.ExpectedPasswordHash != "" && s.ExpectedPasswordHash != passwordHash) {
			return model.ErrUnauthenticated
		}
		_, err := tx.Exec(ctx, `INSERT INTO sessions (hash,user_id,expires_at,created_at) VALUES ($1,$2,$3,clock_timestamp())`, s.Hash, s.UserID, s.ExpiresAt)
		return storageError(err)
	})
}

func (r *users) GetSessionUser(ctx context.Context, hash []byte) (model.User, error) {
	if len(hash) != 32 {
		return model.User{}, model.ErrInvalidArgument
	}
	return scanUser(r.db.pool.QueryRow(ctx, `SELECT u.id,u.username,u.display_name,u.password_hash,u.role,u.disabled,u.created_at,u.updated_at
		FROM sessions s JOIN users u ON u.id=s.user_id
		WHERE s.hash=$1 AND s.expires_at>clock_timestamp() AND NOT u.disabled`, hash))
}

func (r *users) DeleteSession(ctx context.Context, hash []byte) error {
	if len(hash) != 32 {
		return model.ErrInvalidArgument
	}
	_, err := r.db.pool.Exec(ctx, `DELETE FROM sessions WHERE hash=$1`, hash)
	return storageError(err)
}

func (r *users) RevokeUserSessions(ctx context.Context, id string) error {
	if !validUserID(id) {
		return model.ErrInvalidArgument
	}
	return r.db.transaction(ctx, func(tx pgx.Tx) error {
		// Match session creation's row lock so concurrent revocation has a
		// definite order relative to a newly issued session.
		var found string
		if err := tx.QueryRow(ctx, `SELECT id FROM users WHERE id=$1 FOR UPDATE`, id).Scan(&found); err != nil {
			return storageError(err)
		}
		_, err := tx.Exec(ctx, `DELETE FROM sessions WHERE user_id=$1`, id)
		return storageError(err)
	})
}
