package main

import (
	"os"
	"path/filepath"
	"testing"

	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

func TestResolveOperatorNamespace(t *testing.T) {
	tests := []struct {
		name     string
		envValue *string // nil = unset, "" = set but empty
		fileBody string  // written to a temp file; empty string means no file
		want     string
	}{
		{
			name:     "env var set",
			envValue: strPtr("operator-ns"),
			want:     "operator-ns",
		},
		{
			name:     "env var takes precedence over file",
			envValue: strPtr("from-env"),
			fileBody: "from-file",
			want:     "from-env",
		},
		{
			name:     "falls back to SA file when env unset",
			fileBody: "sa-namespace\n",
			want:     "sa-namespace",
		},
		{
			name:     "trims whitespace from SA file",
			fileBody: "  my-ns \n",
			want:     "my-ns",
		},
		{
			name:     "empty env var ignored, falls back to file",
			envValue: strPtr(""),
			fileBody: "fallback-ns",
			want:     "fallback-ns",
		},
		{
			name: "returns empty when nothing available",
			want: "",
		},
		{
			name:     "empty SA file returns empty",
			fileBody: "   \n",
			want:     "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("POD_NAMESPACE", "")
			os.Unsetenv("POD_NAMESPACE")

			if tt.envValue != nil {
				t.Setenv("POD_NAMESPACE", *tt.envValue)
			}

			saFile := filepath.Join(t.TempDir(), "namespace")
			if tt.fileBody != "" {
				if err := os.WriteFile(saFile, []byte(tt.fileBody), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			got := resolveOperatorNamespace(saFile)
			if got != tt.want {
				t.Errorf("resolveOperatorNamespace() = %q, want %q", got, tt.want)
			}
		})
	}
}

func strPtr(s string) *string { return &s }

func TestResolveCurrentNamespace(t *testing.T) {
	tests := []struct {
		name      string
		clientCfg *clientcmdapi.Config
		want      string
	}{
		{
			name:      "nil config",
			clientCfg: nil,
			want:      "default",
		},
		{
			name:      "empty config",
			clientCfg: clientcmdapi.NewConfig(),
			want:      "default",
		},
		{
			name: "unknown current context",
			clientCfg: &clientcmdapi.Config{
				CurrentContext: "missing",
				Contexts: map[string]*clientcmdapi.Context{
					"other": {Namespace: "other-ns"},
				},
			},
			want: "default",
		},
		{
			name: "nil context",
			clientCfg: &clientcmdapi.Config{
				CurrentContext: "broken",
				Contexts:       map[string]*clientcmdapi.Context{"broken": nil},
			},
			want: "default",
		},
		{
			name: "context without namespace",
			clientCfg: &clientcmdapi.Config{
				CurrentContext: "ctx",
				Contexts:       map[string]*clientcmdapi.Context{"ctx": {}},
			},
			want: "default",
		},
		{
			name: "context with namespace",
			clientCfg: &clientcmdapi.Config{
				CurrentContext: "ctx",
				Contexts:       map[string]*clientcmdapi.Context{"ctx": {Namespace: "my-ns"}},
			},
			want: "my-ns",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveCurrentNamespace(tt.clientCfg)
			if got != tt.want {
				t.Errorf("resolveCurrentNamespace() = %q, want %q", got, tt.want)
			}
		})
	}
}
