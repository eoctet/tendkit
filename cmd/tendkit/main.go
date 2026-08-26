package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/eoctet/tendkit/internal/service"
	"github.com/eoctet/tendkit/internal/ui"
	"github.com/eoctet/tendkit/pkg/i18n"
	runtimeutil "github.com/eoctet/tendkit/pkg/runtime"
)

var (
	programVersion = "dev"
	commitSHA      = "unknown"
	buildDate      = "unknown"
)

type tuiRunner func(context.Context, *service.Service, ui.Mode) error

var detectHostSystem = runtimeutil.DetectSystemInfo

func main() {
	os.Exit(run(os.Args[1:]))
}

type commandOptions struct {
	configPath string
	lockPath   string
	color      ui.Mode
	envPath    string
	noEnvFile  bool
}

func run(arguments []string) int {
	return runWithTUI(arguments, runInteractiveTUI)
}

func runWithTUI(arguments []string, startTUI tuiRunner) int {
	i18n.Set(i18n.Detect())
	explicitLanguage, exitCode, done := configureCommandLanguage(arguments)
	if done {
		return exitCode
	}
	bootstrap := service.DefaultBootstrap()
	action, arguments, exitCode, done := resolveCommand(arguments, bootstrap)
	if done {
		return exitCode
	}
	options, exitCode, done := parseCommandOptions(action, arguments, bootstrap)
	if done {
		return exitCode
	}
	if exitCode := loadCommandEnvironment(options, bootstrap); exitCode != 0 {
		return exitCode
	}
	if action == "version" {
		fmt.Println("tendkit", programVersion)
		return 0
	}
	if !options.color.Valid() {
		fmt.Fprintln(os.Stderr, i18n.T("label.error")+":", i18n.T("cli.invalid_color"))
		return 2
	}
	if err := requireSupportedHost(context.Background()); err != nil {
		return reportError(err)
	}
	options, err := resolveCommandPaths(options)
	if err != nil {
		return reportError(err)
	}
	applicationService := service.New(options.configPath, options.lockPath)
	catalog, err := applicationService.Start()
	if err != nil {
		return reportError(err)
	}
	defer func() { _ = applicationService.Close() }()
	if !explicitLanguage {
		applyConfiguredLanguage(catalog.Settings.Language)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := startTUI(ctx, applicationService, options.color); err != nil {
		return reportError(err)
	}
	return 0
}

func resolveCommandPaths(options commandOptions) (commandOptions, error) {
	configPath, err := service.ResolvePath(options.configPath)
	if err != nil {
		return commandOptions{}, err
	}
	lockPath, err := service.ResolvePath(options.lockPath)
	if err != nil {
		return commandOptions{}, err
	}
	options.configPath = configPath
	options.lockPath = lockPath
	return options, nil
}

func requireSupportedHost(ctx context.Context) error {
	info, err := detectHostSystem(ctx)
	if err != nil {
		return err
	}
	if !runtimeutil.IsSupportedSystem(info) {
		return errors.New(i18n.T("cli.unsupported_platform", info.FullName))
	}
	return nil
}

func configureCommandLanguage(arguments []string) (bool, int, bool) {
	language, exists, err := languageArgument(arguments)
	if err != nil {
		fmt.Fprintln(os.Stderr, i18n.T("label.error")+":", i18n.T("cli.invalid_language"))
		return false, 2, true
	}
	if exists {
		i18n.Set(language)
	}
	return exists, 0, false
}

func resolveCommand(arguments []string, bootstrap service.Bootstrap) (string, []string, int, bool) {
	if len(arguments) == 0 {
		return "default", nil, 0, false
	}
	first := arguments[0]
	if first == "help" || first == "-h" || first == "--help" {
		usage(bootstrap)
		return "", nil, 0, true
	}
	if first == "version" || first == "--version" {
		return "version", arguments[1:], 0, false
	}
	if strings.HasPrefix(first, "-") {
		return "default", arguments, 0, false
	}
	fmt.Fprintln(os.Stderr, i18n.T("cli.unknown_command", first))
	usage(bootstrap)
	return "", nil, 2, true
}

func parseCommandOptions(action string, arguments []string, bootstrap service.Bootstrap) (commandOptions, int, bool) {
	flags := flag.NewFlagSet(action, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", bootstrap.ConfigPath, i18n.T("flag.config"))
	lockPath := flags.String("lock", bootstrap.LockPath, i18n.T("flag.lock"))
	colorText := flags.String("color", "auto", i18n.T("flag.color"))
	_ = flags.String("lang", string(i18n.Current()), i18n.T("flag.language"))
	envPath := flags.String("env-file", "", i18n.T("flag.env_file"))
	noEnvFile := flags.Bool("no-env-file", false, i18n.T("flag.no_env_file"))
	if err := flags.Parse(arguments); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			usage(bootstrap)
			return commandOptions{}, 0, true
		}
		fmt.Fprintln(os.Stderr, i18n.T("label.error")+":", i18n.T("cli.extra_arguments"))
		return commandOptions{}, 2, true
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(os.Stderr, i18n.T("label.error")+":", i18n.T("cli.extra_arguments"))
		return commandOptions{}, 2, true
	}
	if *noEnvFile && *envPath != "" {
		fmt.Fprintln(os.Stderr, i18n.T("label.error")+":", i18n.T("cli.env_conflict"))
		return commandOptions{}, 2, true
	}
	effectiveLockPath := *lockPath
	explicitLockPath := false
	flags.Visit(func(option *flag.Flag) {
		if option.Name == "lock" {
			explicitLockPath = true
		}
	})
	if !explicitLockPath {
		effectiveLockPath = *configPath + ".lock"
	}
	return commandOptions{
		configPath: *configPath,
		lockPath:   effectiveLockPath,
		color:      ui.Mode(*colorText),
		envPath:    *envPath,
		noEnvFile:  *noEnvFile,
	}, 0, false
}

func loadCommandEnvironment(options commandOptions, bootstrap service.Bootstrap) int {
	if options.noEnvFile {
		return 0
	}
	err := service.LoadEnvironment(options.envPath, bootstrap)
	if err != nil {
		fmt.Fprintln(os.Stderr, i18n.T("label.error")+":", err)
		return 2
	}
	return 0
}

func applyConfiguredLanguage(value string) {
	if language, err := i18n.Parse(value); err == nil {
		i18n.Set(language)
	}
}

func reportError(err error) int {
	if err == context.Canceled {
		return 130
	}
	fmt.Fprintln(os.Stderr, i18n.T("label.error")+":", i18n.ErrorText(err))
	return 2
}

func usage(bootstrap service.Bootstrap) {
	fmt.Print(usageText(bootstrap))
}

func usageText(bootstrap service.Bootstrap) string {
	return "\n" + i18n.Banner() + "\n\n" + i18n.T("cli.help", bootstrap.ConfigPath, bootstrap.LockPath)
}

func languageArgument(arguments []string) (i18n.Language, bool, error) {
	var selected i18n.Language
	found := false
	for index, argument := range arguments {
		value := ""
		switch {
		case strings.HasPrefix(argument, "--lang="):
			value = strings.TrimPrefix(argument, "--lang=")
		case argument == "--lang":
			if index+1 >= len(arguments) {
				return "", true, i18n.ErrUnsupportedLanguage
			}
			value = arguments[index+1]
		default:
			continue
		}
		language, err := i18n.Parse(value)
		if err != nil {
			return "", true, err
		}
		selected = language
		found = true
	}
	return selected, found, nil
}
