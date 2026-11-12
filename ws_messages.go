package stx

import (
	"encoding/json"
	"log/slog"
)

// Message is one event pushed on a subscription.
type Message struct {
	// Topic is the wire topic, for example "orders:7fb55544-3a7e-4f6b-bd66-fc89bd3a1087".
	Topic string
	// Event is the event name, for example "all_orders", "new_open_order"
	// or "book".
	Event string
	// Payload is the event's JSON. Money and quantities are strings; on the
	// markets, market_updates and market:<id> channels the stream has already
	// converted the exchange's cents to dollar strings.
	Payload json.RawMessage
	// Snapshot is true for the state sent on join, again after every rejoin:
	// "all_orders", "all_trades", "all_positions", "balances" and
	// "user_updated". Replace what you hold rather than merging.
	Snapshot bool
	// Data is the payload decoded into the type documented for the event,
	// or nil for an event the SDK does not know. Switch on its type:
	//
	//	orderbook       book                          *OrderbookUpdate
	//	ticker          ticker                        *Ticker
	//	trades          trade                         *PublicTrade
	//	events          event                         *EventVolume
	//	markets         market_created/_updated       MarketChanges
	//	market_updates  created/updated               *MarketChange
	//	market:<id>     market_update/order_book_update *MarketChange
	//	market_stats    market_stats(_snapshot)       *MarketSeries
	//	orders          all_orders / new_open_order   *OrdersSnapshot / *Order
	//	fills           all_trades / trade            *FillsSnapshot / *Fill (TradeID is the channel's "id")
	//	positions       all_positions / updated_positions *PositionsSnapshot / *PositionsUpdate
	//	settlements     new_settlements               *SettlementsUpdate
	//	balances        balances, update / payment_update *Balance / *PaymentUpdate
	//	user_info       user_updated                  *UserInfo
	//	order_slip      order_numbers_batch / self_match_batch *OrderNumbersBatch / *SelfMatchBatch
	//
	// The account channel carries the orders, fills, positions, settlements
	// and balances events with the same types.
	Data any
}

// Channel returns the topic without the user id, for example "orders".
func (m Message) Channel() string { return channelName(m.Topic) }

// Decode decodes the payload into v.
func (m Message) Decode(v any) error { return json.Unmarshal(m.Payload, v) }

func newMessage(topic, event string, payload json.RawMessage, log *slog.Logger) Message {
	channel := channelName(topic)
	payload = convertPayload(channel, payload)
	m := Message{Topic: topic, Event: event, Payload: payload, Snapshot: isSnapshot(channel, event)}
	if target := dataFor(channel, event); target != nil {
		if err := json.Unmarshal(payload, target); err != nil {
			log.Debug("could not decode payload", "topic", topic, "event", event, "err", err)
		} else {
			m.Data = deref(target)
		}
	}
	return m
}

// deref returns maps by value and everything else as the pointer.
func deref(v any) any {
	switch p := v.(type) {
	case *MarketChanges:
		return *p
	case *streamFill:
		return p.fill()
	}
	return v
}

func dataFor(channel, event string) any {
	switch channel {
	case "orderbook":
		if event == "book" {
			return new(OrderbookUpdate)
		}
	case "ticker":
		if event == "ticker" {
			return new(Ticker)
		}
	case "trades":
		if event == "trade" {
			return new(PublicTrade)
		}
	case "events":
		if event == "event" {
			return new(EventVolume)
		}
	case "markets":
		if event == "market_created" || event == "market_updated" {
			return new(MarketChanges)
		}
	case "market_updates":
		if event == "created" || event == "updated" {
			return new(MarketChange)
		}
	case "market":
		if event == "market_update" || event == "order_book_update" {
			return new(MarketChange)
		}
	case "market_stats":
		if event == "market_stats" || event == "market_stats_snapshot" {
			return new(MarketSeries)
		}
	case "order_slip":
		switch event {
		case "order_numbers_batch":
			return new(OrderNumbersBatch)
		case "self_match_batch":
			return new(SelfMatchBatch)
		}
	case "user_info":
		if event == "user_updated" {
			return new(UserInfo)
		}
	case "orders", "fills", "positions", "settlements", "balances", "account":
		return accountData(event)
	}
	return nil
}

func accountData(event string) any {
	switch event {
	case "all_orders":
		return new(OrdersSnapshot)
	case "new_open_order":
		return new(Order)
	case "all_trades":
		return new(FillsSnapshot)
	case "trade":
		return new(streamFill)
	case "all_positions":
		return new(PositionsSnapshot)
	case "updated_positions":
		return new(PositionsUpdate)
	case "new_settlements":
		return new(SettlementsUpdate)
	case "balances", "update":
		return new(Balance)
	case "payment_update":
		return new(PaymentUpdate)
	}
	return nil
}

// OrderbookLevel is one price level of the aggregated book. Liquidity is
// that level alone; the totals are cumulative through it.
type OrderbookLevel struct {
	Price          Decimal `json:"price"`
	Quantity       Decimal `json:"quantity"`
	Liquidity      Decimal `json:"liquidity"`
	TotalQuantity  Decimal `json:"total_quantity"`
	TotalLiquidity Decimal `json:"total_liquidity"`
}

