// Package statsbook writes and reads WFTDA statsbooks (the "IGRF and
// Standardized Stats Calculator" workbook).
//
// Cell positions are 0-based (column, row), as in the Java exporter this was
// checked against, and hold for the 2019-01-01, 2024-04-11 and 2025-02-01
// template revisions (StatsBook Manual, 5th edition).
package statsbook

import "github.com/xuri/excelize/v2"

// Sheet names.
const (
	sheetIGRF      = "IGRF"
	sheetScore     = "Score"
	sheetPenalties = "Penalties"
	sheetLineups   = "Lineups"
	sheetOSOffset  = "OS Offset"
	sheetClock     = "Game Clock"
	sheetReviews   = "Official Reviews"
)

// IGRF.
var (
	igrfVenue, igrfCity, igrfState, igrfGameNo = pos{1, 2}, pos{8, 2}, pos{10, 2}, pos{11, 2}
	igrfTournament, igrfHost                   = pos{1, 4}, pos{8, 4}
	igrfDate, igrfStartTime, igrfSuspension    = pos{1, 6}, pos{8, 6}, pos{11, 6}
	igrfLeague, igrfTeam, igrfColor            = 9, 10, 11 // rows; team column below
	igrfRosterFirst, igrfRosterLast            = 13, 32    // rows
	igrfOSAdjusted, igrfOSReason               = pos{3, 38}, pos{8, 38}
	igrfSuspensionServed                       = pos{4, 39}
	igrfExpulsionRows                          = []int{40, 42}
	igrfReviewsLabel                           = pos{0, 44}
	igrfReviewsYesNo, igrfExpulsionsYesNo      = pos{3, 44}, pos{10, 44}
	igrfCaptains                               = 48 // row
	igrfHNSO, igrfNSOFirst, igrfNSOLast        = 59, 60, 78
	igrfHR, igrfRefFirst, igrfRefLast          = 79, 80, 86
)

// igrfTeamCol is the IGRF column of a team's data: number (and league, team,
// color) and, one to the right, name.
func igrfTeamCol(team int) int { return []int{1, 8}[team] }

// Score, Lineups, OS Offset: one row per jam line, periods one below the other.
const (
	periodRows      = 38 // jam lines per period
	period2Offset   = periodRows + 4
	firstJamRow     = 3
	scoreTeamCol1   = 0
	scoreTeamCol2   = 19
	lineupsTeamCol2 = 26
	osOffsetCol2    = 7
)

// jamRow is the first row of jam lines of a period (0 or 1).
func jamRow(period int) int { return firstJamRow + period*period2Offset }

// headRow is the header row of a period.
func headRow(period int) int { return period * period2Offset }

// Penalties: two rows per skater (codes, jam numbers), 9 slots + FO/EXP.
const (
	penaltiesFirstRow = 3
	penaltiesTeam2    = 15 // column offset of the visiting team
	penaltiesPeriod2  = 28 // column offset of period 2
	penaltiesFOEXP    = 10 // column of FO/EXP (slot columns are 1-9)
)

// Game Clock.
const clockPeriodRows = 51 // rows per period block

type pos struct{ col, row int }

func (p pos) cell() string { return cellName(p.col, p.row) }

func cellName(col, row int) string {
	name, _ := excelize.CoordinatesToCellName(col+1, row+1)
	return name
}
