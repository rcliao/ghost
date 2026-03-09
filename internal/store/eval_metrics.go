package store

import "time"

// ── Metric functions ────────────────────────────────────────────────

// PrecisionAtK computes the fraction of retrieved items (up to k) that are relevant.
func PrecisionAtK(retrieved, relevant []string, k int) float64 {
	if k <= 0 || len(retrieved) == 0 {
		return 0
	}
	relSet := make(map[string]bool, len(relevant))
	for _, r := range relevant {
		relSet[r] = true
	}
	n := k
	if n > len(retrieved) {
		n = len(retrieved)
	}
	hits := 0
	for _, r := range retrieved[:n] {
		if relSet[r] {
			hits++
		}
	}
	return float64(hits) / float64(n)
}

// RecallAtK computes the fraction of relevant items found in the top-k retrieved.
func RecallAtK(retrieved, relevant []string, k int) float64 {
	if len(relevant) == 0 {
		return 0
	}
	relSet := make(map[string]bool, len(relevant))
	for _, r := range relevant {
		relSet[r] = true
	}
	n := k
	if n > len(retrieved) {
		n = len(retrieved)
	}
	hits := 0
	for _, r := range retrieved[:n] {
		if relSet[r] {
			hits++
		}
	}
	return float64(hits) / float64(len(relevant))
}

// MRR computes Mean Reciprocal Rank: 1/(rank of first relevant result).
func MRR(retrieved []string, relevant map[string]bool) float64 {
	for i, r := range retrieved {
		if relevant[r] {
			return 1.0 / float64(i+1)
		}
	}
	return 0
}

// ── Report types ────────────────────────────────────────────────────

// EvalReport is the top-level output of a full eval run.
type EvalReport struct {
	Timestamp time.Time        `json:"timestamp"`
	EmbedMode string           `json:"embed_mode"`
	Summary   EvalSummary      `json:"summary"`
	Scenarios []ScenarioResult `json:"scenarios"`
}

// EvalSummary aggregates pass/fail counts and mean metrics.
type EvalSummary struct {
	Total           int     `json:"total"`
	Passed          int     `json:"passed"`
	Failed          int     `json:"failed"`
	MeanMRR         float64 `json:"mean_mrr"`
	MeanRecall      float64 `json:"mean_recall"`
	MeanBudgetUtil  float64 `json:"mean_budget_util"`
	ReflectAccuracy float64 `json:"reflect_accuracy"`
}

// ScenarioResult captures one eval scenario's outcome.
type ScenarioResult struct {
	Name     string             `json:"name"`
	Category string             `json:"category"`
	Pass     bool               `json:"pass"`
	Metrics  map[string]float64 `json:"metrics"`
	Errors   []string           `json:"errors,omitempty"`
}
