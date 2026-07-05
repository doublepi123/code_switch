package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

// cmdModel implements `cs model <get|set|list> <provider> [args]`.
//
//   - `cs model get <provider>` prints the provider's default model. When no
//     model is stored on the provider config, the preset default (after agent
//     resolution) is used so the printed model matches what the proxy / switch
//     pipeline would actually select.
//   - `cs model set <provider> <model>` persists the provider's default model
//     to the app config without touching the API key. Works for built-in
//     presets and for already-existing custom providers; unknown providers
//     are rejected.
//   - `cs model list <provider>` lists the provider's preset models. A custom
//     provider with no preset model list prints at least its current model.
func cmdModel(args []string, out io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: code-switch model <get|set|list> <provider> [model]")
	}
	sub := args[0]
	rest := args[1:]
	switch sub {
	case "get":
		return cmdModelGet(rest, out)
	case "set":
		return cmdModelSet(rest, out)
	case "list":
		return cmdModelList(rest, out)
	default:
		return fmt.Errorf("unknown model subcommand %q (supported: get, set, list)", sub)
	}
}

// parseProviderArg validates and canonicalizes a single positional provider
// argument. It returns a usage error when the provider is missing. The
// canonical name is returned so downstream code looks up a single key.
func parseProviderArg(args []string, usage string) (string, []string, error) {
	if len(args) == 0 {
		return "", nil, fmt.Errorf("%s", usage)
	}
	provider := canonicalProviderName(strings.TrimSpace(args[0]))
	if provider == "" {
		return "", nil, fmt.Errorf("%s", usage)
	}
	return provider, args[1:], nil
}

func cmdModelGet(args []string, out io.Writer) error {
	provider, rest, err := parseProviderArg(args, "usage: code-switch model get <provider>")
	if err != nil {
		return err
	}
	if len(rest) > 0 {
		return fmt.Errorf("usage: code-switch model get <provider> (unexpected extra arguments)")
	}
	cfg, _, err := loadAppConfig()
	if err != nil {
		return err
	}
	preset, err := resolveProviderPreset(provider, cfg)
	if err != nil {
		return fmt.Errorf("unsupported provider %q", provider)
	}
	fmt.Fprintf(out, "%s\n", preset.Model)
	return nil
}

func cmdModelSet(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("model set", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) != 2 {
		return fmt.Errorf("usage: code-switch model set <provider> <model>")
	}
	provider := canonicalProviderName(strings.TrimSpace(rest[0]))
	if provider == "" {
		return fmt.Errorf("usage: code-switch model set <provider> <model>")
	}
	model := strings.TrimSpace(rest[1])
	if model == "" {
		return fmt.Errorf("model must not be empty")
	}

	cfg, path, unlock, err := loadAppConfigLocked()
	if err != nil {
		return err
	}
	defer unlock()

	// setDefaultModelForProvider centralises the provider/model validation
	// (unknown provider, NoModel presets, opencode-go chat-only models,
	// custom providers) and the field-preserving persistence so `model
	// set` and `use-model` stay in lock-step.
	if err := setDefaultModelForProvider(cfg, provider, model); err != nil {
		return err
	}
	if err := writeJSONAtomic(path, cfg); err != nil {
		return err
	}
	fmt.Fprintf(out, "set default model for %s to %s\n", provider, model)
	return nil
}

func cmdModelList(args []string, out io.Writer) error {
	provider, rest, err := parseProviderArg(args, "usage: code-switch model list <provider>")
	if err != nil {
		return err
	}
	if len(rest) > 0 {
		return fmt.Errorf("usage: code-switch model list <provider> (unexpected extra arguments)")
	}
	cfg, _, err := loadAppConfig()
	if err != nil {
		return err
	}
	preset, err := resolveProviderPreset(provider, cfg)
	if err != nil {
		return fmt.Errorf("unsupported provider %q", provider)
	}

	fmt.Fprintf(out, "Models for %s:\n", provider)
	// A custom provider (not a built-in preset) with no stored model:
	// resolveProviderPreset synthesises a "custom-model" placeholder so
	// the rest of the pipeline always has a non-empty Model, but that
	// name is not a real model the user can invoke. Detect this case
	// (custom provider + empty stored model) and surface the explicit
	// "(no models available)" placeholder instead of printing the
	// synthetic name as if it were real.
	if !isPresetProvider(provider) {
		stored := cfg.Providers[provider]
		if strings.TrimSpace(stored.Model) == "" {
			fmt.Fprintf(out, "  (no models available)\n")
			return nil
		}
	}
	models := preset.Models
	for _, m := range models {
		if m == preset.Model {
			fmt.Fprintf(out, "  %s %s\n", m, dim("(default)"))
		} else {
			fmt.Fprintf(out, "  %s\n", m)
		}
	}
	return nil
}

