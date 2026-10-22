package db

import "context"

// ExportLeaderTerminate kills l's connection from a second one, as a network drop would.
func ExportLeaderTerminate(ctx context.Context, l *Leader) (bool, error) {
	l.mu.Lock()
	pid := l.conn.PgConn().PID()
	l.mu.Unlock()
	pool, err := NewPool(ctx, l.dsn)
	if err != nil {
		return false, err
	}
	defer pool.Close()
	var ok bool
	err = pool.QueryRow(ctx, `SELECT pg_terminate_backend($1)`, pid).Scan(&ok)
	return ok, err
}
