package songloft

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	appplatform "github.com/waygu-music-plugin/sdk/pluginapi"
	backend "github.com/waygu-music-plugin/songloft/internal/client"
)

type RuntimeAdapter struct {
	state      *State
	settingsMu sync.Mutex
	settings   *platformPreferences
	persist    func()
}

// forgetCredentials is replaced in tests so a test sign-out stays local.
var forgetCredentials = ClearCredentials

func NewRuntime(state *State, settings *platformPreferences, save ...func()) *RuntimeAdapter {
	var persist func()
	if len(save) > 0 {
		persist = save[0]
	}
	return &RuntimeAdapter{
		state:    state,
		settings: settings,
		persist:  persist,
	}
}

func (r *RuntimeAdapter) Config() connectionConfig {
	if r.state == nil {
		return connectionConfig{}
	}
	return toConnectionConfig(r.state.Config())
}

func (r *RuntimeAdapter) SetConfig(cfg connectionConfig) {
	if r.state == nil {
		return
	}
	r.settingsMu.Lock()
	defer r.settingsMu.Unlock()
	r.state.SetConfig(fromConnectionConfig(cfg))
	r.saveConfigLocked(cfg)
}

func (r *RuntimeAdapter) saveConfigLocked(cfg connectionConfig) {
	if r.settings != nil {
		r.settings.Config = cfg
		if r.persist != nil {
			r.persist()
		}
	}
}

func (r *RuntimeAdapter) Configured() bool {
	if r.state == nil {
		return false
	}
	cfg := r.state.Config()
	return cfg.Address != "" && cfg.Username != "" && cfg.Password != ""
}

func (r *RuntimeAdapter) Connected() bool {
	return r.state != nil && r.state.Connected()
}

func (r *RuntimeAdapter) Connecting() bool {
	return r.state != nil && r.state.Connecting()
}

func (r *RuntimeAdapter) Error() error {
	if r.state == nil {
		return nil
	}
	return r.state.Error()
}

func (r *RuntimeAdapter) Connect(ctx context.Context, cfg connectionConfig) error {
	if r.state == nil {
		return backend.ErrNotConnected
	}
	bcfg := fromConnectionConfig(cfg)
	if bcfg.Password == "" {
		LoadCredentials(&bcfg)
	}
	gen, err := r.state.connect(ctx, bcfg)
	if err != nil {
		return err
	}
	r.settingsMu.Lock()
	defer r.settingsMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if r.state.Generation() != gen {
		return context.Canceled
	}
	_ = SaveCredentials(bcfg)
	r.saveConfigLocked(toConnectionConfig(bcfg))
	return nil
}

func (r *RuntimeAdapter) Disconnect(ctx context.Context) error {
	if r.state == nil {
		return nil
	}
	r.settingsMu.Lock()
	defer r.settingsMu.Unlock()
	err := r.state.disconnect(true)
	_ = forgetCredentials()
	r.saveConfigLocked(connectionConfig{})
	return err
}

func (r *RuntimeAdapter) Cancel(context.Context) error {
	r.settingsMu.Lock()
	defer r.settingsMu.Unlock()
	return r.state.Disconnect()
}

func (r *RuntimeAdapter) FetchArtwork(ctx context.Context, raw string, size int) ([]byte, bool, error) {
	if r.state == nil {
		return nil, false, backend.ErrNotConnected
	}
	provider := r.state.Provider()
	if provider == nil {
		return nil, false, backend.ErrNotConnected
	}
	if !strings.HasPrefix(raw, backend.CoverScheme) &&
		!strings.HasPrefix(raw, backend.PlaylistCoverScheme) &&
		!strings.HasPrefix(raw, backend.FacetCoverScheme) {
		return nil, false, nil
	}
	data, err := provider.Cover(ctx, raw, size)
	return data, len(data) > 0, err
}

func (r *RuntimeAdapter) SetSort(key, value string) {
	if r.settings == nil {
		return
	}
	r.settingsMu.Lock()
	defer r.settingsMu.Unlock()
	if r.settings.Sorts == nil {
		r.settings.Sorts = make(map[string]string)
	}
	r.settings.Sorts[key] = value
	if r.persist != nil {
		r.persist()
	}
}

func toConnectionConfig(cfg backend.Config) connectionConfig {
	return connectionConfig{
		Address:  cfg.Address,
		Username: cfg.Username,
		PassHash: cfg.Password,
		DeviceID: cfg.DeviceID,
	}
}

func fromConnectionConfig(cfg connectionConfig) backend.Config {
	return backend.Config{
		Address:  cfg.Address,
		Username: cfg.Username,
		Password: cfg.PassHash,
		DeviceID: cfg.DeviceID,
	}
}

func (a *Adapter) connectOnActivation(event appplatform.Activation, playback appplatform.PlaybackContinuity) {
	rt := a.runtime
	if !rt.Configured() {
		return
	}
	restore := func() {
		a.home.host.Post(func() {
			if playback != nil && event.Context.Err() == nil && a.home.host.Playback().Current() == nil {
				if session, ok := playback.LoadSession(); ok {
					playback.Restore(session)
				}
			}
		})
	}
	if !event.ColdStart && rt.Connected() {
		restore()
		return
	}
	cfg := rt.Config()
	go func() {
		if !event.ColdStart {
			if rt.Connect(event.Context, cfg) == nil {
				restore()
			}
			return
		}
		err := appplatform.Retry(event.Context, []time.Duration{0, 1500 * time.Millisecond, 3 * time.Second}, func(ctx context.Context) error {
			return rt.Connect(ctx, cfg)
		})
		if err != nil && !errors.Is(err, context.Canceled) {
			a.home.host.Post(func() {
				if event.Context.Err() == nil {
					a.home.host.Toast(err.Error())
				}
			})
		}
		if err == nil {
			restore()
		}
	}()
}
