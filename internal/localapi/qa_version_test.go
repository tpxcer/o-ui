package localapi

import (
	"errors"
	"os"
	"testing"

	"independent-panel/internal/core"
)

func qaVersion(value string) (string, error) {
	switch value {
	case "", core.DefaultVersion:
		return core.DefaultVersion, nil
	case "26.10.17":
		return value, nil
	default:
		return "", errors.New("测试版本只允许26.7.28或已获准的26.10.17")
	}
}

func runtimeQAVersion(t *testing.T) string {
	t.Helper()
	version, err := qaVersion(os.Getenv("PANEL_RUNTIME_QA_VERSION"))
	if err != nil {
		t.Fatal(err)
	}
	return version
}

func TestRuntimeQAVersion(t *testing.T) {
	for _, value := range []string{"", core.DefaultVersion, "26.10.17", "latest", "v26.10.17", "26.10.17\n", "26.9.9"} {
		version, err := qaVersion(value)
		allowed := value == "" || value == core.DefaultVersion || value == "26.10.17"
		want := value
		if value == "" {
			want = core.DefaultVersion
		}
		if (err == nil) != allowed || allowed && version != want {
			t.Fatalf("unexpected QA version decision for %q", value)
		}
	}
	if core.DefaultVersion != "26.7.28" {
		t.Fatal("candidate tests changed the product default")
	}
}
