package mcpserver

import (
	"sort"
	"time"

	"magicmarkets-cli/internal/magicmarkets"
)

// Mappers between MCP tool types and the Magic Markets client types
// (internal/magicmarkets). Handlers must not pass API structs through as
// tool input/output.

func ptrStake(s *magicmarkets.Stake) *stake {
	if s == nil {
		return nil
	}
	v := stake{Currency: s.Currency, Amount: s.Amount}
	return &v
}

func stakeValue(s magicmarkets.Stake) stake {
	return stake{Currency: s.Currency, Amount: s.Amount}
}

func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func rfc3339(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339)
}

func priceLevelsFromAPI(levels []magicmarkets.PriceLevel) []priceLevel {
	out := make([]priceLevel, 0, len(levels))
	for _, l := range levels {
		out = append(out, priceLevel{
			Price: l.Effective.Price,
			Min:   ptrStake(l.Effective.Min),
			Max:   ptrStake(l.Effective.Max),
		})
	}
	return out
}

func bestPriceFromAPI(levels []magicmarkets.PriceLevel) *float64 {
	if len(levels) == 0 {
		return nil
	}
	p := levels[0].Effective.Price
	return &p
}

func parlayLegsFromAPI(legs []magicmarkets.ParlayLeg) []parlayLeg {
	if len(legs) == 0 {
		return nil
	}
	out := make([]parlayLeg, 0, len(legs))
	for _, l := range legs {
		out = append(out, parlayLeg{
			ID:                 l.ID,
			Sport:              l.Sport,
			EventID:            l.EventID,
			BetType:            l.BetType,
			BetTypeDescription: l.BetTypeDescription,
			Price:              l.Price,
			Outcome:            l.Outcome,
		})
	}
	return out
}

func eventSummaryFromAPI(e magicmarkets.EventInfo) eventSummary {
	return eventSummary{
		EventType:       e.EventType,
		EventID:         derefStr(e.EventID),
		EventName:       e.EventName,
		HomeTeam:        derefStr(e.HomeTeam),
		AwayTeam:        derefStr(e.AwayTeam),
		CompetitionName: e.CompetitionName,
		StartTime:       rfc3339(e.StartTime),
	}
}

func eventInfoFromAPI(e *magicmarkets.EventInfo) *eventInfo {
	if e == nil {
		return nil
	}
	info := eventInfo{
		EventType:          e.EventType,
		EventID:            derefStr(e.EventID),
		EventName:          e.EventName,
		HomeTeam:           derefStr(e.HomeTeam),
		AwayTeam:           derefStr(e.AwayTeam),
		CompetitionName:    e.CompetitionName,
		CompetitionCountry: e.CompetitionCountry,
		StartTime:          rfc3339(e.StartTime),
		Date:               e.Date,
		RunnerCount:        len(e.Teams),
	}
	if n := len(e.LegEventInfos); n > 0 {
		info.Legs = make([]eventSummary, 0, n)
		for _, leg := range e.LegEventInfos {
			info.Legs = append(info.Legs, eventSummaryFromAPI(leg))
		}
	}
	return &info
}

func betsFromAPI(bets []magicmarkets.Bet) []bet {
	if len(bets) == 0 {
		return nil
	}
	out := make([]bet, 0, len(bets))
	for _, b := range bets {
		row := bet{
			BetID:        b.BetID,
			OrderID:      b.OrderID,
			Status:       b.Status.Code,
			StatusReason: b.Status.Reason,
			Sport:        b.Sport,
			EventID:      derefStr(b.EventID),
			BetType:      b.BetType,
			WantPrice:    b.WantPrice,
			GotPrice:     b.GotPrice,
			WantStake:    ptrStake(b.WantStake),
			GotStake:     ptrStake(b.GotStake),
			ProfitLoss:   ptrStake(b.ProfitLoss),
			ExchangeRole: derefStr(b.ExchangeRole),
		}
		out = append(out, row)
	}
	return out
}

