package cli

import (
	"encoding/json"
	"fmt"

	"github.com/rcliao/ghost/internal/store"
	"github.com/spf13/cobra"
)

func init() {
	RootCmd.AddCommand(reflectCmd)
	reflectCmd.Flags().String("ns", "", "Namespace filter")
	reflectCmd.Flags().Bool("dry-run", false, "Show what would happen without applying changes")

	RootCmd.AddCommand(ruleCmd)
	ruleCmd.AddCommand(ruleSetCmd)
	ruleCmd.AddCommand(ruleListCmd)
	ruleCmd.AddCommand(ruleDeleteCmd)
	ruleCmd.AddCommand(ruleGetCmd)

	ruleSetCmd.Flags().String("id", "", "Rule ID (auto-generated if empty)")
	ruleSetCmd.Flags().String("name", "", "Rule name (required)")
	ruleSetCmd.Flags().String("ns", "", "Namespace scope (empty = all)")
	ruleSetCmd.Flags().Int("priority", 50, "Priority (higher runs first)")
	ruleSetCmd.Flags().String("cond-tier", "", "Match memories in this tier")
	ruleSetCmd.Flags().Float64("cond-age-gt", 0, "Match memories older than N hours")
	ruleSetCmd.Flags().Float64("cond-importance-lt", 0, "Match memories with importance below threshold")
	ruleSetCmd.Flags().Int("cond-access-lt", 0, "Match memories with fewer than N accesses")
	ruleSetCmd.Flags().Int("cond-access-gt", 0, "Match memories with more than N accesses")
	ruleSetCmd.Flags().Float64("cond-utility-lt", 0, "Match memories with utility ratio below threshold")
	ruleSetCmd.Flags().String("cond-kind", "", "Match memory kind")
	ruleSetCmd.Flags().String("cond-tag", "", "Match memories with this tag")
	ruleSetCmd.Flags().Float64("cond-similarity-gt", 0, "Match memories with embedding similarity above threshold (0.0-1.0, used with MERGE action)")
	ruleSetCmd.Flags().String("action", "", "Action: DECAY, DELETE, PROMOTE, DEMOTE, ARCHIVE, TTL_SET, PIN, MERGE")
	ruleSetCmd.Flags().String("action-params", "", "Action params as JSON (e.g. '{\"factor\":0.9}')")

	ruleListCmd.Flags().String("ns", "", "Namespace filter")
}

var reflectCmd = &cobra.Command{
	Use:   "reflect",
	Short: "Run the reflect cycle to evaluate and apply rules to memories",
	RunE: func(cmd *cobra.Command, args []string) error {
		ns, _ := cmd.Flags().GetString("ns")
		dryRun, _ := cmd.Flags().GetBool("dry-run")

		result, err := st.Reflect(cmd.Context(), store.ReflectParams{
			NS:     ns,
			DryRun: dryRun,
		})
		if err != nil {
			return err
		}

		if formatFlag == "text" {
			prefix := ""
			if dryRun {
				prefix = "(dry-run) "
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%sReflect complete:\n", prefix)
			fmt.Fprintf(cmd.OutOrStdout(), "  Memories evaluated: %d\n", result.MemoriesEvaluated)
			fmt.Fprintf(cmd.OutOrStdout(), "  Rules applied:      %d\n", result.RulesApplied)
			fmt.Fprintf(cmd.OutOrStdout(), "  Decayed:            %d\n", result.Decayed)
			fmt.Fprintf(cmd.OutOrStdout(), "  Promoted:           %d\n", result.Promoted)
			fmt.Fprintf(cmd.OutOrStdout(), "  Demoted:            %d\n", result.Demoted)
			fmt.Fprintf(cmd.OutOrStdout(), "  Archived:           %d\n", result.Archived)
			fmt.Fprintf(cmd.OutOrStdout(), "  Deleted:            %d\n", result.Deleted)
			fmt.Fprintf(cmd.OutOrStdout(), "  Merged:             %d\n", result.Merged)
			if len(result.Errors) > 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "  Errors:\n")
				for _, e := range result.Errors {
					fmt.Fprintf(cmd.OutOrStdout(), "    - %s\n", e)
				}
			}
			return nil
		}

		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(result)
	},
}

