package core

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func archiveFixture(t *testing.T, names []string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for _, name := range names {
		file, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write([]byte("fictional test data for " + name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func fixture(t *testing.T) (*Manager, *[]byte, *githubRelease) {
	t.Helper()
	m, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	data := archiveFixture(t, []string{"xray", "geoip.dat", "geosite.dat", "LICENSE"})
	h := sha256.Sum256(data)
	r := githubRelease{Tag: "v" + DefaultVersion, Prerelease: true, URL: repository + "/releases/tag/v" + DefaultVersion,
		Assets: []asset{{Name: m.asset, Size: int64(len(data)), Digest: "sha256:" + hex.EncodeToString(h[:]), URL: repository + "/releases/download/v" + DefaultVersion + "/" + m.asset}}}
	m.client = &http.Client{Transport: roundTrip(func(req *http.Request) (*http.Response, error) {
		var body []byte
		switch {
		case strings.HasPrefix(req.URL.String(), apiBase+"/releases/tags/"):
			body, _ = json.Marshal(r)
		case req.URL.String() == apiBase+"/releases?per_page=50":
			body, _ = json.Marshal([]githubRelease{r})
		case req.URL.String() == r.Assets[0].URL:
			body = data
		default:
			return nil, errors.New("unexpected URL: " + req.URL.String())
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(body)), Header: http.Header{}}, nil
	})}
	m.probe = func(context.Context, string, string) error { return nil }
	return m, &data, &r
}

func TestNormalizeVersion(t *testing.T) {
	for _, input := range []string{"26.7.28", " v26.7.28 "} {
		if result, err := NormalizeVersion(input); err != nil || result != DefaultVersion {
			t.Fatalf("%q: %s %v", input, result, err)
		}
	}
	for _, input := range []string{"latest", "../26.7.28", "26.7.28;id", "26.7.28-beta", "https://other.test/core", "", "v26.7.28/x"} {
		if _, err := NormalizeVersion(input); err == nil {
			t.Errorf("accepted %q", input)
		}
	}
}

func TestPlatformMapping(t *testing.T) {
	for _, platform := range []string{"darwin/arm64", "darwin/amd64", "linux/amd64", "linux/arm64"} {
		parts := strings.Split(platform, "/")
		if _, err := assetName(parts[0], parts[1]); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := assetName("linux", "unknown"); err == nil {
		t.Fatal("unknown platform accepted")
	}
}

func TestOfficialURLRestriction(t *testing.T) {
	for _, value := range []string{"http://github.com/a", "https://github.com.evil.test/a", "https://user@github.com/a", "https://github.com:443/a", "https://evil.test/a"} {
		u, _ := url.Parse(value)
		if officialURL(u, false) {
			t.Fatal("accepted " + value)
		}
	}
	u, _ := url.Parse("https://release-assets.githubusercontent.com/asset")
	if !officialURL(u, false) {
		t.Fatal("official CDN rejected")
	}
	if newHTTPClient().CheckRedirect(&http.Request{URL: &url.URL{Scheme: "https", Host: "evil.test"}}, nil) == nil {
		t.Fatal("untrusted redirect accepted")
	}
}

func TestReleaseListPreservesDefaultAndPrerelease(t *testing.T) {
	m, _, _ := fixture(t)
	list, err := m.Releases(context.Background())
	if err != nil || len(list) != 1 || list[0].Version != DefaultVersion || !list[0].Prerelease {
		t.Fatalf("%v %v", list, err)
	}
}

func TestReleaseListAddsDefaultOutsideRecentList(t *testing.T) {
	m, _, r := fixture(t)
	transport := m.client.Transport
	m.client.Transport = roundTrip(func(req *http.Request) (*http.Response, error) {
		if req.URL.RawQuery != "" {
			item := *r
			item.Tag = "v26.10.17"
			item.URL = repository + "/releases/tag/v26.10.17"
			body, _ := json.Marshal([]githubRelease{item})
			return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(body))}, nil
		}
		return transport.RoundTrip(req)
	})
	list, err := m.Releases(context.Background())
	if err != nil || len(list) != 2 || list[1].Version != DefaultVersion {
		t.Fatalf("%v %v", list, err)
	}
	if m.Status().Selected != DefaultVersion {
		t.Fatal("list changed selected version")
	}
}

func TestInstallAndRestartPersistence(t *testing.T) {
	m, _, _ := fixture(t)
	if s := m.Status(); s.Selected != DefaultVersion || len(s.Installed) != 0 {
		t.Fatalf("%+v", s)
	}
	if err := m.Install(context.Background(), DefaultVersion); err != nil {
		t.Fatal(err)
	}
	s := m.Status()
	if len(s.Installed) != 1 || s.Selected != DefaultVersion || s.Runtime != "not-configured" {
		t.Fatalf("%+v", s)
	}
	for _, name := range []string{"selection.json", "v" + DefaultVersion + "/receipt.json"} {
		info, err := os.Stat(filepath.Join(m.root, name))
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("unsafe mode %s: %v", name, err)
		}
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewManager(m.root)
	if err != nil || restarted.Status().Selected != DefaultVersion || len(restarted.Status().Installed) != 1 {
		t.Fatalf("restart %v", err)
	}
	t.Cleanup(func() { _ = restarted.Close() })
}

func TestInstallRejectsInvalidMetadataBeforeDownload(t *testing.T) {
	for _, change := range []string{"missing-digest", "bad-digest", "wrong-url", "wrong-tag", "missing-asset", "oversize"} {
		t.Run(change, func(t *testing.T) {
			m, _, r := fixture(t)
			switch change {
			case "missing-digest":
				r.Assets[0].Digest = ""
			case "bad-digest":
				r.Assets[0].Digest = "sha256:zz"
			case "wrong-url":
				r.Assets[0].URL = "https://evil.test/core.zip"
			case "wrong-tag":
				r.Tag = "v26.10.17"
			case "missing-asset":
				r.Assets[0].Name = "other.zip"
			case "oversize":
				r.Assets[0].Size = maxArchive + 1
			}
			if err := m.Install(context.Background(), DefaultVersion); err == nil {
				t.Fatal("invalid metadata accepted")
			}
			if len(m.Status().Installed) != 0 {
				t.Fatal("invalid package installed")
			}
		})
	}
}

func TestChecksumFailureDoesNotSelect(t *testing.T) {
	m, data, _ := fixture(t)
	(*data)[0] ^= 1
	if err := m.Install(context.Background(), DefaultVersion); err == nil || !strings.Contains(err.Error(), "SHA256") {
		t.Fatalf("%v", err)
	}
	if _, err := os.Stat(filepath.Join(m.root, "selection.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("selection written on failure")
	}
}

func TestBadArchiveRejected(t *testing.T) {
	for _, names := range [][]string{{"../xray"}, {"/xray"}, {"xray", "xray"}, {"xray"}, {"a\\xray"}} {
		t.Run(strings.Join(names, "_"), func(t *testing.T) {
			archive := archiveFixture(t, names)
			file := filepath.Join(t.TempDir(), "fixture.zip")
			if err := os.WriteFile(file, archive, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := extractArchive(file, t.TempDir()); err == nil {
				t.Fatal("unsafe archive accepted")
			}
		})
	}
}

func TestProbeFailureRetainsSelection(t *testing.T) {
	m, _, _ := fixture(t)
	m.probe = func(context.Context, string, string) error { return errors.New("test probe failure") }
	if m.Install(context.Background(), DefaultVersion) == nil {
		t.Fatal("failed probe accepted")
	}
	if len(m.Status().Installed) != 0 {
		t.Fatal("failed probe installed")
	}
}

func TestSwitchPreviousAndOfflineReuse(t *testing.T) {
	m, _, r := fixture(t)
	if err := m.Install(context.Background(), DefaultVersion); err != nil {
		t.Fatal(err)
	}
	r.Tag, r.URL = "v26.8.3", repository+"/releases/tag/v26.8.3"
	r.Assets[0].URL = repository + "/releases/download/v26.8.3/" + m.asset
	if err := m.Install(context.Background(), "26.8.3"); err != nil {
		t.Fatal(err)
	}
	if s := m.Status(); s.Previous != DefaultVersion || len(s.Installed) != 2 {
		t.Fatalf("%+v", s)
	}
	m.client.Transport = roundTrip(func(*http.Request) (*http.Response, error) { return nil, errors.New("offline") })
	if err := m.Install(context.Background(), DefaultVersion); err != nil {
		t.Fatal(err)
	}
	if s := m.Status(); s.Selected != DefaultVersion || s.Previous != "26.8.3" || len(s.Installed) != 2 {
		t.Fatalf("%+v", s)
	}
}

func TestTamperedInstallNotReusedOrOverwritten(t *testing.T) {
	m, _, _ := fixture(t)
	if err := m.Install(context.Background(), DefaultVersion); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(m.root, "v"+DefaultVersion, "geoip.dat")
	if err := os.WriteFile(file, []byte("tampered fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := m.Install(context.Background(), DefaultVersion); err == nil {
		t.Fatal("tampered package reused")
	}
	if s := m.Status(); len(s.Installed) != 0 {
		t.Fatalf("%+v", s)
	}
	if contents, _ := os.ReadFile(file); string(contents) != "tampered fixture" {
		t.Fatal("existing files overwritten")
	}
}

func TestConcurrentInstallRejected(t *testing.T) {
	m, _, _ := fixture(t)
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	m.probe = func(context.Context, string, string) error { once.Do(func() { close(started) }); <-release; return nil }
	done := make(chan error, 1)
	go func() { done <- m.Install(context.Background(), DefaultVersion) }()
	<-started
	if !m.Status().Busy {
		t.Fatal("busy state not visible")
	}
	if err := m.Install(context.Background(), DefaultVersion); !errors.Is(err, ErrBusy) {
		t.Fatalf("%v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

type selectionGate struct {
	sync.Mutex
	entered chan struct{}
	once    sync.Once
}

func (g *selectionGate) Lock() {
	g.once.Do(func() { close(g.entered) })
	g.Mutex.Lock()
}

func TestInstallSelectionCommitLock(t *testing.T) {
	for _, kind := range []string{"new", "installed", "cancelled", "save-failure"} {
		t.Run(kind, func(t *testing.T) {
			m, _, release := fixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := m.Install(ctx, DefaultVersion); err != nil {
				t.Fatal(err)
			}
			const next = "26.8.3"
			release.Tag, release.URL = "v"+next, repository+"/releases/tag/v"+next
			release.Assets[0].URL = repository + "/releases/download/v" + next + "/" + m.asset
			if kind == "installed" {
				if err := m.Install(ctx, next); err != nil {
					t.Fatal(err)
				}
				if err := m.Install(ctx, DefaultVersion); err != nil {
					t.Fatal(err)
				}
				m.client.Transport = roundTrip(func(*http.Request) (*http.Response, error) { return nil, errors.New("offline") })
			}
			if kind == "save-failure" {
				if err := os.Rename(filepath.Join(m.root, "selection.json"), filepath.Join(m.root, "selection.saved")); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(filepath.Join(m.root, "selection.json"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			gate := &selectionGate{entered: make(chan struct{})}
			gate.Mutex.Lock()
			var unlock sync.Once
			defer unlock.Do(gate.Unlock)
			done := make(chan error, 1)
			go func() { done <- m.InstallWithSelectionLock(ctx, next, gate) }()
			select {
			case <-gate.entered:
			case <-ctx.Done():
				t.Fatal("install did not reach selection commit")
			}
			if m.installed(next) != nil || m.Selected() != DefaultVersion || !m.Status().Busy {
				t.Error("download not complete outside lock, or selection committed before lock")
			}
			if err := m.Install(ctx, DefaultVersion); !errors.Is(err, ErrBusy) {
				t.Error("concurrent install accepted during selection commit")
			}
			if kind == "cancelled" {
				cancel()
			}
			unlock.Do(gate.Unlock)
			err := <-done
			if kind == "cancelled" || kind == "save-failure" {
				if err == nil || m.Selected() != DefaultVersion || (kind == "cancelled" && !errors.Is(err, context.Canceled)) {
					t.Fatal("failed selection changed current choice or hid error", err)
				}
			} else if err != nil || m.Selected() != next || m.Status().Previous != DefaultVersion {
				t.Fatal("selection did not commit with previous version preserved", err)
			}
			if m.Status().Busy || m.installed(DefaultVersion) != nil || m.installed(next) != nil {
				t.Fatal("install left busy state or lost retained core")
			}
			if kind != "save-failure" {
				data, err := os.ReadFile(filepath.Join(m.root, "selection.json"))
				var saved selection
				if err != nil || json.Unmarshal(data, &saved) != nil || saved.Selected != m.Selected() {
					t.Fatal("persisted choice differs from memory")
				}
			}
		})
	}
}

func TestCorruptSelectionRefusesSilentReset(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "selection.json"), []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewManager(root); err == nil {
		t.Fatal("corrupt selection silently reset")
	}
}

func TestDirectoryLockRejectsSecondManager(t *testing.T) {
	m, _, _ := fixture(t)
	if second, err := NewManager(m.root); err == nil {
		second.Close()
		t.Fatal("two processes may manage the same directory")
	}
}

func TestNetworkFailureKeepsInstalledChoice(t *testing.T) {
	m, _, _ := fixture(t)
	if err := m.Install(context.Background(), DefaultVersion); err != nil {
		t.Fatal(err)
	}
	m.client.Transport = roundTrip(func(*http.Request) (*http.Response, error) { return nil, errors.New("offline") })
	if err := m.Install(context.Background(), "26.8.3"); err == nil {
		t.Fatal("network failure ignored")
	}
	if s := m.Status(); s.Selected != DefaultVersion || len(s.Installed) != 1 {
		t.Fatalf("%+v", s)
	}
}
