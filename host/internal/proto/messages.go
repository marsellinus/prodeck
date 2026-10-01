package proto

// The payload shapes for every message in both directions. They live in one
// file so the wire contract can be read in one place, next to the constants it
// belongs to.
//
// Every struct tolerates unknown fields on decode (encoding/json ignores them by
// default), which is what makes the additive evolution promised in
// docs/PROTOCOL.md §11 safe in practice.

// HelloPayload is the client's first frame.
type HelloPayload struct {
	DeviceID string     `json:"device_id"`
	Token    string     `json:"token"`
	Client   ClientInfo `json:"client,omitempty"`
	Resume   ResumeInfo `json:"resume,omitempty"`
}

// ClientInfo describes the connecting application.
type ClientInfo struct {
	Platform   string      `json:"platform,omitempty"`
	AppVersion string      `json:"app_version,omitempty"`
	Model      string      `json:"model,omitempty"`
	Screen     *ScreenInfo `json:"screen,omitempty"`
	Encoding   string      `json:"encoding,omitempty"`
}

// ScreenInfo is the client's display geometry, used by the host to warn about a
// grid that will not fit.
type ScreenInfo struct {
	Width       int     `json:"w"`
	Height      int     `json:"h"`
	Density     float64 `json:"density,omitempty"`
	Orientation string  `json:"orientation,omitempty"`
}

// ResumeInfo lets a reconnecting client land back where it was.
type ResumeInfo struct {
	ProfileID string `json:"profile_id,omitempty"`
	PageID    string `json:"page_id,omitempty"`
}

// WelcomePayload is the host's answer to hello.
type WelcomePayload struct {
	SessionID           string     `json:"session_id"`
	Host                HostInfo   `json:"host"`
	Device              DeviceInfo `json:"device"`
	ServerTime          int64      `json:"server_time"`
	HeartbeatIntervalMS int        `json:"heartbeat_interval_ms"`
	IdleTimeoutMS       int        `json:"idle_timeout_ms"`
	Features            []string   `json:"features"`
	ProfilesRevision    string     `json:"profiles_revision"`
}

// HostInfo identifies the host.
type HostInfo struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	OS      string `json:"os"`
	Version string `json:"version"`
}

// DeviceInfo identifies the authenticated device and what it may do.
type DeviceInfo struct {
	ID     string   `json:"id"`
	Name   string   `json:"name"`
	Scopes []string `json:"scopes"`
}

// PingPayload is the heartbeat body.
type PingPayload struct {
	Echo string `json:"echo,omitempty"`
}

// PongPayload answers a ping.
type PongPayload struct {
	Echo string `json:"echo,omitempty"`
}

// --- profiles ------------------------------------------------------------

// ProfileListPayload requests the profile list.
type ProfileListPayload struct{}

// ProfileSummary is one entry in the list.
type ProfileSummary struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Icon     any      `json:"icon,omitempty"`
	Pages    []string `json:"pages"`
	Revision string   `json:"revision"`
}

// ProfileListResultPayload answers profile.list.
type ProfileListResultPayload struct {
	Profiles []ProfileSummary `json:"profiles"`
	Active   string           `json:"active,omitempty"`
}

// ProfileGetPayload requests one profile document.
type ProfileGetPayload struct {
	ProfileID string `json:"profile_id"`
}

// ProfileGetResultPayload carries one profile document.
//
// Profile is `any` rather than a typed struct on purpose: the host must be able
// to serve a document that a newer schema produced, and re-encoding it through
// a struct in this package would silently drop the fields this build does not
// know about.
type ProfileGetResultPayload struct {
	Profile  any    `json:"profile"`
	Revision string `json:"revision"`
}

// ProfileSetActivePayload selects the active profile.
type ProfileSetActivePayload struct {
	ProfileID string `json:"profile_id"`
}

// ProfileReloadPayload re-reads a profile from disk.
type ProfileReloadPayload struct {
	ProfileID string `json:"profile_id"`
}

