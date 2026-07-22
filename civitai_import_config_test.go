package main

import "testing"

func TestResolveImportConfigUsesFileValuesAsBaseline(t *testing.T) {
	fileConfig := &ImportConfig{
		Token:               "file-token",
		Username:            "file-user",
		AutoImportOnStartup: true,
	}

	config := resolveImportConfig(fileConfig, emptyEnvironment)

	if config.Token != "file-token" || config.Username != "file-user" || !config.AutoImportOnStartup {
		t.Fatalf("unexpected config: %#v", config)
	}
}

func TestResolveImportConfigLetsEnvironmentOverrideFile(t *testing.T) {
	fileConfig := &ImportConfig{
		Token:               "file-token",
		Username:            "file-user",
		AutoImportOnStartup: true,
	}
	environment := map[string]string{
		"CIVITAI_TOKEN":          "op-token",
		"CIVITAI_USERNAME":       "environment-user",
		"AUTO_IMPORT_ON_STARTUP": "false",
	}

	config := resolveImportConfig(fileConfig, mapEnvironment(environment))

	if config.Token != "op-token" {
		t.Errorf("expected injected token, got %q", config.Token)
	}
	if config.Username != "environment-user" {
		t.Errorf("expected injected username, got %q", config.Username)
	}
	if config.AutoImportOnStartup {
		t.Error("expected injected auto-import setting to disable auto import")
	}
}

func TestResolveImportConfigProvidesDefaults(t *testing.T) {
	config := resolveImportConfig(nil, emptyEnvironment)

	if config.Username != "moutonrebelle" {
		t.Errorf("unexpected default username: %q", config.Username)
	}
	if config.Token != "" || config.AutoImportOnStartup {
		t.Fatalf("unexpected defaults: %#v", config)
	}
}

func emptyEnvironment(string) (string, bool) {
	return "", false
}

func mapEnvironment(values map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}