// cmdModelMap implements `cs model-map <set|get|list|remove> <provider> ...`.
//
// Model mappings rewrite a client-visible model name (the model the agent
// sends in its request body) to the upstream provider's real model. A
// special "default" entry is the fallback for any client model not
// explicitly mapped. Mappings are stored per-provider under
// AppConfig.ModelMappings.
func cmdModelMap(args []string, out io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: code-switch model-map <set|get|list|remove> <provider> [client-model] [upstream-model]")
	}
	sub := args[0]
	rest := args[1:]
	switch sub {
	case "set":
		return cmdModelMapSet(rest, out)
	case "get":
		return cmdModelMapGet(rest, out)
	case "list":
		return cmdModelMapList(rest, out)
	case "remove":
		return cmdModelMapRemove(rest, out)
	default:
		return fmt.Errorf("unknown model-map subcommand %q (supported: set, get, list, remove)", sub)
	}
}

// ensureProviderModelMappings returns the non-nil mappings map for the
// provider, initializing cfg.ModelMappings lazily. Used by write paths so
// the top-level map only appears in config.json when a mapping is set.
func ensureProviderModelMappings(cfg *AppConfig, provider string) map[string]string {
	if cfg.ModelMappings == nil {
		cfg.ModelMappings = map[string]map[string]string{}
	}
	m := cfg.ModelMappings[provider]
	if m == nil {
		m = map[string]string{}
		cfg.ModelMappings[provider] = m
	}
	return m
}

// useModelForProvider persists `model` as the provider's default model and
// sets the "default" model mapping to the same model, mirroring the
// semantics of the `cs use-model` CLI command. It is the single helper
// shared by the CLI command and the upcoming TUI "Use Model" page.
//
// Validation matches `cmdUseModel`:
//   - provider is canonicalized via providerAliases (e.g. "bigmodel" -> "zhipu-cn");
//   - empty/whitespace provider or model is rejected with an actionable message;
//   - unknown providers are rejected (only built-in presets and already-saved
//     custom providers with a BaseURL are allowed);
//   - NoModel presets (e.g. kimi-coding) reject default-model selection;
//   - opencode-go rejects chat/completions-only Anthropic models;
//   - custom (non-preset) providers pass through.
//
// The caller is responsible for locking and persisting cfg via writeJSONAtomic.
func useModelForProvider(cfg *AppConfig, provider, model string) error {
	provider = canonicalProviderName(strings.TrimSpace(provider))
	model = strings.TrimSpace(model)
	if provider == "" {
		return fmt.Errorf("provider must not be empty")
	}
	if model == "" {
		return fmt.Errorf("model must not be empty")
	}
	if err := setDefaultModelForProvider(cfg, provider, model); err != nil {
		return err
	}
	// Mirror `cs use-model`: also point the "default" client-model
	// mapping at the same model so the proxy default-fallback resolves
	// to it.
	ensureProviderModelMappings(cfg, provider)["default"] = model
	return nil
}

// setDefaultModelForProvider is the shared validation + persistence core
// used by both `cmdModelSet` and `useModelForProvider`. It assumes
// `provider` and `model` have already been canonicalized and trimmed and
// are non-empty; callers handle the empty checks themselves so the error
// messages stay specific. It writes the model to cfg.Providers[provider]
// (preserving every other field) but does NOT touch model mappings —
// `useModelForProvider` additionally sets the "default" mapping on top.
//
// The caller is responsible for locking and persisting cfg via writeJSONAtomic.
func setDefaultModelForProvider(cfg *AppConfig, provider, model string) error {
	preset, err := resolveProviderPreset(provider, cfg)
	if err != nil {
		return fmt.Errorf("unsupported provider %q", provider)
	}
	if err := validateModelSelectionForProvider(provider, model, isPresetProvider(provider), preset); err != nil {
		return err
	}
	stored := cfg.Providers[provider]
	// Preserve every other field (API key, tiers, base URL) and only
	// overwrite the default model.
	stored.Model = model
	cfg.Providers[provider] = stored
	return nil
}

