package diagnose

import "github.com/wittywilla/wittymodule"

// A check of a disc, reporting through the helper protocol. The words are the same ones a server
// shows for its own checks.

type (
	// Job is a check in this helper.
	Job = wittymodule.DiagJob
	// Finding is one thing found.
	Finding = wittymodule.DiagFinding
)

// How bad a finding is.
const (
	SevOK      = wittymodule.DiagOK
	SevInfo    = wittymodule.DiagInfo
	SevWarn    = wittymodule.DiagWarn
	SevProblem = wittymodule.DiagProblem
)

// DiscOptions choose the long parts of a disc check, and where they go on the progress bar.
type DiscOptions struct {
	Crawl      bool    `json:"crawl,omitempty"` // press every menu button
	From       float64 `json:"from,omitempty"`
	To         float64 `json:"to,omitempty"`
	WithDamage bool    `json:"withDamage,omitempty"` // a damage scan follows, in the server
	Path       string  `json:"path"`
	BDJ        bool    `json:"bdj,omitempty"`  // a disc's Java programs are on, on this server
	Java       string  `json:"java,omitempty"` // the Java to use; "" finds it
}

// Result is what a server needs afterwards: its damage scan needs to know whether to expect
// scrambled video, and Where says what a place in a file is used for.
type Result struct {
	Scrambled bool                                `json:"scrambled,omitempty"`
	Where     func(name string, off int64) string `json:"-"`
}

// clip keeps the first max of a list, and says how many were left out.
func clip(list []string, total, max int) ([]string, int) {
	if len(list) > max {
		return list[:max], total - max
	}
	return list, total - len(list)
}
