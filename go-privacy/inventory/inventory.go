// Package inventory is the data inventory: one machine-readable record per
// app of what personal data it holds, where, why, for how long, who else
// receives it, and which cookies and browser storage keys it sets. It is the
// GDPR Art. 30 record of processing and the cookie registry, kept as code so
// it cannot drift from the app: CI checks it against the app's database and
// config, and the generator turns it into the sections of the legal
// documents.
//
// It describes software, and decides nothing legal. Wherever a legal answer
// is needed (a legal basis, a transfer mechanism, a retention period nobody
// has chosen) the value is TodoLegal, and wherever a fact could not be
// confirmed from the code it is Unknown. Neither is ever guessed.
package inventory

// SchemaVersion is the version of the file format this package reads.
const SchemaVersion = 1

const (
	// TodoLegal is the placeholder for a value a qualified person decides.
	TodoLegal = "TODO-LEGAL"
	// Unknown marks a fact that was not confirmed from the code or config.
	Unknown = "unknown"
	// Indefinite is a retention period of "kept until something deletes it".
	Indefinite = "indefinite"
	// NoMechanism is the retention mechanism of data nothing ever deletes.
	NoMechanism = "none"
)

// Inventory is one app's data inventory.
type Inventory struct {
	SchemaVersion int    `yaml:"schema_version"`
	App           string `yaml:"app"`

	Purposes         []Purpose         `yaml:"purposes"`
	DataCategories   []DataCategory    `yaml:"data_categories"`
	Tables           []Table           `yaml:"tables"`
	Processors       []Processor       `yaml:"processors"`
	InternalServices []InternalService `yaml:"internal_services"`
	Storage          []StorageKey      `yaml:"storage"`
	RetentionClasses []RetentionClass  `yaml:"retention_classes"`
}

// PurposeKind says how a purpose is shown to a person.
type PurposeKind string

const (
	// CookieCategory purposes are consented to in the banner (necessary
	// ones are disclosed, not consented to).
	CookieCategory PurposeKind = "cookie_category"
	// Processing purposes have a consent flow of their own.
	Processing PurposeKind = "processing"
	// Operational purposes are what the service does to run at all; nothing
	// is asked.
	Operational PurposeKind = "operational"
)

// Purpose is a reason data is used. Its ID is the one in the app's consent
// registry where it has one.
type Purpose struct {
	ID          string      `yaml:"id"`
	Kind        PurposeKind `yaml:"kind"`
	Description string      `yaml:"description"`
	// LegalBasis is TodoLegal until a qualified person fills it in.
	LegalBasis string `yaml:"legal_basis"`
}

// SpecialCategory marks data under GDPR Art. 9.
type SpecialCategory string

const (
	NotSpecial SpecialCategory = ""
	Health     SpecialCategory = "health"
)

// StoreKind is the kind of place data is kept.
type StoreKind string

const (
	StoreTable       StoreKind = "table"
	StoreObjectStore StoreKind = "object_store"
	StoreLogs        StoreKind = "logs"
	StoreMetrics     StoreKind = "metrics"
	StoreBackups     StoreKind = "backups"
	StoreBrowser     StoreKind = "browser"
	// StoreMemory is process memory only, never written to disk.
	StoreMemory StoreKind = "memory"
	// StoreWorkflow is a workflow engine's history.
	StoreWorkflow StoreKind = "workflow_history"
)

// Store is one place a data category is kept.
type Store struct {
	Kind StoreKind `yaml:"kind"`
	Name string    `yaml:"name"`
	// Note is free text for what is notable about this copy.
	Note string `yaml:"note,omitempty"`
}

// DataCategory is a kind of data the app holds.
type DataCategory struct {
	ID              string          `yaml:"id"`
	Description     string          `yaml:"description"`
	SpecialCategory SpecialCategory `yaml:"special_category,omitempty"`
	Purposes        []string        `yaml:"purposes"`
	LegalBasis      string          `yaml:"legal_basis"`
	Stores          []Store         `yaml:"stores"`
	// Retention is a RetentionClass id.
	Retention  string   `yaml:"retention"`
	Processors []string `yaml:"processors,omitempty"`
}

// Table is a database table and the data categories it holds. A table with
// no personal data says so with NoPersonalData instead of listing none, so
// a table can't be left out by forgetting it.
type Table struct {
	Name           string   `yaml:"name"`
	Categories     []string `yaml:"categories,omitempty"`
	NoPersonalData bool     `yaml:"no_personal_data,omitempty"`
}

// Role is a processor's role towards the app's data.
type Role string

const (
	RoleProcessor    Role = "processor"
	RoleSubProcessor Role = "sub_processor"
	RoleController   Role = "controller"
	// RoleUnknown is for a party whose role nobody has decided; it is not
	// a guess.
	RoleUnknown Role = "unknown"
)

// Processor is an external party that receives or stores the app's data, or
// that the app's config can call.
type Processor struct {
	ID   string `yaml:"id"`
	Name string `yaml:"name"`
	Role Role   `yaml:"role"`
	// Purpose is what the processor is used for.
	Purpose string `yaml:"purpose"`
	// DataShared lists data category ids.
	DataShared []string `yaml:"data_shared"`
	// Location is where the processor handles the data, or Unknown.
	Location string `yaml:"location"`
	// TransferMechanism is TodoLegal until a qualified person decides.
	TransferMechanism string `yaml:"transfer_mechanism"`
	// SubProcessors lists processor ids this one engages, or is the single
	// value Unknown.
	SubProcessors []string `yaml:"sub_processors,omitempty"`
	// ConfigKeys are the app's config (env var) names that point the app at
	// this processor, so CI can tell a new one has appeared.
	ConfigKeys []string `yaml:"config_keys,omitempty"`
	Notes      string   `yaml:"notes,omitempty"`
}

// InternalService is a service the config can call that is run by the app's
// own operator inside its own infrastructure, so it is not a processor.
// Listing it records that someone looked.
type InternalService struct {
	ID         string   `yaml:"id"`
	Name       string   `yaml:"name"`
	ConfigKeys []string `yaml:"config_keys,omitempty"`
	Notes      string   `yaml:"notes,omitempty"`
}

// StorageType is the kind of browser storage.
type StorageType string

const (
	Cookie         StorageType = "cookie"
	LocalStorage   StorageType = "local_storage"
	SessionStorage StorageType = "session_storage"
	IndexedDB      StorageType = "indexeddb"
)

// StorageKey is a cookie or browser storage key the app sets.
type StorageKey struct {
	Key  string      `yaml:"key"`
	Type StorageType `yaml:"type"`
	// Purpose is a Purpose id, of kind cookie_category.
	Purpose    string `yaml:"purpose"`
	FirstParty bool   `yaml:"first_party"`
	// Lifetime is how long the browser keeps it, e.g. "15m", "tab",
	// "session".
	Lifetime string `yaml:"lifetime"`
	// Description is what it holds, in plain words.
	Description string `yaml:"description"`
	// SetBy is the code that sets it.
	SetBy string `yaml:"set_by"`
}

// RetentionClass is how long a kind of data is kept and what enforces it.
type RetentionClass struct {
	ID          string `yaml:"id"`
	Description string `yaml:"description"`
	// Period is what the system does today: a duration such as "14d", or
	// Indefinite or Unknown, or TodoLegal where a period is yet to be set.
	Period string `yaml:"period"`
	// Placeholder is true when the period is one the human has not
	// decided yet.
	Placeholder bool `yaml:"placeholder,omitempty"`
	// Mechanism is what deletes the data when the period ends, or
	// NoMechanism or Unknown.
	Mechanism string `yaml:"mechanism"`
}