// setModelMappingForProvider records a single client-model -> upstream-model
// mapping for the provider, mirroring `cs model-map set` semantics. The
// provider is canonicalized and validated (built-in presets and existing
// custom providers only). Empty provider, client-model, or upstream-model
// is rejected.
//
// The caller is responsible for locking and persisting cfg via writeJSONAtomic.
func setModelMappingForProvider(cfg *AppConfig, provider, clientModel, upstreamModel string) error {
	provider = canonicalProviderName(strings.TrimSpace(provider))
	clientModel = strings.TrimSpace(clientModel)
	upstreamModel = strings.TrimSpace(upstreamModel)
	if provider == "" {
		return fmt.Errorf("provider must not be empty")
	}
	if clientModel == "" || upstreamModel == "" {
		return fmt.Errorf("client-model and upstream-model must not be empty")
	}
	if _, err := resolveProviderPreset(provider, cfg); err != nil {
		return fmt.Errorf("unsupported provider %q", provider)
	}
	ensureProviderModelMappings(cfg, provider)[clientModel] = upstreamModel
	return nil
}

// removeModelMappingForProvider deletes a single client-model mapping for
// the provider, mirroring `cs model-map remove` semantics. The provider is
// canonicalized and validated. When the provider's mapping table becomes
// empty after deletion, the provider entry itself is removed from
// cfg.ModelMappings so a subsequent `model-map get` cleanly reports "no
// mappings" rather than a lingering empty map.
//
// Errors (unknown provider, empty provider/client model, no provider
// mappings, missing client model) leave cfg untouched.
//
// The caller is responsible for locking and persisting cfg via writeJSONAtomic.
func removeModelMappingForProvider(cfg *AppConfig, provider, clientModel string) error {
	provider = canonicalProviderName(strings.TrimSpace(provider))
	clientModel = strings.TrimSpace(clientModel)
	if provider == "" {
		return fmt.Errorf("provider must not be empty")
	}
	if clientModel == "" {
		return fmt.Errorf("client-model must not be empty")
	}
	if _, err := resolveProviderPreset(provider, cfg); err != nil {
		return fmt.Errorf("unsupported provider %q", provider)
	}
	mappings := cfg.ModelMappings[provider]
	if mappings == nil {
		return fmt.Errorf("no model mappings for provider %q", provider)
	}
	if _, ok := mappings[clientModel]; !ok {
		return fmt.Errorf("no mapping for client model %q on provider %q", clientModel, provider)
	}
	delete(mappings, clientModel)
	// Drop the provider entry when it becomes empty so a subsequent
	// `model-map get` cleanly reports "no mappings" rather than a
	// lingering empty map.
	if len(mappings) == 0 {
		delete(cfg.ModelMappings, provider)
	}
	return nil
}

func cmdModelMapSet(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("model-map set", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) != 3 {
		return fmt.Errorf("usage: code-switch model-map set <provider> <client-model> <upstream-model>")
	}
	provider := canonicalProviderName(strings.TrimSpace(rest[0]))
	if provider == "" {
		return fmt.Errorf("usage: code-switch model-map set <provider> <client-model> <upstream-model>")
	}
	clientModel := strings.TrimSpace(rest[1])
	upstreamModel := strings.TrimSpace(rest[2])
	if clientModel == "" || upstreamModel == "" {
		return fmt.Errorf("client-model and upstream-model must not be empty")
	}

	cfg, path, unlock, err := loadAppConfigLocked()
	if err != nil {
		return err
	}
	defer unlock()

	if err := setModelMappingForProvider(cfg, provider, clientModel, upstreamModel); err != nil {
		return err
	}
	if err := writeJSONAtomic(path, cfg); err != nil {
		return err
	}
	fmt.Fprintf(out, "mapped %s %s -> %s\n", provider, clientModel, upstreamModel)
	return nil
}

func cmdModelMapGet(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("model-map get", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) < 1 || len(rest) > 2 {
		return fmt.Errorf("usage: code-switch model-map get <provider> [client-model]")
	}
	provider := canonicalProviderName(strings.TrimSpace(rest[0]))
	if provider == "" {
		return fmt.Errorf("usage: code-switch model-map get <provider> [client-model]")
	}
	if len(rest) == 2 {
		if strings.TrimSpace(rest[1]) == "" {
			return fmt.Errorf("client-model must not be empty")
		}
	}
	cfg, _, err := loadAppConfig()
	if err != nil {
		return err
	}
	if _, perr := resolveProviderPreset(provider, cfg); perr != nil {
		return fmt.Errorf("unsupported provider %q", provider)
	}
	mappings := cfg.ModelMappings[provider]

	if len(rest) == 2 {
		clientModel := strings.TrimSpace(rest[1])
		if mappings == nil {
			return fmt.Errorf("no model mappings for provider %q", provider)
		}
		upstream, ok := mappings[clientModel]
		if !ok {
			return fmt.Errorf("no mapping for client model %q on provider %q", clientModel, provider)
		}
		fmt.Fprintf(out, "%s\n", upstream)
		return nil
	}

	// No client-model: list all mappings for the provider.
	if mappings == nil || len(mappings) == 0 {
		return fmt.Errorf("no model mappings for provider %q", provider)
	}
	keys := make([]string, 0, len(mappings))
	for k := range mappings {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(out, "%s\t%s\n", k, mappings[k])
	}
	return nil
}

