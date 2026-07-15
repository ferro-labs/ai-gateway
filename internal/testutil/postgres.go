// Package testutil provides shared test helpers.
package testutil

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"time"

	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

// PostgresContainer holds the running container and its DSN.
type PostgresContainer struct {
	DSN       string
	container *postgres.PostgresContainer
}

// StartPostgres starts a Postgres 16 container and returns its DSN.
// Returns an error if Docker is not available or the container fails to start.
func StartPostgres(ctx context.Context) (pg *PostgresContainer, err error) {
	if _, lookErr := exec.LookPath("docker"); lookErr != nil {
		return nil, fmt.Errorf("docker not found in PATH: %w", lookErr)
	}

	var container *postgres.PostgresContainer
	defer func() {
		if r := recover(); r != nil {
			panicErr := fmt.Errorf("testcontainers panic: %v", r)
			if container == nil {
				err = panicErr
				return
			}
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			err = errors.Join(panicErr, container.Terminate(cleanupCtx))
		}
	}()

	container, err = postgres.Run(ctx,
		"postgres:16-alpine",
		postgres.WithDatabase("ferrogw_test"),
		postgres.WithUsername("test"),
		postgres.WithPassword("test"),
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		if container != nil {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			cleanupErr := container.Terminate(cleanupCtx)
			cancel()
			return nil, errors.Join(fmt.Errorf("start postgres container: %w", err), cleanupErr)
		}
		return nil, fmt.Errorf("start postgres container: %w", err)
	}

	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cleanupErr := container.Terminate(cleanupCtx)
		return nil, errors.Join(fmt.Errorf("get connection string: %w", err), cleanupErr)
	}

	return &PostgresContainer{DSN: dsn, container: container}, nil
}

// Terminate stops and removes the container.
func (c *PostgresContainer) Terminate(ctx context.Context) error {
	if c == nil || c.container == nil {
		return nil
	}
	return c.container.Terminate(ctx)
}