// OrderbookUpdate is a "book" message: one market's whole book, best level
// first. Replace the book you hold.
type OrderbookUpdate struct {
	MarketID    string           `json:"market_id"`
	Bids        []OrderbookLevel `json:"bids"`
	Offers      []OrderbookLevel `json:"offers"`
	Timestamp   string           `json:"timestamp"`
	TimestampUS int64            `json:"timestamp_us"`
}

// Ticker is a "ticker" message: one market's price summary. Any field can be
// empty on a market that has not traded or has an empty side.
type Ticker struct {
	MarketID           string  `json:"market_id"`
	MarketSymbol       string  `json:"market_symbol"`
	EventID            string  `json:"event_id"`
	EventSymbol        string  `json:"event_symbol"`
	Sport              string  `json:"sport"`
	Competition        string  `json:"competition"`
	LastTradedPrice    Decimal `json:"last_traded_price"`
	LastTradedQuantity Decimal `json:"last_traded_quantity"`
	BestBid            Decimal `json:"best_bid"`
	BestBidQuantity    Decimal `json:"best_bid_quantity"`
	BestOffer          Decimal `json:"best_offer"`
	BestOfferQuantity  Decimal `json:"best_offer_quantity"`
	BidDepth           int     `json:"bid_depth"`
	OfferDepth         int     `json:"offer_depth"`
	OpenInterest       Decimal `json:"open_interest"`
	TotalVolume        Decimal `json:"total_volume"`
	Timestamp          string  `json:"timestamp"`
	TimestampUS        int64   `json:"timestamp_us"`
}

// PublicTrade is a "trade" message on the trades channel: an anonymous
// execution. Action is the taker's side, "buy" or "sell".
type PublicTrade struct {
	MarketID     string  `json:"market_id"`
	MarketSymbol string  `json:"market_symbol"`
	EventID      string  `json:"event_id"`
	EventSymbol  string  `json:"event_symbol"`
	Price        Decimal `json:"price"`
	Quantity     Decimal `json:"quantity"`
	Action       string  `json:"action"`
	Timestamp    string  `json:"timestamp"`
	TimestampUS  int64   `json:"timestamp_us"`
}

// EventVolume is an "event" message: an event's lifetime traded volume in
// dollars across its markets. It is the whole value, not a delta.
type EventVolume struct {
	EventID string  `json:"event_id"`
	Volume  Decimal `json:"volume"`
}

// EventsReply is the events channel's join reply, and the reply to
// [Subscription.SelectEventIDs]. An event with nothing traded is left out of
// Events.
type EventsReply struct {
	SelectedEventIDs []string      `json:"selected_event_ids"`
	Events           []EventVolume `json:"events"`
}

// PricePoint is one bucket of a price series: its start in Unix
// microseconds and its closing price as a percent of the market's max price.
type PricePoint struct {
	TimestampUS  int64   `json:"timestamp_us"`
	PricePercent float64 `json:"price_percent"`
}

// MarketSeries is one market's price series. On "market_stats" it holds the
// buckets that changed (upsert them by TimestampUS); on
// "market_stats_snapshot" the whole series (replace it).
type MarketSeries struct {
	MarketID string       `json:"market_id"`
	Points   []PricePoint `json:"points"`
}

// MarketStatsReply is the market_stats join reply, and the reply to
// [Subscription.RequestSeries].
type MarketStatsReply struct {
	SelectedMarketIDs []string       `json:"selected_market_ids"`
	Range             StatsRange     `json:"range"`
	Markets           []MarketSeries `json:"markets"`
}

// MarketBookLevel is one level of the book on market:<id>: price,
// quantity, liquidity, and the cumulative quantity and liquidity.
type MarketBookLevel struct {
	Price          Decimal `json:"p"`
	Quantity       Decimal `json:"q"`
	Liquidity      Decimal `json:"l"`
	TotalQuantity  Decimal `json:"tc"`
	TotalLiquidity Decimal `json:"tl"`
}

// MarketBook is the book on market:<id>.
type MarketBook struct {
	Bids   []MarketBookLevel `json:"b"`
	Offers []MarketBookLevel `json:"o"`
}