func cmdModelMapList(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("model-map list", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) != 1 {
		return fmt.Errorf("usage: code-switch model-map list <provider>")
	}
	provider := canonicalProviderName(strings.TrimSpace(rest[0]))
	if provider == "" {
		return fmt.Errorf("usage: code-switch model-map list <provider>")
	}
	cfg, _, err := loadAppConfig()
	if err != nil {
		return err
	}
	if _, perr := resolveProviderPreset(provider, cfg); perr != nil {
		return fmt.Errorf("unsupported provider %q", provider)
	}
	mappings := cfg.ModelMappings[provider]
	fmt.Fprintf(out, "Model mappings for %s:\n", provider)
	if len(mappings) == 0 {
		fmt.Fprintf(out, "  (none)\n")
		return nil
	}
	keys := make([]string, 0, len(mappings))
	for k := range mappings {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(out, "  %s -> %s\n", k, mappings[k])
	}
	return nil
}

func cmdModelMapRemove(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("model-map remove", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) != 2 {
		return fmt.Errorf("usage: code-switch model-map remove <provider> <client-model>")
	}
	provider := canonicalProviderName(strings.TrimSpace(rest[0]))
	if provider == "" {
		return fmt.Errorf("usage: code-switch model-map remove <provider> <client-model>")
	}
	clientModel := strings.TrimSpace(rest[1])
	if clientModel == "" {
		return fmt.Errorf("client-model must not be empty")
	}

	cfg, path, unlock, err := loadAppConfigLocked()
	if err != nil {
		return err
	}
	defer unlock()

	if err := removeModelMappingForProvider(cfg, provider, clientModel); err != nil {
		return err
	}
	if err := writeJSONAtomic(path, cfg); err != nil {
		return err
	}
	fmt.Fprintf(out, "removed mapping %s %s\n", provider, clientModel)
	return nil
}

// validateModelSelectionForProvider validates that a default model can be
// persisted for the given provider and that the model itself is acceptable.
// It centralises the rules shared by `model set` and `use-model`:
//
//   - A built-in preset provider whose ProviderPreset.NoModel is true (e.g.
//     kimi-coding, which is wired by API key alone and ignores the model
//     field) rejects default-model persistence with an actionable message.
//   - Otherwise the standard validateProviderModel check runs (e.g.
//     opencode-go rejects chat/completions-only Anthropic models).
//   - Custom (non-preset) providers are NOT rejected here: they have no
//     preset NoModel flag and validateProviderModel only special-cases
//     opencode-go, so they pass through and the caller persists the model.
//
// presetKnown reports whether `provider` is a built-in preset (i.e. present
// in providerPresets); preset is the resolved preset (zero value when
// unknown).
func validateModelSelectionForProvider(provider string, model string, presetKnown bool, preset ProviderPreset) error {
	if presetKnown && preset.NoModel {
		return fmt.Errorf("provider %s does not accept model selection (it is configured by API key alone)", provider)
	}
	return validateProviderModel(provider, model)
}

// cmdUseModel is equivalent to:
//
//	cs model set <provider> <model>
//	cs model-map set <provider> default <model>
//
// It is the single-command fast path for "make this provider use this
// model everywhere, including the proxy default". Both writes happen in
// one locked config transaction so a partial state is never observable
// on disk.
func cmdUseModel(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("use-model", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) != 2 {
		return fmt.Errorf("usage: code-switch use-model <provider> <model>")
	}
	provider := canonicalProviderName(strings.TrimSpace(rest[0]))
	if provider == "" {
		return fmt.Errorf("usage: code-switch use-model <provider> <model>")
	}
	model := strings.TrimSpace(rest[1])
	if model == "" {
		return fmt.Errorf("model must not be empty")
	}

	cfg, path, unlock, err := loadAppConfigLocked()
	if err != nil {
		return err
	}
	defer unlock()

	if err := useModelForProvider(cfg, provider, model); err != nil {
		return err
	}
	if err := writeJSONAtomic(path, cfg); err != nil {
		return err
	}
	fmt.Fprintf(out, "using %s on %s (set as default model and model-map default)\n", model, provider)
	return nil
}
