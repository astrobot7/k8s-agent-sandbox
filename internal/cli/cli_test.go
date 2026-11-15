package cli

import (
	"testing"
	"time"

	"github.com/spf13/pflag"
)

func lookup(env map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := env[k]; return v, ok }
}

func TestBindEnv(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		args    []string
		env     map[string]string
		want    time.Duration
		wantErr bool
	}{
		{"default", nil, nil, time.Minute, false},
		{"from env", nil, map[string]string{"DEDUP_WINDOW": "5m"}, 5 * time.Minute, false},
		{"flag beats env", []string{"--dedup-window=2m"}, map[string]string{"DEDUP_WINDOW": "5m"}, 2 * time.Minute, false},
		{"empty env is unset", nil, map[string]string{"DEDUP_WINDOW": ""}, time.Minute, false},
		{"bad env value", nil, map[string]string{"DEDUP_WINDOW": "soon"}, 0, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
			d := fs.Duration("dedup-window", time.Minute, "dedup window")
			if err := fs.Parse(tc.args); err != nil {
				t.Fatal(err)
			}
			err := BindEnv(fs, lookup(tc.env))
			if (err != nil) != tc.wantErr {
				t.Fatalf("BindEnv error = %v, wantErr %v", err, tc.wantErr)
			}
			if !tc.wantErr && *d != tc.want {
				t.Errorf("dedup-window = %v, want %v", *d, tc.want)
			}
		})
	}
}

func TestEnvName(t *testing.T) {
	t.Parallel()
	if got := EnvName("remediation-enabled"); got != "REMEDIATION_ENABLED" {
		t.Errorf("EnvName = %q", got)
	}
}