// ProfileExportResultPayload carries a profile as a JSON string.
type ProfileExportResultPayload struct {
	ProfileID string `json:"profile_id"`
	JSON      string `json:"json"`
}

// ProfileImportPayload restores a profile from a JSON string.
type ProfileImportPayload struct {
	JSON      string `json:"json"`
	Overwrite bool   `json:"overwrite,omitempty"`
}

// EventProfileChangedPayload announces a hot reload.
type EventProfileChangedPayload struct {
	ProfileID string `json:"profile_id"`
	Revision  string `json:"revision"`
}

// --- buttons -------------------------------------------------------------

// ButtonPressPayload is a button press.
type ButtonPressPayload struct {
	ProfileID string    `json:"profile_id"`
	PageID    string    `json:"page_id"`
	ButtonID  string    `json:"button_id"`
	Press     PressInfo `json:"press,omitempty"`
}

// PressInfo distinguishes a tap from a long press. The client decides which one
// happened; the host never duplicates gesture detection, because two
// implementations of a 400 ms threshold would disagree.
type PressInfo struct {
	Kind  string `json:"kind,omitempty"` // short | long | repeat
	Count int    `json:"count,omitempty"`
}

// ButtonReleasePayload reports a release, sent only for buttons that declare an
// on_release action.
type ButtonReleasePayload struct {
	ProfileID string `json:"profile_id"`
	PageID    string `json:"page_id"`
	ButtonID  string `json:"button_id"`
	HeldMS    int    `json:"held_ms,omitempty"`
}

// ActionResultPayload reports the outcome of an action.
type ActionResultPayload struct {
	ExecutionID string       `json:"execution_id"`
	ActionID    string       `json:"action_id,omitempty"`
	ActionType  string       `json:"action_type,omitempty"`
	OK          bool         `json:"ok"`
	Accepted    bool         `json:"accepted,omitempty"`
	DurationMS  int64        `json:"duration_ms,omitempty"`
	Output      any          `json:"output,omitempty"`
	Error       *ActionError `json:"error,omitempty"`
}

// ActionError describes a failed action.
type ActionError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ActionCancelPayload cancels a running execution.
type ActionCancelPayload struct {
	ExecutionID string `json:"execution_id"`
}

// EventButtonStatePayload carries a state change for one button.
type EventButtonStatePayload struct {
	ProfileID string   `json:"profile_id"`
	PageID    string   `json:"page_id"`
	ButtonID  string   `json:"button_id"`
	State     StateVal `json:"state"`
}

// StateVal is the client-visible state of a button.
type StateVal struct {
	Type     string   `json:"type,omitempty"`
	Label    string   `json:"label,omitempty"`
	Color    string   `json:"color,omitempty"`
	Progress *float64 `json:"progress,omitempty"`
	Value    *float64 `json:"value,omitempty"`
	Active   *bool    `json:"active,omitempty"`
}

// --- telemetry -----------------------------------------------------------

// TelemetrySubscribePayload subscribes to the metric stream.
type TelemetrySubscribePayload struct {
	Metrics    []string `json:"metrics,omitempty"`
	IntervalMS int      `json:"interval_ms,omitempty"`
}

// EventTelemetryPayload is one telemetry sample.
type EventTelemetryPayload struct {
	TS     int64              `json:"ts"`
	Values map[string]float64 `json:"values"`
}

// --- server-initiated ----------------------------------------------------

// OpenPagePayload tells a client to navigate.
type OpenPagePayload struct {
	ProfileID string `json:"profile_id,omitempty"`
	PageID    string `json:"page_id"`
}

// ChangeProfilePayload tells a client to switch profile.
type ChangeProfilePayload struct {
	ProfileID string `json:"profile_id"`
}

// NotifyPayload shows a transient message on a client.
type NotifyPayload struct {
	Message string `json:"message"`
	Level   string `json:"level"`
}
