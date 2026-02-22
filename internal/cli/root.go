package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/phenixrizen/rift/internal/awsconfig"
	"github.com/phenixrizen/rift/internal/config"
	"github.com/phenixrizen/rift/internal/discovery"
	"github.com/phenixrizen/rift/internal/kubeconfig"
	"github.com/phenixrizen/rift/internal/namespaces"
	"github.com/phenixrizen/rift/internal/naming"
	"github.com/phenixrizen/rift/internal/state"
	"github.com/spf13/cobra"
)

var ErrSSOLoginRequired = errors.New("aws sso login required")

type App struct {
	ConfigPath string
	StatePath  string
	Debug      bool
	Logger     *slog.Logger
}

type SyncReport struct {
	Inventory discovery.Inventory
	State     state.State
	NS        namespaces.Result
	AWS       awsconfig.SyncResult
	Kube      kubeconfig.SyncResult
	DryRun    bool
}

func Execute() error {
	root, err := NewRootCommand()
	if err != nil {
		return err
	}
	return root.Execute()
}

func NewRootCommand() (*cobra.Command, error) {
	defaultConfigPath, err := config.DefaultConfigPath()
	if err != nil {
		return nil, err
	}
	defaultStatePath, err := config.DefaultStatePath()
	if err != nil {
		return nil, err
	}

	app := &App{
		ConfigPath: defaultConfigPath,
		StatePath:  defaultStatePath,
	}

	cmd := &cobra.Command{
		Use:           "rift",
		Short:         "Rift orchestrates AWS SSO profiles and EKS kube contexts",
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(_ *cobra.Command, _ []string) error {
			return app.initialize()
		},
	}
	cmd.PersistentFlags().StringVar(&app.ConfigPath, "config", app.ConfigPath, "Path to config.yaml")
	cmd.PersistentFlags().StringVar(&app.StatePath, "state", app.StatePath, "Path to state.json")
	cmd.PersistentFlags().BoolVar(&app.Debug, "debug", false, "Enable debug logging")

	cmd.AddCommand(
		newInitCmd(app),
		newAuthCmd(app),
		newSyncCmd(app),
		newListCmd(app),
		newUseCmd(app),
		newUICmd(app),
		newGraphCmd(app),
		newVersionCmd(),
	)
	return cmd, nil
}

func (a *App) initialize() error {
	configPath, err := config.ResolvePath(a.ConfigPath)
	if err != nil {
		return err
	}
	statePath, err := config.ResolvePath(a.StatePath)
	if err != nil {
		return err
	}
	a.ConfigPath = configPath
	a.StatePath = statePath

	level := slog.LevelInfo
	if a.Debug {
		level = slog.LevelDebug
	}
	a.Logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	return nil
}

func (a *App) loadConfig() (config.Config, error) {
	cfg, err := config.Load(a.ConfigPath)
	if err != nil {
		return cfg, fmt.Errorf("load config %s: %w", a.ConfigPath, err)
	}
	return cfg, nil
}

func (a *App) loadState() (state.State, error) {
	st, err := state.Load(a.StatePath)
	if err != nil {
		return st, fmt.Errorf("load state %s: %w", a.StatePath, err)
	}
	return st, nil
}

func (a *App) RunSync(ctx context.Context, dryRun bool) (SyncReport, error) {
	cfg, err := a.loadConfig()
	if err != nil {
		return SyncReport{}, err
	}

	inv, err := discovery.Discover(ctx, cfg, a.Logger)
	if err != nil {
		if errors.Is(err, discovery.ErrSSONotLoggedIn) {
			return SyncReport{}, fmt.Errorf("%w. Run: rift auth", ErrSSOLoginRequired)
		}
		return SyncReport{}, err
	}

	st := naming.BuildState(cfg, inv)
	nsResult := namespaces.Result{}
	if cfg.DiscoverNamespaces {
		nsResult, err = namespaces.Enrich(ctx, &st, cfg, a.Logger)
		if err != nil {
			return SyncReport{}, fmt.Errorf("discover namespaces: %w", err)
		}
	}

	awsConfigPath, err := defaultAWSConfigPath()
	if err != nil {
		return SyncReport{}, err
	}
	kubeConfigPath, err := defaultKubeConfigPath()
	if err != nil {
		return SyncReport{}, err
	}

	awsResult, err := awsconfig.Sync(awsConfigPath, cfg, st, dryRun)
	if err != nil {
		return SyncReport{}, fmt.Errorf("sync aws config: %w", err)
	}
	kubeResult, err := kubeconfig.Sync(kubeConfigPath, st, cfg, dryRun)
	if err != nil {
		return SyncReport{}, fmt.Errorf("sync kubeconfig: %w", err)
	}

	if !dryRun {
		if err := state.Save(a.StatePath, st); err != nil {
			return SyncReport{}, fmt.Errorf("write state: %w", err)
		}
	}

	return SyncReport{
		Inventory: inv,
		State:     st,
		NS:        nsResult,
		AWS:       awsResult,
		Kube:      kubeResult,
		DryRun:    dryRun,
	}, nil
}

func defaultAWSConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".aws", "config"), nil
}

func defaultKubeConfigPath() (string, error) {
	if env := strings.TrimSpace(os.Getenv("KUBECONFIG")); env != "" {
		parts := strings.Split(env, string(os.PathListSeparator))
		if len(parts) > 0 {
			return config.ResolvePath(parts[0])
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".kube", "config"), nil
}

func println(w io.Writer, lines ...string) {
	for _, line := range lines {
		_, _ = fmt.Fprintln(w, line)
	}
}
