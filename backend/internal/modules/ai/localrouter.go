// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import (
	"context"
	"sync"
)

// NewUnmeteredRouter builds a Router over an in-memory meter and the
// static single-seat budget — the DB-less path for dev tooling (the
// worker's siteread debug subcommand). Calls ride the full routing,
// budget-band, retry and secret-stripping pipeline; only the spend
// record lives in process memory instead of ai_usage.
func NewUnmeteredRouter(cfg RoutingConfig) (*Router, error) {
	clients, embedder, err := cfg.buildClients()
	if err != nil {
		return nil, err
	}
	return newRouter(clients, embedder, cfg.Profile, &memoryMeter{}, DefaultMonthlyTokens), nil
}

// memoryMeter accumulates spend for the life of one process: enough for
// the budget bands to behave honestly during a debug run, gone at exit.
type memoryMeter struct {
	mu     sync.Mutex
	tokens int64
}

func (m *memoryMeter) Record(_ context.Context, u Usage) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tokens += int64(u.TokensIn + u.TokensOut)
	return nil
}

func (m *memoryMeter) MonthTokens(context.Context) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.tokens, nil
}
