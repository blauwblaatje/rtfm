package replay

// Types for the game summary. Field order and omitempty follow
// game.schema.json: required fields are never omitted, and clock values that
// can legitimately be 0 are pointers so "0" and "unknown" stay distinct.

type Summary struct {
	Schema           string             `json:"schema"`
	ID               string             `json:"id"`
	Name             string             `json:"name,omitempty"`
	State            string             `json:"state"`
	Info             map[string]string  `json:"info,omitempty"`
	Timezone         string             `json:"timezone,omitempty"`
	GameType         string             `json:"gameType,omitempty"`
	CueMode          string             `json:"cueMode,omitempty"`
	Ruleset          Ruleset            `json:"ruleset"`
	Teams            []*Team            `json:"teams"`
	Officials        []*Official        `json:"officials,omitempty"`
	Periods          []*Period          `json:"periods"`
	UpcomingJam      *Jam               `json:"upcomingJam,omitempty"`
	Penalties        []*Penalty         `json:"penalties,omitempty"`
	BoxTrips         []*BoxTrip         `json:"boxTrips,omitempty"`
	Expulsions       []*Expulsion       `json:"expulsions,omitempty"`
	ScoreAdjustments []*ScoreAdjustment `json:"scoreAdjustments,omitempty"`
	Result           *Result            `json:"result,omitempty"`
	DismissedHints   []*DismissedHint   `json:"dismissedHints,omitempty"`
	Log              *LogInfo           `json:"log,omitempty"`
}

type Ruleset struct {
	Base        string         `json:"base"`
	BaseVersion string         `json:"baseVersion"`
	Name        string         `json:"name,omitempty"`
	Overrides   map[string]any `json:"overrides"`
}

type Team struct {
	Team           string            `json:"team"`
	PreparedTeam   string            `json:"preparedTeam,omitempty"`
	Name           string            `json:"name"`
	FullName       string            `json:"fullName,omitempty"`
	League         string            `json:"league,omitempty"`
	TeamName       string            `json:"teamName,omitempty"`
	Initials       string            `json:"initials,omitempty"`
	NameCue        string            `json:"nameCue,omitempty"`
	NameCuePron    string            `json:"nameCuePronunciation,omitempty"`
	UniformColor   string            `json:"uniformColor,omitempty"`
	Colors         map[string]string `json:"colors,omitempty"`
	AlternateNames map[string]string `json:"alternateNames,omitempty"`
	Logo           string            `json:"logo,omitempty"`
	Skaters        []*Skater         `json:"skaters"`
	Staff          []*Staff          `json:"staff,omitempty"`
}

// Staff is a non-skating team member (bench staff, coach box).
type Staff struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Role  string `json:"role,omitempty"`
	Flags string `json:"flags,omitempty"`
}

type Skater struct {
	ID       string `json:"id"`
	Number   string `json:"number"`
	Name     string `json:"name,omitempty"`
	Pronouns string `json:"pronouns,omitempty"`
	// How to say the skater name, and the WFTDA unique skater id (from the
	// charter).
	Pronunciation string `json:"pronunciation,omitempty"`
	WUID          string `json:"wuid,omitempty"`
	Flags         string `json:"flags,omitempty"`
	Status        string `json:"status,omitempty"` // "" means eligible
}

type Official struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Role   string `json:"role"`
	League string `json:"league,omitempty"`
	Cert   string `json:"cert,omitempty"`
	P1Team string `json:"p1Team,omitempty"`
	Swap   bool   `json:"swap,omitempty"`
}

type Period struct {
	ID       string     `json:"id"`
	Number   int        `json:"number"`
	Start    string     `json:"start,omitempty"`
	End      string     `json:"end,omitempty"`
	Duration *int64     `json:"duration,omitempty"`
	Jams     []*Jam     `json:"jams"`
	Timeouts []*Timeout `json:"timeouts,omitempty"`
	// SuddenScoring: jams in this period are sudden scoring jams (JRDA).
	SuddenScoring bool `json:"suddenScoring,omitempty"`
}

type Jam struct {
	ID                 string     `json:"id"`
	Number             int        `json:"number"`
	Overtime           bool       `json:"overtime,omitempty"`
	InjuryContinuation bool       `json:"injuryContinuation,omitempty"`
	Start              string     `json:"start,omitempty"`
	End                string     `json:"end,omitempty"`
	Duration           *int64     `json:"duration,omitempty"`
	PeriodClockStart   *int64     `json:"periodClockStart,omitempty"`
	PeriodClockEnd     *int64     `json:"periodClockEnd,omitempty"`
	EndReason          string     `json:"endReason,omitempty"`
	EndDetail          string     `json:"endDetail,omitempty"`
	Teams              []*TeamJam `json:"teams"`
}

type TeamJam struct {
	Team           string               `json:"team"`
	Lead           bool                 `json:"lead,omitempty"`
	Lost           bool                 `json:"lost,omitempty"`
	Calloff        bool                 `json:"calloff,omitempty"`
	NoPivot        bool                 `json:"noPivot,omitempty"`
	StarPassTrip   string               `json:"starPassTrip,omitempty"`
	OsOffset       int                  `json:"osOffset,omitempty"`
	OsOffsetReason string               `json:"osOffsetReason,omitempty"`
	Trips          []*Trip              `json:"trips"`
	Lineup         map[string]*Fielding `json:"lineup,omitempty"`
	SkAnnotation   string               `json:"skAnnotation,omitempty"`
	LtAnnotation   string               `json:"ltAnnotation,omitempty"`
}