func orderFromAPI(o *magicmarkets.Order) order {
	if o == nil {
		return order{}
	}
	return order{
		OrderID:            o.OrderID,
		OrderType:          o.OrderType,
		BetType:            o.BetType,
		BetTypeDescription: o.BetTypeDescription,
		Sport:              o.Sport,
		WantPrice:          o.WantPrice,
		WantStake:          ptrStake(o.WantStake),
		PlacementTime:      rfc3339(o.PlacementTime),
		ExpiryTime:         rfc3339(o.ExpiryTime),
		Closed:             o.Closed,
		CloseReason:        derefStr(o.CloseReason),
		Event:              eventInfoFromAPI(o.EventInfo),
		Bets:               betsFromAPI(o.Bets),
		UserData:           derefStr(o.UserData),
		Status:             o.Status,
		KeepOpenIR:         o.KeepOpenIR,
		ExchangeMode:       derefStr(o.ExchangeMode),
		Price:              o.Price,
		Stake:              ptrStake(o.Stake),
		ProfitLoss:         ptrStake(o.ProfitLoss),
		Legs:               parlayLegsFromAPI(o.Legs),
	}
}

func ordersFromAPI(orders []magicmarkets.Order) []order {
	out := make([]order, 0, len(orders))
	for i := range orders {
		out = append(out, orderFromAPI(&orders[i]))
	}
	return out
}

func heartbeatFromAPI(h *magicmarkets.Heartbeat) heartbeat {
	if h == nil {
		return heartbeat{}
	}
	return heartbeat{
		HeartbeatID: h.HeartbeatID,
		ExpiryTime:  rfc3339(h.ExpiryTime),
	}
}

func heartbeatsFromAPI(hbs []magicmarkets.Heartbeat) []heartbeat {
	out := make([]heartbeat, 0, len(hbs))
	for i := range hbs {
		out = append(out, heartbeatFromAPI(&hbs[i]))
	}
	return out
}

func ratesFromAPI(rates []magicmarkets.XRate) []rate {
	out := make([]rate, 0, len(rates))
	for _, r := range rates {
		out = append(out, rate{Ccy: r.Ccy, Rate: r.Rate})
	}
	return out
}

func balanceFromAPI(bal *magicmarkets.Balance) balanceResult {
	if bal == nil {
		return balanceResult{}
	}
	return balanceResult{
		Balance:     stakeValue(bal.Balance),
		OpenStake:   stakeValue(bal.OpenStake),
		Available:   bal.Balance.Amount - bal.OpenStake.Amount,
		SmartCredit: ptrStake(bal.SmartCredit),
	}
}

func betslipFromAPI(bs *magicmarkets.Betslip) betslip {
	if bs == nil {
		return betslip{}
	}
	out := betslip{
		BetslipID:          bs.BetslipID,
		Sport:              bs.Sport,
		EventID:            bs.EventID,
		BetType:            bs.BetType,
		BetTypeDescription: bs.BetTypeDescription,
		BetslipType:        bs.BetslipType,
		IsOpen:             bs.IsOpen,
		ExpiresAt:          rfc3339(bs.ExpiresAt()),
		ExpiresInSeconds:   int(time.Until(bs.ExpiresAt()).Seconds()),
		Prices:             priceLevelsFromAPI(bs.PriceList),
		BestPrice:          bestPriceFromAPI(bs.PriceList),
		TotalAvailable:     ptrStake(bs.Total),
		Legs:               parlayLegsFromAPI(bs.Legs),
	}
	if bs.CloseReason != nil {
		out.CloseReason = *bs.CloseReason
	}
	return out
}

func eventFromStream(e magicmarkets.StreamEvent) event {
	row := event{
		Sport:           e.Sport,
		EventID:         e.EventID,
		EventType:       e.EventType,
		EventName:       e.EventName,
		CompetitionName: e.CompetitionName,
		Country:         e.CompetitionCountry,
		IRStatus:        e.IRStatus,
		StartTime:       rfc3339(e.StartTime),
	}
	if e.EventType == "multirunner" {
		row.RunnerCount = len(e.Teams)
	} else {
		row.Home = e.Home
		row.Away = e.Away
	}
	return row
}

