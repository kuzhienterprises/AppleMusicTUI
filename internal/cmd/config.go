package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/kuzhienterprises/AppleMusicTUI/internal/config"
	"github.com/kuzhienterprises/AppleMusicTUI/internal/ui"
)

// setting is one row of `am config`: the name it is set by, and how to read it
// from the saved settings.
type setting struct {
	name  string
	help  string
	read  func(config.Config) string
	write func(*config.Config, string)
	// secret marks a value that is never printed, only whether it is set.
	secret bool
}

var settings = []setting{
	{
		name: "storefront",
		help: "two-letter country code used for the catalogue",
		read: func(c config.Config) string { return c.Storefront },
		write: func(c *config.Config, value string) {
			c.Storefront = strings.ToLower(value)
		},
	},
	{
		name: "openaiKey",
		help: "key recommendations are bought with (OPENAI_API_KEY wins over it)",
		read: func(c config.Config) string { return c.OpenAIKey },
		write: func(c *config.Config, value string) {
			c.OpenAIKey = value
		},
		secret: true,
	},
	{
		name: "openaiModel",
		help: "model recommendations use",
		read: func(c config.Config) string { return c.OpenAIModel },
		write: func(c *config.Config, value string) {
			c.OpenAIModel = value
		},
	},
	{
		name: "mpvPath",
		help: "mpv binary to use for previews",
		read: func(c config.Config) string { return c.MpvPath },
		write: func(c *config.Config, value string) {
			c.MpvPath = value
		},
	},
}

func newConfigCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Show or change the saved settings",
		Long: `Shows the saved settings, with the secrets masked to whether they are set.

The OpenAI key can also live in the env file, which is a plain KEY=VALUE file
read before every command: see ` + "`am config env`" + `. A variable already set
in the environment wins over both.`,
		Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			cfg := config.Load()
			for _, item := range settings {
				ui.Info("%-13s %s", item.name, describe(item, item.read(cfg)))
			}
			ui.Info("%-13s %s", "env file", config.EnvFile+" ("+config.EnvFileStatus()+")")
			return nil
		},
	}

	cmd.AddCommand(newConfigSetCommand(), newConfigUnsetCommand(), newConfigEnvCommand())
	return cmd
}

// newConfigEnvCommand creates the env file. It is a separate command rather
// than something `am config` does on its own, because writing a file in the
// settings directory is not something a listing should have as a side effect.
func newConfigEnvCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "env",
		Short: "Create the env file used for keys",
		Long: `Creates the env file if it is not there yet, with a commented example in it.

Every am command reads it before it starts, so a key put there works without
being exported in each shell. A variable already set in the environment wins
over the file, and the file is never uploaded anywhere: it is read on this
machine, for this process.

The file is not overwritten if it already exists.`,
		Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			path, created, err := config.WriteEnvTemplate()
			if err != nil {
				return err
			}
			if created {
				ui.OK("Created %s.", path)
				ui.Info("Put a key in it, e.g. OPENAI_API_KEY=sk-...")
				return nil
			}
			ui.OK("%s already exists.", path)
			return nil
		},
	}
}

func newConfigSetCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "set <name> <value>",
		Short: "Save a setting",
		Long: `Saves a setting. The names are:

  storefront    two-letter country code used for the catalogue
  openaiKey     key recommendations are bought with
  openaiModel   model recommendations use, e.g. gpt-6-luna
  mpvPath       mpv binary to use for previews

Setting the OpenAI key this way puts it in your shell history. The env file
(` + "`am config env`" + `) holds it just as well, and keeps it out of both the
shell and the saved settings.`,
		Args: cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			item, ok := lookupSetting(args[0])
			if !ok {
				return fmt.Errorf("unknown setting %q", args[0])
			}
			value := strings.TrimSpace(args[1])
			if value == "" {
				return fmt.Errorf("%s cannot be empty - use `am config unset %s` instead", item.name, item.name)
			}
			if _, err := config.Update(func(c *config.Config) { item.write(c, value) }); err != nil {
				return err
			}
			ui.OK("Saved %s.", item.name)
			return nil
		},
	}
}

func newConfigUnsetCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "unset <name>",
		Short: "Forget a saved setting",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			item, ok := lookupSetting(args[0])
			if !ok {
				return fmt.Errorf("unknown setting %q", args[0])
			}
			if err := config.Clear(item.name); err != nil {
				return err
			}
			ui.OK("Cleared %s.", item.name)
			return nil
		},
	}
}

func lookupSetting(name string) (setting, bool) {
	for _, item := range settings {
		if item.name == name {
			return item, true
		}
	}
	return setting{}, false
}

// describe renders a setting for the list. A secret says whether it is set and
// nothing else: the point of masking is that a screenshot of this output is
// not a leak.
func describe(item setting, value string) string {
	if item.secret {
		if value == "" {
			return "not set"
		}
		return "set"
	}
	if value == "" {
		return "not set"
	}
	return value
}

// openAIKey is the key recommendations use: the environment first, because it
// cannot leak through this repository's config file, then the saved one.
func openAIKey() string {
	if key := strings.TrimSpace(os.Getenv("OPENAI_API_KEY")); key != "" {
		return key
	}
	return strings.TrimSpace(config.Load().OpenAIKey)
}
