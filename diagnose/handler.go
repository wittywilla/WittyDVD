// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package diagnose

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/wittywilla/wittymodule"
)

// Handler adds the disc check's calls to a helper's own.
type Handler struct {
	Next wittymodule.Handler
	last *Result
}

// Handle answers one call: "diagnose" runs the check, reporting as it goes, and "where" words what
// a place in a file is used for, which the damage scan that follows it asks.
func (h *Handler) Handle(method string, raw json.RawMessage) (any, []byte, error) {
	switch method {
	case "diagnose":
		var o DiscOptions
		if err := json.Unmarshal(raw, &o); err != nil || o.Path == "" {
			return nil, nil, &wittymodule.CallError{Code: "bad-call", Message: "no disc was named"}
		}
		src, err := openSource(o.Path)
		if err != nil {
			return nil, nil, &wittymodule.CallError{Message: fmt.Sprintf("the disc can't be read: %v", err)}
		}
		defer src.close()
		res := &Result{}
		check(context.Background(), &Job{}, src, o, res)
		h.last = res
		return res, nil, nil
	case "where":
		var p struct {
			Name string `json:"name"`
			Off  int64  `json:"off"`
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, nil, &wittymodule.CallError{Code: "bad-call", Message: "the call is unreadable"}
		}
		out := ""
		if h.last != nil && h.last.Where != nil {
			out = h.last.Where(p.Name, p.Off)
		}
		return struct {
			Where string `json:"where"`
		}{out}, nil, nil
	}
	return h.Next(method, raw)
}
