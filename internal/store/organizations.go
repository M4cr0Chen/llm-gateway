package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Organization mirrors a row in the organizations table.
type Organization struct {
	ID                   string
	Name                 string
	MonthlyBudgetUSD     *float64
	TotalBudgetUSD       *float64
	BudgetAlertThreshold float64
	BudgetAction         string
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

// OrgStore provides CRUD access to organizations.
type OrgStore struct {
	pool *pgxpool.Pool
}

// NewOrgStore returns an OrgStore backed by the given pool.
func NewOrgStore(pool *pgxpool.Pool) *OrgStore {
	return &OrgStore{pool: pool}
}

// GetByID fetches a single organization by its UUID. Returns ErrNotFound if
// no row matches.
func (s *OrgStore) GetByID(ctx context.Context, id string) (*Organization, error) {
	const q = `
		SELECT id, name, monthly_budget_usd, total_budget_usd,
		       budget_alert_threshold, budget_action,
		       created_at, updated_at
		FROM organizations
		WHERE id = $1
	`
	row := s.pool.QueryRow(ctx, q, id)
	var o Organization
	if err := row.Scan(
		&o.ID, &o.Name, &o.MonthlyBudgetUSD, &o.TotalBudgetUSD,
		&o.BudgetAlertThreshold, &o.BudgetAction,
		&o.CreatedAt, &o.UpdatedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("scanning organization: %w", err)
	}
	return &o, nil
}
