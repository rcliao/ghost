package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/rcliao/ghost/internal/store"
	"github.com/spf13/cobra"
)

// pairRulesStore is the slice of the SQLite store the rules command needs. It
// is asserted at run time rather than added to store.Store, so the Store
// interface (a human-approval surface) is unchanged while this is experimental.
type pairRulesStore interface {
	ListPairRules(ctx context.Context, ns string) ([]store.PairRule, error)
	RunPairRules(ctx context.Context, p store.RunPairRulesParams) (*store.RunPairRulesResult, error)
	ListRuleEvents(ctx context.Context, p store.ListRuleEventsParams) ([]store.RuleEvent, error)
	ReviewRuleEvent(ctx context.Context, id, verdict, by string) error
}

func rulesStore() (pairRulesStore, error) {
	rs, ok := st.(pairRulesStore)
	if !ok {
		return nil, fmt.Errorf("this store backend does not support pair rules")
	}
	return rs, nil
}

// ghost rules — relationship logic as configuration, with an audit trace.
//
//	ghost rules list   --ns X
//	ghost rules pairs  --ns X [--max-pairs N] [--dry-run] [--rule ID]
//	ghost rules events --ns X [--unreviewed] [--rule ID] [--limit N]
//	ghost rules review <event-id> --verdict agree|disagree [--by who]
func init() {
	rulesCmd := &cobra.Command{
		Use:   "rules",
		Short: "Pair rules: deterministic relationship candidates with a reviewable trace",
		Long: `Pair rules evaluate deterministic features of memory pairs (shared entities,
time apart, overlap, correction cues) and either PROPOSE a relation for a caller
to decide or ASSERT one. Every firing is recorded in rule_events and can be
reviewed; disagreeing with an asserted edge removes it. No model is called.`,
	}

	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List pair rules (global plus the namespace's own)",
		RunE: func(cmd *cobra.Command, args []string) error {
			rs, err := rulesStore()
			if err != nil {
				return err
			}
			ns, _ := cmd.Flags().GetString("ns")
			rules, err := rs.ListPairRules(cmd.Context(), ns)
			if err != nil {
				return err
			}
			if formatFlag == "text" {
				for _, r := range rules {
					state := "on "
					if !r.Enabled {
						state = "off"
					}
					fmt.Fprintf(cmd.OutOrStdout(), "%s [%3d] %s %s %s — %s\n", state, r.Priority, r.ID, r.Action.Op, r.Action.Rel, r.Name)
				}
				return nil
			}
			outputJSON(cmd, rules)
			return nil
		},
	}
	listCmd.Flags().StringP("ns", "n", "", "Namespace (empty = all rules)")

	pairsCmd := &cobra.Command{
		Use:   "pairs",
		Short: "Run pair rules over a namespace; record firings (or preview with --dry-run)",
		RunE: func(cmd *cobra.Command, args []string) error {
			rs, err := rulesStore()
			if err != nil {
				return err
			}
			ns, _ := cmd.Flags().GetString("ns")
			maxPairs, _ := cmd.Flags().GetInt("max-pairs")
			dry, _ := cmd.Flags().GetBool("dry-run")
			ruleIDs, _ := cmd.Flags().GetStringSlice("rule")
			res, err := rs.RunPairRules(cmd.Context(), store.RunPairRulesParams{NS: ns, MaxPairs: maxPairs, DryRun: dry, RuleIDs: ruleIDs})
			if err != nil {
				return err
			}
			if formatFlag == "text" {
				prefix := ""
				if dry {
					prefix = "(dry-run) "
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%sscanned %d memories, evaluated %d pairs, %d firings, %d skipped\n",
					prefix, res.MemoriesScanned, res.PairsEvaluated, len(res.Firings), res.Skipped)
				for _, f := range res.Firings {
					mark := " "
					if f.EdgeWritten {
						mark = "+"
					}
					ents := make([]string, 0, len(f.Features.SharedEntities))
					for _, e := range f.Features.SharedEntities {
						ents = append(ents, e.Text)
					}
					fmt.Fprintf(cmd.OutOrStdout(), "  %s %-7s %-9s %s -> %s  [%s] %.0fd shared=%s cue=%q\n",
						mark, f.Op, f.Rel, f.Features.OlderKey, f.Features.NewerKey, f.RuleID, f.Features.DaysApart, strings.Join(ents, ","), f.Features.NewerCue)
				}
				return nil
			}
			outputJSON(cmd, res)
			return nil
		},
	}
	pairsCmd.Flags().StringP("ns", "n", "", "Namespace (required)")
	pairsCmd.Flags().Int("max-pairs", 500, "Candidate pairs to evaluate")
	pairsCmd.Flags().Bool("dry-run", false, "Evaluate and report; write nothing")
	pairsCmd.Flags().StringSlice("rule", nil, "Only these rule ids")
	pairsCmd.MarkFlagRequired("ns")

	eventsCmd := &cobra.Command{
		Use:   "events",
		Short: "Show recorded rule firings, newest first",
		RunE: func(cmd *cobra.Command, args []string) error {
			rs, err := rulesStore()
			if err != nil {
				return err
			}
			ns, _ := cmd.Flags().GetString("ns")
			unrev, _ := cmd.Flags().GetBool("unreviewed")
			rule, _ := cmd.Flags().GetString("rule")
			limit, _ := cmd.Flags().GetInt("limit")
			events, err := rs.ListRuleEvents(cmd.Context(), store.ListRuleEventsParams{NS: ns, RuleID: rule, Unreviewed: unrev, Limit: limit})
			if err != nil {
				return err
			}
			if formatFlag == "text" {
				for _, e := range events {
					status := "unreviewed"
					if e.Reviewed {
						status = e.Verdict + " by " + e.ReviewedBy
					}
					edge := ""
					if e.EdgeWritten {
						edge = " +edge"
					}
					fmt.Fprintf(cmd.OutOrStdout(), "%s  %s %s %s%s  %s -> %s  [%s]  %s\n",
						e.ID, e.CreatedAt[:10], e.ActionOp, e.ActionRel, edge, e.FromKey, e.ToKey, e.RuleID, status)
				}
				return nil
			}
			outputJSON(cmd, events)
			return nil
		},
	}
	eventsCmd.Flags().StringP("ns", "n", "", "Namespace")
	eventsCmd.Flags().Bool("unreviewed", false, "Only events without a verdict")
	eventsCmd.Flags().String("rule", "", "Only this rule id")
	eventsCmd.Flags().Int("limit", 100, "Max events")

	reviewCmd := &cobra.Command{
		Use:   "review <event-id>",
		Short: "Record agree or disagree on a firing; disagree removes an asserted edge",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rs, err := rulesStore()
			if err != nil {
				return err
			}
			verdict, _ := cmd.Flags().GetString("verdict")
			by, _ := cmd.Flags().GetString("by")
			if err := rs.ReviewRuleEvent(cmd.Context(), args[0], verdict, by); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "recorded %s on %s\n", verdict, args[0])
			return nil
		},
	}
	reviewCmd.Flags().String("verdict", "", "agree | disagree (required)")
	reviewCmd.Flags().String("by", "cli", "Who is reviewing")
	reviewCmd.MarkFlagRequired("verdict")

	rulesCmd.AddCommand(listCmd, pairsCmd, eventsCmd, reviewCmd)
	RootCmd.AddCommand(rulesCmd)
}