var ruleCmd = &cobra.Command{
	Use:   "rule",
	Short: "Manage reflect rules",
}

var ruleSetCmd = &cobra.Command{
	Use:   "set",
	Short: "Create or update a reflect rule",
	RunE: func(cmd *cobra.Command, args []string) error {
		name, _ := cmd.Flags().GetString("name")
		if name == "" {
			return fmt.Errorf("--name is required")
		}
		action, _ := cmd.Flags().GetString("action")
		if action == "" {
			return fmt.Errorf("--action is required")
		}

		id, _ := cmd.Flags().GetString("id")
		ns, _ := cmd.Flags().GetString("ns")
		priority, _ := cmd.Flags().GetInt("priority")
		condTier, _ := cmd.Flags().GetString("cond-tier")
		condAgeGT, _ := cmd.Flags().GetFloat64("cond-age-gt")
		condImpLT, _ := cmd.Flags().GetFloat64("cond-importance-lt")
		condAccessLT, _ := cmd.Flags().GetInt("cond-access-lt")
		condAccessGT, _ := cmd.Flags().GetInt("cond-access-gt")
		condUtilLT, _ := cmd.Flags().GetFloat64("cond-utility-lt")
		condKind, _ := cmd.Flags().GetString("cond-kind")
		condTag, _ := cmd.Flags().GetString("cond-tag")
		condSimGT, _ := cmd.Flags().GetFloat64("cond-similarity-gt")
		actionParamsStr, _ := cmd.Flags().GetString("action-params")

		var actionParams map[string]any
		if actionParamsStr != "" {
			if err := json.Unmarshal([]byte(actionParamsStr), &actionParams); err != nil {
				return fmt.Errorf("invalid --action-params JSON: %w", err)
			}
		}

		rule := store.ReflectRule{
			ID:       id,
			NS:       ns,
			Name:     name,
			Priority: priority,
			Cond: store.RuleCond{
				Tier:         condTier,
				AgeGTHours:   condAgeGT,
				ImportanceLT: condImpLT,
				AccessLT:     condAccessLT,
				AccessGT:     condAccessGT,
				UtilityLT:    condUtilLT,
				Kind:         condKind,
				TagIncludes:  condTag,
				SimilarityGT: condSimGT,
			},
			Action: store.RuleAction{
				Op:     action,
				Params: actionParams,
			},
		}

		result, err := st.RuleSet(cmd.Context(), rule)
		if err != nil {
			return err
		}

		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(result)
	},
}

var ruleListCmd = &cobra.Command{
	Use:   "list",
	Short: "List reflect rules",
	RunE: func(cmd *cobra.Command, args []string) error {
		ns, _ := cmd.Flags().GetString("ns")

		rules, err := st.RuleList(cmd.Context(), ns)
		if err != nil {
			return err
		}

		if formatFlag == "text" {
			if len(rules) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No rules found.")
				return nil
			}
			for _, r := range rules {
				fmt.Fprintf(cmd.OutOrStdout(), "[%s] %s (priority=%d, op=%s, by=%s)\n",
					r.ID, r.Name, r.Priority, r.Action.Op, r.CreatedBy)
			}
			return nil
		}

		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(rules)
	},
}

var ruleGetCmd = &cobra.Command{
	Use:   "get <id>",
	Short: "Get a reflect rule by ID",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		rule, err := st.RuleGet(cmd.Context(), args[0])
		if err != nil {
			return err
		}

		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(rule)
	},
}

var ruleDeleteCmd = &cobra.Command{
	Use:   "delete <id>",
	Short: "Delete a reflect rule",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := st.RuleDelete(cmd.Context(), args[0]); err != nil {
			return err
		}
		if formatFlag == "text" {
			fmt.Fprintf(cmd.OutOrStdout(), "Rule %s deleted.\n", args[0])
			return nil
		}
		fmt.Fprintf(cmd.OutOrStdout(), `{"deleted":"%s"}`+"\n", args[0])
		return nil
	},
}
