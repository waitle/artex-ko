package server

import (
	"errors"
	"github.com/Autumn-27/artex/agent"
	"net/http"
	"path/filepath"
	"strings"
)

func codexAuthPath(keyDir, dataDir string) (string, error) {
	k, e := filepath.Abs(keyDir)
	if e != nil {
		return "", e
	}
	d, e := filepath.Abs(dataDir)
	if e != nil {
		return "", e
	}
	if resolved, e := filepath.EvalSymlinks(k); e == nil {
		k = resolved
	}
	if resolved, e := filepath.EvalSymlinks(d); e == nil {
		d = resolved
	}
	rel, e := filepath.Rel(d, k)
	if e != nil || rel == "." || (!strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != "..") {
		return "", errors.New("Codex auth directory must be outside browsable data")
	}
	return filepath.Join(k, "codex-oauth.json"), nil
}
func (s *Server) bindOAuth(c agent.Config) agent.Config {
	if c.Provider() == "openai-codex" {
		c.Codex = s.codex
	}
	return c
}
func (s *Server) codexStatus(w http.ResponseWriter, r *http.Request) {
	if s.codex == nil {
		writeJSON(w, 200, map[string]bool{"connected": false})
		return
	}
	writeJSON(w, 200, s.codex.Status())
}
func (s *Server) codexStart(w http.ResponseWriter, r *http.Request) {
	if s.codex == nil {
		writeErr(w, 503, "Codex OAuth credential store unavailable")
		return
	}
	v, e := s.codex.Start(r.Context())
	if e != nil {
		writeErr(w, 502, e.Error())
		return
	}
	writeJSON(w, 202, v)
}
func (s *Server) codexPoll(w http.ResponseWriter, r *http.Request) {
	if s.codex == nil {
		writeErr(w, 503, "Codex OAuth credential store unavailable")
		return
	}
	v, e := s.codex.Poll(r.Context())
	if e != nil {
		writeErr(w, 502, e.Error())
		return
	}
	if v.Connected && s.m != nil {
		s.invalidateProfileAgents()
		// A persisted selection always takes precedence, even if unavailable.
		// Otherwise restore the environment bootstrap that could not start
		// before login (and was cleared on logout).
		if s.m.pg != nil {
			active, err := s.m.pg.ActiveProfile()
			if err != nil {
				writeErr(w, 500, "Unable to reload active LLM profile")
				return
			}
			if active != nil {
				s.reapplyActiveProfile()
				writeJSON(w, 200, v)
				return
			}
		}
		if cfg, ok := agent.FromEnv(); ok && cfg.Provider() == "openai-codex" {
			if err := s.applyLLM(s.bindOAuth(cfg)); err != nil {
				writeErr(w, 503, err.Error())
				return
			}
		}
	}
	writeJSON(w, 200, v)
}
func (s *Server) codexLogout(w http.ResponseWriter, r *http.Request) {
	if s.codex != nil {
		if e := s.codex.Logout(); e != nil {
			writeErr(w, 500, e.Error())
			return
		}
	}
	s.cfgMu.Lock()
	if s.llmCfg.Provider() == "openai-codex" {
		s.llmOn = false
		s.llmCfg = agent.Config{}
		s.llmProf = ""
		s.llmProv = nil
		s.llmDirect = nil
		s.chatAgent = nil
	}
	s.cfgMu.Unlock()
	if s.m != nil {
		s.invalidateProfileAgents()
	}
	writeJSON(w, 200, map[string]bool{"connected": false})
}