// MarketChange is a market as the markets, market_updates and market:<id>
// channels send it: in full when a market is created (and as the market:<id>
// join reply), otherwise only the fields that changed. Use Has to tell a
// field that is absent from one that is null. Prices are dollar strings.
type MarketChange struct {
	MarketID           string      `json:"market_id"`
	Symbol             string      `json:"symbol"`
	Title              string      `json:"title"`
	ShortTitle         string      `json:"short_title"`
	Status             string      `json:"status"`
	Result             string      `json:"result"`
	Trading            *bool       `json:"trading"`
	Rules              string      `json:"rules"`
	EventID            string      `json:"event_id"`
	EventStatus        string      `json:"event_status"`
	EventBrief         string      `json:"event_brief"`
	DetailedEventBrief string      `json:"detailed_event_brief"`
	EventStart         int64       `json:"event_start"`
	SettledAt          string      `json:"settled_at"`
	MaxPrice           Decimal     `json:"max_price"`
	Price              Decimal     `json:"price"`
	LastTradedPrice    Decimal     `json:"last_traded_price"`
	TotalVolume        Decimal     `json:"total_volume"`
	Volume24h          Decimal     `json:"volume_24h"`
	OpenInterest       Decimal     `json:"open_interest"`
	Probability        *float64    `json:"probability"`
	Bids               []BookLevel `json:"bids"`
	Offers             []BookLevel `json:"offers"`
	Book               *MarketBook `json:"ob"`
	Timestamp          string      `json:"timestamp"`
	UnixTimestamp      int64       `json:"unix_timestamp"`
	// Fields holds every field sent, converted, by name.
	Fields map[string]json.RawMessage `json:"-"`
}

// Has reports whether the message carried field.
func (m *MarketChange) Has(field string) bool {
	_, ok := m.Fields[field]
	return ok
}

// UnmarshalJSON decodes the known fields and keeps every field in Fields.
func (m *MarketChange) UnmarshalJSON(data []byte) error {
	type plain MarketChange
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	if err := json.Unmarshal(data, &p.Fields); err != nil {
		return err
	}
	if p.MarketID == "" {
		var id struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(data, &id)
		p.MarketID = id.ID
	}
	*m = MarketChange(p)
	return nil
}

// MarketChanges is a markets channel message: market id to change.
type MarketChanges map[string]*MarketChange

// OrdersSnapshot is "all_orders": your open orders on join.
type OrdersSnapshot struct {
	Orders []Order `json:"orders"`
}

// FillsSnapshot is "all_trades": the fills behind your open positions.
type FillsSnapshot struct {
	Trades []Fill `json:"trades"`
}

// UnmarshalJSON reads the fills, taking TradeID from the channel's "id".
func (s *FillsSnapshot) UnmarshalJSON(data []byte) error {
	var raw struct {
		Trades []streamFill `json:"trades"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	s.Trades = make([]Fill, len(raw.Trades))
	for i := range raw.Trades {
		s.Trades[i] = *raw.Trades[i].fill()
	}
	return nil
}

// streamFill is a fill as the channels send it: the trade id is "id".
type streamFill struct {
	Fill
	ID string `json:"id"`
}

func (f *streamFill) fill() *Fill {
	out := f.Fill
	if out.TradeID == "" {
		out.TradeID = f.ID
	}
	return &out
}

// PositionsSnapshot is "all_positions": your positions on join.
type PositionsSnapshot struct {
	Positions []Position `json:"positions"`
}

// PositionsUpdate is "updated_positions": only the positions that changed.
type PositionsUpdate struct {
	Positions []Position `json:"positions"`
}

// SettlementsUpdate is "new_settlements".
type SettlementsUpdate struct {
	Settlements []Settlement `json:"settlements"`
}

// PaymentUpdate is "payment_update": a payment changed status. Data is the
// provider's own detail.
type PaymentUpdate struct {
	ID         string          `json:"id"`
	AccountID  string          `json:"account_id"`
	Provider   string          `json:"provider"`
	ProviderID string          `json:"provider_id"`
	Status     string          `json:"status"`
	Type       string          `json:"type"`
	Amount     Decimal         `json:"amount"`
	Data       json.RawMessage `json:"data"`
}

// UserInfo is "user_updated": your profile and account state.
type UserInfo struct {
	UserID               string `json:"userId"`
	UserUID              string `json:"userUid"`
	UserStatus           string `json:"userStatus"`
	FirstName            string `json:"firstName"`
	LastName             string `json:"lastName"`
	MiddleName           string `json:"middleName"`
	PreferredName        string `json:"preferredName"`
	CountryCode          string `json:"countryCode"`
	PhoneNumber          string `json:"phoneNumber"`
	BusinessCountryCode  string `json:"businessCountryCode"`
	BusinessPhone        string `json:"businessPhone"`
	DateOfBirth          string `json:"dateOfBirth"`
	Address1             string `json:"address1"`
	Address2             string `json:"address2"`
	City                 string `json:"city"`
	State                string `json:"state"`
	ZipCode              string `json:"zipCode"`
	Country              string `json:"country"`
	CitizenshipCountry   string `json:"citizenshipCountry"`
	JobTitle             string `json:"jobTitle"`
	Industry             string `json:"industry"`
	EmployerName         string `json:"employerName"`
	EmployerAddress      string `json:"employerAddress"`
	PasswordChanged      bool   `json:"password_changed"`
	Logout               bool   `json:"logout"`
	TestAccount          bool   `json:"test_account"`
	OptInMarketing       bool   `json:"optInMarketing"`
	AllowMultipleLogins  bool   `json:"allowMultipleLogins"`
	WhitelistIPAddresses string `json:"whitelistIpAddresses"`
	ReferralCode         string `json:"referralCode"`
}
