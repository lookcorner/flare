package cmd

import (
	"flare/internal/daemon"

	"github.com/spf13/cobra"
)

var downCmd = &cobra.Command{
	Use:   "down",
	Short: "停止隧道",
	RunE: func(cmd *cobra.Command, args []string) error {
		return daemon.Stop()
	},
}

func init() {
	systemCmd.AddCommand(downCmd)
}
