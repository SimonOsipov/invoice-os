package gateway

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PGHandoffStore is the Handoffs every gateway instance shares, in Postgres as invoice_gateway.
type PGHandoffStore struct {
	pool *pgxpool.Pool
	ttl  time.Duration
	log  *slog.Logger
}

func NewPGHandoffStore(pool *pgxpool.Pool, ttl time.Duration, log *slog.Logger) *PGHandoffStore {
	return &PGHandoffStore{pool: pool, ttl: ttl, log: log}
}

// handoffDBTimeout bounds one store call; the handlers carry no context into Put and Take.
const handoffDBTimeout = 5 * time.Second

func (s *PGHandoffStore) Put(answer string, stateHash [32]byte) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), handoffDBTimeout)
	defer cancel()
	code := newCode()
	ok, err := putHandoff(ctx, s.pool, sha256.Sum256([]byte(code)), stateHash, answer, s.ttl, HandoffMaxLive)
	if err != nil {
		s.log.Error("hand-off: store code", "error", err)
		return "", false
	}
	if !ok {
		return "", false
	}
	return code, true
}

func (s *PGHandoffStore) Take(code, state string) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), handoffDBTimeout)
	defer cancel()
	answer, stateHash, ok, err := takeHandoff(ctx, s.pool, sha256.Sum256([]byte(code)))
	if err != nil {
		s.log.Error("hand-off: redeem code", "error", err)
		return "", false
	}
	if !ok {
		return "", false
	}
	sh := sha256.Sum256([]byte(state))
	if subtle.ConstantTimeCompare(sh[:], stateHash[:]) != 1 {
		return "", false
	}
	return answer, true
}

// putHandoff stores one code as invoice_gateway, sweeping expired rows first; false means maxLive live codes exist.
// ceiling: concurrent puts on several instances can overshoot maxLive by the instance count.
func putHandoff(ctx context.Context, pool *pgxpool.Pool, codeHash, stateHash [32]byte, answer string, ttl time.Duration, maxLive int) (bool, error) {
	tag, err := pool.Exec(ctx, `
WITH swept AS (DELETE FROM gateway_handoff_codes WHERE expires_at <= now()),
     live  AS (SELECT count(*) AS n FROM gateway_handoff_codes WHERE expires_at > now())
INSERT INTO gateway_handoff_codes (code_hash, state_hash, answer, expires_at)
SELECT $1, $2, $3, now() + $4 * interval '1 microsecond' FROM live WHERE n < $5`,
		codeHash[:], stateHash[:], answer, ttl.Microseconds(), maxLive)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// takeHandoff deletes the code and returns what it held; ok is false for an unknown or expired code.
// The delete is the redemption, so a code is single-use across instances.
func takeHandoff(ctx context.Context, pool *pgxpool.Pool, codeHash [32]byte) (answer string, stateHash [32]byte, ok bool, err error) {
	var sh []byte
	var live bool
	err = pool.QueryRow(ctx, `
DELETE FROM gateway_handoff_codes WHERE code_hash = $1
RETURNING state_hash, answer, expires_at > now()`, codeHash[:]).Scan(&sh, &answer, &live)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", stateHash, false, nil
	}
	if err != nil || !live || len(sh) != len(stateHash) {
		return "", stateHash, false, err
	}
	copy(stateHash[:], sh)
	return answer, stateHash, true, nil
}
