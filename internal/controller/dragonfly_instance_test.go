package controller

import (
	"testing"
	"time"

	dfv1alpha1 "github.com/dragonflydb/dragonfly-operator/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func duration(d time.Duration) *metav1.Duration {
	return &metav1.Duration{Duration: d}
}

func TestReplicaCronForRank(t *testing.T) {
	tests := []struct {
		name            string
		baseCron        string
		staggerInterval *metav1.Duration
		rank            int
		want            string
	}{
		// No stagger — all replicas use base cron
		{"no stagger rank0", "0 * * * *", nil, 0, "0 * * * *"},
		{"no stagger rank1", "0 * * * *", nil, 1, "0 * * * *"},
		{"no stagger rank2", "15 * * * *", nil, 2, "15 * * * *"},

		// 30m stagger (common case: 2 replicas, one at :00, one at :30)
		{"30m stagger rank0", "0 * * * *", duration(30 * time.Minute), 0, "0 * * * *"},
		{"30m stagger rank1", "0 * * * *", duration(30 * time.Minute), 1, "30 * * * *"},

		// 20m stagger (3 replicas: :00, :20, :40)
		{"20m stagger rank0", "0 * * * *", duration(20 * time.Minute), 0, "0 * * * *"},
		{"20m stagger rank1", "0 * * * *", duration(20 * time.Minute), 1, "20 * * * *"},
		{"20m stagger rank2", "0 * * * *", duration(20 * time.Minute), 2, "40 * * * *"},

		// Base minute offset (start at :15)
		{"30m stagger base15 rank0", "15 * * * *", duration(30 * time.Minute), 0, "15 * * * *"},
		{"30m stagger base15 rank1", "15 * * * *", duration(30 * time.Minute), 1, "45 * * * *"},

		// Wrap around (rank 2 with 30m stagger from :00 -> :60 -> :0)
		{"30m stagger wrap rank2", "0 * * * *", duration(30 * time.Minute), 2, "0 * * * *"},

		// 15m stagger (4 replicas: :00, :15, :30, :45)
		{"15m stagger rank3", "0 * * * *", duration(15 * time.Minute), 3, "45 * * * *"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			snap := &dfv1alpha1.Snapshot{
				Cron:            tt.baseCron,
				StaggerInterval: tt.staggerInterval,
			}
			got := replicaCronForRank(snap, tt.rank)
			if got != tt.want {
				t.Errorf("replicaCronForRank(rank=%d) = %q, want %q", tt.rank, got, tt.want)
			}
		})
	}
}

func TestStaggerCron(t *testing.T) {
	tests := []struct {
		name     string
		baseCron string
		rank     int
		interval time.Duration
		want     string
	}{
		// Basic cases
		{"rank0", "0 * * * *", 0, 30 * time.Minute, "0 * * * *"},
		{"rank1", "0 * * * *", 1, 30 * time.Minute, "30 * * * *"},

		// Non-hourly crons (still only minute changes)
		{"daily cron", "0 3 * * *", 1, 30 * time.Minute, "30 3 * * *"},
		{"weekly cron", "0 0 * * 0", 1, 15 * time.Minute, "15 0 * * 0"},

		// Invalid cron format — returns as-is
		{"invalid short", "0 *", 1, 30 * time.Minute, "0 *"},
		{"non-numeric minute", "*/5 * * * *", 1, 30 * time.Minute, "*/5 * * * *"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := staggerCron(tt.baseCron, tt.rank, tt.interval)
			if got != tt.want {
				t.Errorf("staggerCron(%q, %d, %v) = %q, want %q",
					tt.baseCron, tt.rank, tt.interval, got, tt.want)
			}
		})
	}
}
