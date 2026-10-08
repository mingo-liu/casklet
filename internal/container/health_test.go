package container

import (
	"testing"
	"time"

	"github.com/mingo-liu/casklet/internal/config"
)

func TestHealthGraceRetriesRecoveryAndHistory(t *testing.T) {
	start := time.Unix(1600000000, 0)
	grace := time.Minute
	retries := 2
	cfg := &config.HealthConfig{StartPeriod: &grace, Retries: &retries}
	health := Health{Status: HealthStarting}
	result := HealthResult{StartedAt: start.Add(time.Second), FinishedAt: start.Add(2 * time.Second), ExitCode: 1}
	health = health.next(cfg, start, result)
	if health.Status != HealthStarting || health.FailingStreak != 0 {
		t.Fatal(health)
	}
	result.ExitCode = 0
	health = health.next(cfg, start, result)
	if health.Status != HealthHealthy {
		t.Fatal(health)
	}
	result.ExitCode = 1
	health = health.next(cfg, start, result)
	if health.Status != HealthHealthy || health.FailingStreak != 1 {
		t.Fatal(health)
	}
	health = health.next(cfg, start, result)
	if health.Status != HealthUnhealthy || health.FailingStreak != 2 {
		t.Fatal(health)
	}
	result.ExitCode = 0
	health = health.next(cfg, start, result)
	if health.Status != HealthHealthy || health.FailingStreak != 0 {
		t.Fatal(health)
	}
	result.ExecutionFailed = true
	for i := 0; i < 100; i++ {
		health = health.next(cfg, start, result)
	}
	if health.Status != HealthUnhealthy || len(health.Checks) != 5 || health.FailingStreak != 2 || health.validate() != nil {
		t.Fatal(health)
	}
	// Crossing the start period counts failure even without a successful check.
	health = Health{Status: HealthStarting}
	result.ExecutionFailed = false
	result.ExitCode = 1
	result.StartedAt = start.Add(grace)
	result.FinishedAt = result.StartedAt
	health = health.next(cfg, start, result)
	if health.FailingStreak != 1 {
		t.Fatal(health)
	}
	if health.delay(cfg, start, start.Add(time.Second)) != 5*time.Second || health.delay(cfg, start, start.Add(grace)) != 30*time.Second {
		t.Fatal("wrong frequency")
	}
	record := Record{State: StateExited, Health: &health}
	if record.effectiveHealth().Status != HealthStopped || record.Health.Status != HealthStarting {
		t.Fatal("mutated saved health")
	}
}
