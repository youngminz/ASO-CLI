package main

import (
	"os"

	"github.com/spf13/cobra"
)

var outputFormat string

var rootCmd = &cobra.Command{
	Use:   "aads-aso",
	Short: "Standalone ASO CLI for unofficial Apple endpoints",
	Long: "Standalone ASO CLI for unofficial Apple endpoints.\n" +
		"This binary is intentionally separate from aads because these commands rely on undocumented behavior and may break at any time.",
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func init() {
	rootCmd.PersistentFlags().StringVarP(&outputFormat, "output", "o", "json", "Output format: json, table, yaml")

	rootCmd.AddCommand(newASOPopscoreCmd())
	rootCmd.AddCommand(newASORecommendCmd())
	rootCmd.AddCommand(newASOHintsCmd())
	rootCmd.AddCommand(newASOCMCookieCmd())
}
