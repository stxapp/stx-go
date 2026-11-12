package stx

// Creating markets on a scheduled event (POST /api/v1/markets), with the
// market types and event players that describe what can be created.

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
)

// MaxMarketsPerRequest is the most markets one [MarketsService.Create] call
// takes.
const MaxMarketsPerRequest = 25

// RulePlayerStatLine is the market rule for over/under one player's stat in
// one event, the only rule an app can create today.
const RulePlayerStatLine = "player_stat_line"

// Statuses of a [CreateMarketResult].
const (
	// MarketCreated: the market was created.
	MarketCreated = "created"
	// MarketExists: the market was already listed; MarketID is that market.
	MarketExists = "exists"
	// MarketValid: a dry run found nothing wrong; nothing was created.
	MarketValid = "valid"
	// MarketRejected: the market was not created; see Reason.
	MarketRejected = "rejected"
)

// Reasons a market is rejected. The exchange may send reasons added after
// this version; compare with these constants and treat others as rejected.
const (
	RejectInvalidMarket   = "invalid_market"
	RejectUnsupportedRule = "unsupported_rule"
	RejectUnsupportedStat = "unsupported_stat"
	RejectInvalidLine     = "invalid_line"
	RejectUnknownPlayer   = "unknown_player"
	RejectEventNotOpen    = "event_not_open"
	// RejectNotCreated: the answer had no result for the market.
	RejectNotCreated = "not_created"
)

// KnownStats returns the player stats by sport, as the exchange names them,
// for autocomplete. The stats an event accepts are the ones
// [EventsService.Players] returns for it.
func KnownStats() map[string][]string {
	return map[string][]string{
		"Baseball":   {"BASES", "BK", "DOUBLES", "HITS", "HIT_RUN_RBI", "HOMERUN", "PERA", "PHITA", "PK", "POUTS", "PRUNSALOWED", "PWALKSA", "RBI", "RUNS", "SINGLES", "STOLENBASES", "TRIPLES", "WALKS"},
		"Basketball": {"ASSISTS", "BLOCKS", "POINTS", "POINTS_ASSISTS", "POINTS_REBOUNDS", "POINTS_REBOUNDS_ASSISTS", "REBOUNDS", "REBOUNDS_ASSISTS", "STEALS", "STEALS_BLOCKS", "THREE_POINTS", "TURNOVERS"},
		"Football":   {"DEF_INTERCEPT", "DEF_TD", "FG_MADE", "INTERCEPT", "PASS_ATT", "PASS_CMP", "PASS_RUSH_YDS", "PASS_SACKS", "PASS_TD", "PASS_YDS", "REC", "RECV_LONG", "RECV_TD", "RECV_YDS", "RUSH_ATT", "RUSH_LONG", "RUSH_REC_YDS", "RUSH_TD", "RUSH_YDS", "SACKS", "TACKLES_ASSIST", "TACKLES_SOLO", "TACKLES_TOTAL", "TD_SCORE"},
		"Hockey":     {"ASSISTS", "GOALS", "GOAL_AG", "GOAL_SO", "POINTS", "SAVES"},
	}
}

// NewMarket is one market to create. It is also the request echoed back in
// each [CreateMarketResult].
type NewMarket struct {
	// Rule is the market rule. Empty means RulePlayerStatLine.
	Rule string `json:"rule"`
	// PlayerID is from [EventsService.Players].
	PlayerID string `json:"player_id"`
	// Stat is one of the event's stats, such as "POINTS".
	Stat string `json:"stat"`
	// Line is a positive number ending in .5, such as 18.5. Whole and
	// quarter lines, zero and negatives are rejected (invalid_line).
	Line float64 `json:"line"`
}

// CreateMarketsOptions tune [MarketsService.Create].
type CreateMarketsOptions struct {
	// Validate checks each market against the event's players and stats and
	// the line rule before sending. Failing markets come back rejected
	// without a request, and the rest are sent with the event's own stat key.
	// It reads the event's players, so an app token also needs the events
	// scope.
	Validate bool
	// DryRun runs every check on the exchange and creates nothing; a market
	// that would be created, or one already listed, comes back valid.
	DryRun bool
}

// CreateMarketResult is the outcome for one requested market.
type CreateMarketResult struct {
	// Index is the market's position in the list given to Create.
	Index int
	// Status is MarketCreated, MarketExists, MarketValid or MarketRejected.
	Status string
	// MarketID is set when Status is MarketCreated or MarketExists.
	MarketID string
	// Reason is set when Status is MarketRejected, such as
	// RejectInvalidLine.
	Reason string
	// Market is the market as requested.
	Market NewMarket
}

// IsValidLine reports whether line is a valid line: positive and ending in .5.
func IsValidLine(line float64) bool {
	if math.IsNaN(line) || math.IsInf(line, 0) || line <= 0 {
		return false
	}
	return math.Mod(line*2, 2) == 1
}

// StatsFor returns the stats an event accepts for new markets.
func (s *EventsService) StatsFor(ctx context.Context, eventID string) ([]EventStat, error) {
	p, err := s.Players(ctx, eventID)
	if err != nil {
		return nil, err
	}
	return p.Stats, nil
}

