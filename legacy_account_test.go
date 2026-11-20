package songloft

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo"

	"github.com/waygu-music-plugin/sdk/pluginapi"
	"github.com/waygu-music-plugin/sdk/plugintest"
)

// Activation reconnects with the existing private credential and omits it from saved preferences.
func TestLegacyCredentialReconnectsAtStartup(t *testing.T) {
	t.Setenv("MYGO_ENV", "development")
	dir := t.TempDir()
	mygo.App.SetPath(mygo.PathUserData, dir)
	t.Cleanup(func() { mygo.App.SetPath(mygo.PathUserData, "") })
	if err := os.WriteFile(filepath.Join(dir, string(ID)+"-credentials"), []byte(`{"password":"saved-songloft-password"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		legacySongloftServer(w, r)

	}))
	t.Cleanup(server.Close)
	h := plugintest.NewHost(ID)
	prefs := &platformPreferences{Config: connectionConfig{Address: server.URL, Username: "saved-user"}}
	type snapshot struct {
		data []byte
		err  error
	}
	saved := make(chan snapshot, 1)
	h.Save = func() {
		var data json.RawMessage
		err := h.Preferences.Load(&data)
		select {
		case saved <- snapshot{data, err}:
		default:
		}
	}
	if adapter := h.Start(Registration(), prefs); !adapter.Configured() {
		t.Fatal("did not load the saved account")
	}
	ctx := h.Activate(t)
	eventCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	h.Lifecycle.Ready(pluginapi.Activation{Context: eventCtx, ColdStart: true})
	var persistedSettings snapshot
	select {
	case persistedSettings = <-saved:
	case <-eventCtx.Done():
		t.Fatal("startup did not reconnect with the saved credential")
	}

	if persistedSettings.err != nil {
		t.Fatal(persistedSettings.err)
	}
	var persisted struct{ Config map[string]any }
	if err := json.Unmarshal(persistedSettings.data, &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.Config["address"] != server.URL || persisted.Config["username"] != "saved-user" {
		t.Fatalf("persisted connection did not match the saved account: %s", persistedSettings.data)
	}
	for _, secret := range []string{"passHash", "accessCode", "password"} {
		if _, ok := persisted.Config[secret]; ok {
			t.Fatalf("%s was written into the public preferences", secret)
		}
	}
}

// Disconnecting during the initial playlist fetch used to save the disconnected account again.
func TestDisconnectRevokesSongloftConnectionBeingSaved(t *testing.T) {
	t.Setenv("MYGO_ENV", "development")
	dir := t.TempDir()
	mygo.App.SetPath(mygo.PathUserData, dir)
	t.Cleanup(func() { mygo.App.SetPath(mygo.PathUserData, "") })
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	loading, release := make(chan struct{}), make(chan struct{})
	var playlistRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/api/v1/playlists" && playlistRequests.Add(1) == 2 {
			close(loading)
			select {
			case <-release:
			case <-req.Context().Done():
			}
		}
		legacySongloftServer(w, req)
	}))
	t.Cleanup(server.Close)
	settings := &platformPreferences{}
	runtime := NewRuntime(NewState(), settings)
	result := make(chan error, 1)
	go func() {
		result <- runtime.Connect(ctx, connectionConfig{
			Address: server.URL, Username: "saved-user", PassHash: "saved-songloft-password",
		})
	}()
	select {
	case <-loading:
	case <-ctx.Done():
		t.Fatal("connection did not reach the playlist request")
	}
	if !runtime.Connected() {
		t.Fatal("the first library fetch had not published the connection")
	}
	if err := runtime.Disconnect(ctx); err != nil {
		t.Fatal(err)
	}
	close(release)
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("the retired connection finished with %v", err)
		}
	case <-ctx.Done():
		t.Fatal("connection did not finish")
	}
	if runtime.Configured() || settings.Config != (connectionConfig{}) {
		t.Error("the retired connection restored the account settings")
	}
	if _, err := os.Stat(filepath.Join(dir, string(ID)+"-credentials")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the retired connection restored the password: %v", err)
	}
}

func legacySongloftServer(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/api/v1/auth/login" {
		var account map[string]string
		if json.NewDecoder(r.Body).Decode(&account) != nil || account["username"] != "saved-user" || account["password"] != "saved-songloft-password" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		io.WriteString(w, `{"access_token":"songloft-session","expires_in":3600}`)
		return
	}
	io.WriteString(w, `{}`)
}
