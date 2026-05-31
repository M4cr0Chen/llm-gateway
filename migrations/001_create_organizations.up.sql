CREATE TABLE organizations (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name TEXT NOT NULL,
    monthly_budget_usd DECIMAL(10, 2),
    total_budget_usd DECIMAL(10, 2),
    budget_alert_threshold DECIMAL(3, 2) DEFAULT 0.80,
    budget_action TEXT DEFAULT 'block',
    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW()
);