func offerFromStream(o magicmarkets.Offer) offer {
	return offer{
		BetType:    o.BetType,
		MarketType: o.MarketType,
		InRunning:  o.InRunning,
		Prices:     priceLevelsFromAPI(o.PriceList),
		BestPrice:  bestPriceFromAPI(o.PriceList),
	}
}

func positionGridFromAPI(g *magicmarkets.PositionGrid) *positionGrid {
	if g == nil {
		return nil
	}
	return &positionGrid{CcyCode: g.CcyCode, Values: g.Values}
}

func positionFromAPI(p *magicmarkets.Position) position {
	if p == nil {
		return position{}
	}
	out := position{
		PayoffGrid:     positionGridFromAPI(p.PayoffGrid),
		UnknownBetsNum: p.UnknownBetsNum,
		UnknownGrid:    positionGridFromAPI(p.UnknownGrid),
		Sport:          p.Sport,
		EventID:        p.EventID,
		Event:          eventInfoFromAPI(p.EventInfo),
		Totals:         make([]positionTotal, 0, len(p.Totals)),
	}
	keys := make([]string, 0, len(p.Totals))
	for k := range p.Totals {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		t := p.Totals[k]
		out.Totals = append(out.Totals, positionTotal{
			BetType:            k,
			BetTypeDescription: t.BetTypeDescription,
			GotPrice:           t.GotPrice,
			GotStake:           ptrStake(t.GotStake),
			UnknownPrice:       t.UnknownPrice,
			UnknownStake:       ptrStake(t.UnknownStake),
			PayoffGrid:         positionGridFromAPI(t.PayoffGrid),
		})
	}
	if p.CashoutInfo != nil {
		c := p.CashoutInfo
		out.Cashout = &cashoutInfo{
			Allowed:          c.Allowed,
			Reason:           derefStr(c.Reason),
			Valuation:        ptrStake(c.Valuation),
			Stake:            ptrStake(c.Stake),
			SmartCreditDelta: ptrStake(c.SmartCreditDelta),
			Position:         positionGridFromAPI(c.Position),
		}
	}
	return out
}

func (in getPositionInput) orderFilter() magicmarkets.OrderFilter {
	return magicmarkets.OrderFilter{
		Sport:   nonEmpty(in.Sport),
		EventID: nonEmpty(in.EventID),
		Status:  parseCSV(in.Status),
	}
}

func (in listOrdersInput) orderFilter() magicmarkets.OrderFilter {
	return magicmarkets.OrderFilter{
		Status:    parseCSV(in.Status),
		Sport:     parseCSV(in.Sport),
		EventID:   parseCSV(in.EventID),
		OrderType: parseCSV(in.OrderType),
		DateFrom:  in.DateFrom,
		DateTo:    in.DateTo,
		Search:    in.Search,
	}
}

func (in createBetslipInput) request() magicmarkets.CreateBetslipRequest {
	betslipType := in.BetslipType
	if betslipType == "" {
		betslipType = magicmarkets.BetslipNormal
	}
	return magicmarkets.CreateBetslipRequest{
		Sport:         in.Sport,
		EventID:       in.EventID,
		BetType:       in.BetType,
		BetslipType:   betslipType,
		UserData:      in.UserData,
		ExcludeDanger: in.ExcludeDanger,
	}
}

func (in placeOrderInput) request(price float64) magicmarkets.CreateOrderRequest {
	duration := in.Duration
	if duration == 0 {
		duration = 15
	}
	req := magicmarkets.CreateOrderRequest{
		BetslipID:    in.BetslipID,
		Price:        price,
		Stake:        magicmarkets.USDT(in.Stake),
		Duration:     duration,
		ExchangeMode: in.ExchangeMode,
		RequestUUID:  in.RequestUUID,
		UserData:     in.UserData,
		KeepOpenIR:   in.KeepOpenIR,
	}
	if in.AcceptPartialFill != nil && !*in.AcceptPartialFill {
		req.AcceptPartialFill = in.AcceptPartialFill
	}
	if in.AcceptBetterPrice != nil && !*in.AcceptBetterPrice {
		req.AcceptBetterPrice = in.AcceptBetterPrice
	}
	return req
}