// Create creates markets on a scheduled event (POST /api/v1/markets), 1 to
// [MaxMarketsPerRequest] per call. It needs an app token with markets.write,
// which STX grants, sent from an address on the app's IP allow-list. See
// https://docs.stxapp.io/apps/creating-markets/.
//
// It returns one result per market, in order. A rejected market is a result,
// not an error, and resending is safe: a market already listed comes back
// MarketExists. A refused request is an error: a 403 without markets.write,
// [ErrNotFound] for an unknown event, [ErrRejected] for an event that is not
// open. Where the exchange cannot create markets the error matches
// [ErrNotAvailable].
func (s *MarketsService) Create(ctx context.Context, eventID string, markets []NewMarket, opts *CreateMarketsOptions) ([]CreateMarketResult, error) {
	if len(markets) < 1 || len(markets) > MaxMarketsPerRequest {
		return nil, fmt.Errorf("stx: Create takes 1 to %d markets; got %d", MaxMarketsPerRequest, len(markets))
	}
	var o CreateMarketsOptions
	if opts != nil {
		o = *opts
	}
	type slot struct {
		m NewMarket
		i int
	}
	results := make([]*CreateMarketResult, len(markets))
	send := make([]slot, 0, len(markets))
	for i, m := range markets {
		if m.Rule == "" {
			m.Rule = RulePlayerStatLine
		}
		send = append(send, slot{m, i})
	}
	if o.Validate {
		players, err := s.c.Events.Players(ctx, eventID)
		if err != nil {
			return nil, err
		}
		kept := send[:0]
		for _, sl := range send {
			checked, reason := checkMarket(sl.m, players)
			if reason != "" {
				results[sl.i] = &CreateMarketResult{Index: sl.i, Status: MarketRejected, Reason: reason, Market: sl.m}
				continue
			}
			kept = append(kept, slot{checked, sl.i})
		}
		send = kept
	}
	if len(send) > 0 {
		body := map[string]any{"event_id": eventID}
		entries := make([]NewMarket, len(send))
		for k, sl := range send {
			entries[k] = sl.m
		}
		body["markets"] = entries
		if o.DryRun {
			body["dry_run"] = true
		}
		raw, err := doOne[struct {
			Markets []struct {
				Index    *int   `json:"index"`
				Status   string `json:"status"`
				MarketID string `json:"market_id"`
				Reason   string `json:"reason"`
			} `json:"markets"`
		}](ctx, s.c, "markets_post", callArgs{body: body})
		if err != nil {
			return nil, s.createRefused(ctx, err)
		}
		for k, r := range raw.Markets {
			at := k
			if r.Index != nil {
				at = *r.Index
			}
			if at < 0 || at >= len(send) {
				continue
			}
			sl := send[at]
			res := &CreateMarketResult{Index: sl.i, Status: r.Status, Market: sl.m}
			switch r.Status {
			case MarketCreated, MarketExists:
				res.MarketID = r.MarketID
			case MarketValid:
			default:
				res.Status, res.Reason = MarketRejected, r.Reason
				if res.Reason == "" {
					res.Reason = RejectNotCreated
				}
			}
			results[sl.i] = res
		}
	}
	out := make([]CreateMarketResult, len(markets))
	for i, r := range results {
		if r == nil {
			m := markets[i]
			if m.Rule == "" {
				m.Rule = RulePlayerStatLine
			}
			r = &CreateMarketResult{Index: i, Status: MarketRejected, Reason: RejectNotCreated, Market: m}
		}
		out[i] = *r
	}
	return out, nil
}

// createRefused turns a plain 403 into a *NotAvailableError when the
// exchange's discovery document shows it does not create markets at all.
func (s *MarketsService) createRefused(ctx context.Context, err error) error {
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 403 || errors.Is(err, ErrIPNotAllowed) || errors.Is(err, ErrTermsRequired) {
		return err
	}
	caps, cerr := s.c.Capabilities(ctx, false)
	if cerr == nil && caps.Known(FeatureMarketsCreate) && !caps.Has(FeatureMarketsCreate) {
		return &NotAvailableError{Feature: FeatureMarketsCreate, Host: s.c.host, Err: err}
	}
	return err
}

// checkMarket checks m against the event: m with the event's own stat key,
// or why it would be rejected.
func checkMarket(m NewMarket, p *EventPlayers) (NewMarket, string) {
	if !IsValidLine(m.Line) {
		return m, RejectInvalidLine
	}
	stat := ""
	for _, s := range p.Stats {
		if strings.EqualFold(s.Stat, m.Stat) {
			stat = s.Stat
			break
		}
	}
	if stat == "" {
		return m, RejectUnsupportedStat
	}
	for _, t := range p.Teams {
		for _, pl := range t.Players {
			if pl.PlayerID == m.PlayerID {
				m.Stat = stat
				return m, ""
			}
		}
	}
	return m, RejectUnknownPlayer
}
