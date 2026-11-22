package domain

import (
	"encoding/json"
	"time"

	"github.com/antonyrejith/portun/internal/platform/ids"
)

// SettingRow is what one level saved for a setting.
type SettingRow struct {
	Key string
	// LevelEnvironmentID is the environment of an environment-level row; the
	// zero value means the tenant level.
	LevelEnvironmentID ids.EnvironmentID
	Mode               Mode
	Value              json.RawMessage // ModeOwn only
	Version            int64
	UpdatedBy          string
	UpdatedAt          time.Time
}

// Level is the level the row belongs to.
func (r SettingRow) Level() Level {
	if r.LevelEnvironmentID.IsZero() {
		return LevelTenant
	}
	return LevelEnvironment
}

// PlatformRow is the platform's value and policy for a setting.
type PlatformRow struct {
	Key       string
	Value     json.RawMessage
	Policy    Policy
	Version   int64
	UpdatedAt time.Time
}

// BundleRow is a stored bundle.
type BundleRow struct {
	Version   int64
	Checksum  string
	Payload   json.RawMessage
	CreatedAt time.Time
}

// Publication records one publish of an environment.
type Publication struct {
	EnvironmentID ids.EnvironmentID
	BundleVersion int64
	Checksum      string
	PublishedAt   time.Time
	PublishedBy   string
	ChangeNote    string
	// Objects lists the object versions this publish made current.
	Objects []PublishedObject
}

// PublishedObject is an object version made current by a publish.
type PublishedObject struct {
	ObjectID ids.ConfigObjectID
	Version  int
}

// CurrentSpec is the published spec in force of one active object.
type CurrentSpec struct {
	Kind string
	Name string
	Spec json.RawMessage
}
