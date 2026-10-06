package store

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestIsUniqueViolation(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil 错误", nil, false},
		{
			name: "PG 类型化错误 23505",
			err:  &pgconn.PgError{Code: "23505"},
			want: true,
		},
		{
			name: "PG 其他错误码不算",
			err:  &pgconn.PgError{Code: "23503"}, // foreign_key_violation
			want: false,
		},
		{
			name: "PG 错误被包装后仍应识别",
			err:  fmt.Errorf("创建用户失败: %w", &pgconn.PgError{Code: "23505"}),
			want: true,
		},
		{
			name: "SQLite 文案",
			err:  errors.New("UNIQUE constraint failed: users.username"),
			want: true,
		},
		{
			name: "PG 文案（驱动未包装时）",
			err:  errors.New(`ERROR: duplicate key value violates unique constraint "users_pkey"`),
			want: true,
		},
		{"无关错误", errors.New("connection refused"), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isUniqueViolation(tc.err); got != tc.want {
				t.Errorf("isUniqueViolation(%v) = %v，期望 %v", tc.err, got, tc.want)
			}
		})
	}
}
