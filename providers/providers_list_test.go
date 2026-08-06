package providers

import (
	"reflect"
	"testing"
)

// TestSplitModels_TrimsEntries verifies a spaced or ragged comma-separated model
// list yields usable names: " b" would otherwise be registered as a model no
// request can ever match.
func TestSplitModels_TrimsEntries(t *testing.T) {
	tests := []struct {
		name string
		list string
		want []string
	}{
		{name: "spaced entries are trimmed", list: "a, b", want: []string{"a", "b"}},
		{name: "surrounding whitespace is trimmed", list: " a , b ", want: []string{"a", "b"}},
		{name: "empty entries are dropped", list: "a,,b,", want: []string{"a", "b"}},
		{name: "blank list yields nothing", list: " , ", want: nil},
		{name: "empty string yields nothing", list: "", want: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := splitModels(tt.list); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("splitModels(%q) = %#v, want %#v", tt.list, got, tt.want)
			}
		})
	}
}

// TestOllamaBuild_TrimsConfiguredModels verifies the trimming reaches the built
// provider's model list, not just the parser.
func TestOllamaBuild_TrimsConfiguredModels(t *testing.T) {
	entry, ok := GetProviderEntry(NameOllama)
	if !ok {
		t.Fatal("ollama provider entry not found")
	}

	p, err := entry.Build(ProviderConfig{
		CfgKeyHost:   "http://localhost:11434",
		CfgKeyModels: "llama3.2, mistral",
	})
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}

	lister, ok := p.(interface{ ConfiguredModels() []string })
	if !ok {
		t.Fatal("ollama provider does not report supported models")
	}
	if want := []string{"llama3.2", "mistral"}; !reflect.DeepEqual(lister.ConfiguredModels(), want) {
		t.Errorf("ConfiguredModels() = %#v, want %#v", lister.ConfiguredModels(), want)
	}
}

// TestIsDirectoryPath guards the OLLAMA_MODELS collision: Ollama's own variable
// of that name is its models *directory*, so a path landing in the model list
// must be recognised — without swallowing model ids that merely contain a colon
// or a slash.
func TestIsDirectoryPath(t *testing.T) {
	paths := []string{
		"/root/.ollama/models", // Ollama's own default shape
		"/usr/share/ollama/.ollama/models",
		"~/.ollama/models",
		"./models",
		"../models",
		`C:\Users\me\.ollama\models`,
		"D:/ollama/models",
	}
	for _, v := range paths {
		if !isDirectoryPath(v) {
			t.Errorf("isDirectoryPath(%q) = false, want true", v)
		}
	}

	modelLists := []string{
		"", "llama3.2", "gpt-oss:20b", "llama3.2, mistral",
		"library/mistral", "meta/llama-2-70b-chat",
		"deepseek-r1:8b,qwen2.5:14b",
		"/root/.ollama/models,llama3.2", // a comma means someone meant a list
	}
	for _, v := range modelLists {
		if isDirectoryPath(v) {
			t.Errorf("isDirectoryPath(%q) = true, want false", v)
		}
	}
}

// TestOllamaBuild_DropsDirectoryPathModelList is the regression test for the
// collision itself: a host running Ollama exports OLLAMA_MODELS as a directory,
// and that path used to be registered as one model id and advertised by
// /v1/models. Dropping it leaves the provider exactly as it is with the
// variable unset.
func TestOllamaBuild_DropsDirectoryPathModelList(t *testing.T) {
	entry, ok := GetProviderEntry(NameOllama)
	if !ok {
		t.Fatal("ollama provider entry not found")
	}

	p, err := entry.Build(ProviderConfig{
		CfgKeyHost:   "http://localhost:11434",
		CfgKeyModels: "/root/.ollama/models",
	})
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}

	lister, ok := p.(interface{ ConfiguredModels() []string })
	if !ok {
		t.Fatal("ollama provider does not report supported models")
	}
	for _, m := range lister.ConfiguredModels() {
		if m == "/root/.ollama/models" {
			t.Fatalf("ConfiguredModels() = %#v: a filesystem path was registered as a model id",
				lister.ConfiguredModels())
		}
	}
}

// TestOllamaModelsEnvPrecedence pins the rename. Both variables write
// CfgKeyModels and ProviderConfigFromEnv only writes non-empty values, so the
// later EnvMapping wins — FERRO_OLLAMA_MODELS must be the later one, or an
// operator who sets it on an Ollama host still gets that host's OLLAMA_MODELS
// directory path.
func TestOllamaModelsEnvPrecedence(t *testing.T) {
	entry, ok := GetProviderEntry(NameOllama)
	if !ok {
		t.Fatal("ollama provider entry not found")
	}
	t.Setenv("OLLAMA_HOST", "http://localhost:11434")

	t.Run("deprecated name still read", func(t *testing.T) {
		t.Setenv("OLLAMA_MODELS", "llama3.2")
		if got := ProviderConfigFromEnv(entry)[CfgKeyModels]; got != "llama3.2" {
			t.Errorf("models = %q, want %q: OLLAMA_MODELS must keep working this release", got, "llama3.2")
		}
	})

	t.Run("new name read", func(t *testing.T) {
		t.Setenv("FERRO_OLLAMA_MODELS", "mistral")
		if got := ProviderConfigFromEnv(entry)[CfgKeyModels]; got != "mistral" {
			t.Errorf("models = %q, want %q", got, "mistral")
		}
	})

	t.Run("new name wins over deprecated", func(t *testing.T) {
		t.Setenv("OLLAMA_MODELS", "/root/.ollama/models")
		t.Setenv("FERRO_OLLAMA_MODELS", "mistral")
		if got := ProviderConfigFromEnv(entry)[CfgKeyModels]; got != "mistral" {
			t.Errorf("models = %q, want %q: FERRO_OLLAMA_MODELS must supersede OLLAMA_MODELS", got, "mistral")
		}
	})
}
