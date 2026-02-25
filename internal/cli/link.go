package cli

import (
	"fmt"
	"strings"

	"github.com/rcliao/agent-memory/internal/store"
	"github.com/spf13/cobra"
)

func init() {
	cmd := &cobra.Command{
		Use:   "link",
		Short: "Create or remove relations between memories",
		Run:   runLink,
	}

	cmd.Flags().String("from-ns", "", "Source namespace")
	cmd.Flags().String("from-key", "", "Source key")
	cmd.Flags().String("to-ns", "", "Target namespace")
	cmd.Flags().String("to-key", "", "Target key")
	cmd.Flags().StringP("rel", "r", "", "Relation: relates_to, contradicts, depends_on, refines")
	cmd.Flags().Bool("rm", false, "Remove the link")

	cmd.MarkFlagRequired("from-ns")
	cmd.MarkFlagRequired("from-key")
	cmd.MarkFlagRequired("to-ns")
	cmd.MarkFlagRequired("to-key")
	cmd.MarkFlagRequired("rel")

	RootCmd.AddCommand(cmd)
}

func runLink(cmd *cobra.Command, args []string) {
	fromNS, _ := cmd.Flags().GetString("from-ns")
	fromKey, _ := cmd.Flags().GetString("from-key")
	toNS, _ := cmd.Flags().GetString("to-ns")
	toKey, _ := cmd.Flags().GetString("to-key")
	rel, _ := cmd.Flags().GetString("rel")
	rm, _ := cmd.Flags().GetBool("rm")

	if !validLinkRels[rel] {
		exitErr("link", fmt.Errorf("invalid --rel %q — must be one of: relates_to, contradicts, depends_on, refines", rel))
	}

	link, err := st.Link(cmd.Context(), store.LinkParams{
		FromNS:  fromNS,
		FromKey: fromKey,
		ToNS:    toNS,
		ToKey:   toKey,
		Rel:     rel,
		Remove:  rm,
	})
	if err != nil {
		errStr := err.Error()
		switch {
		case strings.Contains(errStr, "not found"):
			exitErr("link", fmt.Errorf("one or both memories not found: %s/%s or %s/%s — verify they exist with 'get'", fromNS, fromKey, toNS, toKey))
		default:
			exitErr("link", fmt.Errorf("failed to link %s/%s -> %s/%s: %w", fromNS, fromKey, toNS, toKey, err))
		}
	}

	outputJSON(cmd, link)
}