type Trip struct {
	ID            string `json:"id"`
	Points        int    `json:"points"`
	AfterStarPass bool   `json:"afterStarPass,omitempty"`
	JamClockStart *int64 `json:"jamClockStart,omitempty"`
	JamClockEnd   *int64 `json:"jamClockEnd,omitempty"`
	Annotation    string `json:"annotation,omitempty"`
}

type Fielding struct {
	Skater     string `json:"skater,omitempty"`
	NotFielded bool   `json:"notFielded,omitempty"`
	SitFor3    bool   `json:"sitFor3,omitempty"`
	Annotation string `json:"annotation,omitempty"`
	// BoxTrip: the skater sits in this box trip for someone else (a
	// substitute; Java's Fielding.BoxTrip).
	BoxTrip string `json:"boxTrip,omitempty"`
}

func (f *Fielding) empty() bool { return *f == Fielding{} }

type Timeout struct {
	ID            string `json:"id"`
	Owner         string `json:"owner"`
	Review        bool   `json:"review,omitempty"`
	AsTimeout     bool   `json:"asTimeout,omitempty"`
	Retained      *bool  `json:"retained,omitempty"`
	ReviewRequest string `json:"reviewRequest,omitempty"`
	ReviewResult  string `json:"reviewResult,omitempty"`
	AfterJam      string `json:"afterJam"`
	Start         string `json:"start,omitempty"`
	End           string `json:"end,omitempty"`
	Duration      *int64 `json:"duration,omitempty"`
	PeriodClock   *int64 `json:"periodClock,omitempty"`
	// PeriodClockEnd is the period clock when the timeout ended, after any
	// correction made during it (e.g. set back to when it was requested).
	PeriodClockEnd *int64 `json:"periodClockEnd,omitempty"`
}

type Penalty struct {
	ID              string `json:"id"`
	Team            string `json:"team"`
	Skater          string `json:"skater,omitempty"`
	Staff           string `json:"staff,omitempty"`
	Slot            int    `json:"slot"`
	Code            string `json:"code"`
	Jam             string `json:"jam"`
	Time            string `json:"time,omitempty"`
	PeriodClock     *int64 `json:"periodClock,omitempty"`
	JamClock        *int64 `json:"jamClock,omitempty"`
	CalledBy        string `json:"calledBy,omitempty"`
	CallingPosition string `json:"callingPosition,omitempty"`
	ForceServed     bool   `json:"forceServed,omitempty"`
	Annotation      string `json:"annotation,omitempty"`
}

type BoxPoint struct {
	Jam           string `json:"jam"`
	BetweenJams   bool   `json:"betweenJams"`
	AfterStarPass bool   `json:"afterStarPass"`
	Position      string `json:"position,omitempty"`
	Time          string `json:"time,omitempty"`
	JamClock      *int64 `json:"jamClock,omitempty"`
}

type BoxTrip struct {
	ID        string    `json:"id"`
	Team      string    `json:"team"`
	Skater    string    `json:"skater,omitempty"`
	Jammer    bool      `json:"jammer"`
	Start     BoxPoint  `json:"start"`
	End       *BoxPoint `json:"end,omitempty"`
	Duration  *int64    `json:"duration,omitempty"`
	Shortened int       `json:"shortened,omitempty"`
	// ShortenedTime is the box time taken off by jammer swaps, ms.
	ShortenedTime int64    `json:"shortenedTime,omitempty"`
	Penalties     []string `json:"penalties,omitempty"`
	Annotation    string   `json:"annotation,omitempty"`
}

type Expulsion struct {
	Penalty    string `json:"penalty"`
	Info       string `json:"info"`
	ExtraInfo  string `json:"extraInfo,omitempty"`
	Suspension bool   `json:"suspension"`
}

type ScoreAdjustment struct {
	ID             string `json:"id"`
	Team           string `json:"team"`
	Amount         int    `json:"amount"`
	Jam            string `json:"jam"`
	DuringJam      bool   `json:"duringJam"`
	LastTwoMinutes bool   `json:"lastTwoMinutes,omitempty"`
}

type Score struct {
	Team1 int `json:"1"`
	Team2 int `json:"2"`
}

type Result struct {
	OfficialScore     *Score   `json:"officialScore,omitempty"`
	DeclaredAt        string   `json:"declaredAt,omitempty"`
	HR                string   `json:"hr,omitempty"`
	HNSO              string   `json:"hnso,omitempty"`
	EndedEarly        *Ended   `json:"endedEarly,omitempty"`
	SuspensionsServed []string `json:"suspensionsServed,omitempty"`
}

// Ended describes a game that was cancelled or forfeited.
type Ended struct {
	Outcome        string `json:"outcome"`
	ForfeitingTeam string `json:"forfeitingTeam,omitempty"`
	Reason         string `json:"reason"`
	Time           string `json:"time,omitempty"`
	Period         int    `json:"period"`
	PeriodClock    int64  `json:"periodClock"`
}

// DismissedHint is a paperwork hint someone marked as correct.
type DismissedHint struct {
	Check  string     `json:"check"`
	Target HintTarget `json:"target"`
	Reason string     `json:"reason"`
	Time   string     `json:"time,omitempty"`
}

type HintTarget struct {
	Kind string `json:"kind"`
	ID   string `json:"id,omitempty"`
}

type LogInfo struct {
	File    string `json:"file,omitempty"`
	LastSeq int    `json:"lastSeq"`
}
